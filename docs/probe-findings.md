# Milestone 0 — Probe findings

Probed against `claude` (Claude Code v2.1.226) in a PTY at 120x40 on macOS,
`TERM=xterm-256color`, child confined to a scratch directory.

The probe itself lives on `probe/milestone-0` and is not merged. Reproduce with:

```bash
go build -o /tmp/probe ./probe && /tmp/probe -script probe/scripts/q3b-isolate.txt -out /tmp/out -dir /tmp/sandbox claude
```

Two tests on that branch are **expected to fail** — they are the evidence for
finding 1: `TestOSCTitleLeaks/bel_with_leading_rune` and `TestReplay/charmbracelet`.

---

## 1. The chosen VT emulator is broken for this agent — switch to vt10x

`github.com/charmbracelet/x/vt` leaks OSC payloads onto the screen when the
payload contains a non-ASCII rune. Reduced to a unit test:

```go
term.Write([]byte("hello\r\n"))
term.Write([]byte("\x1b]0;✳ SECRET TITLE\x07"))   // set window title
term.Write([]byte("world\r\n"))
// renders: "hello\n SECRET TITLEworld\n"
```

Pure ASCII payloads are handled correctly, with either `BEL` or `ST` terminator.
The `✳` is consumed and everything after it is printed as text.

This is not a corner case here. Claude Code sets the window title on every frame
with a leading spinner glyph — `ESC]0;✳ Create note2.txt file with hello BEL`,
cycling `✳ ⠂ ⠐` — so the corruption fires continuously during normal work. In the
recorded sessions it overwrote a modal's option list, turning
`2. Switch to Sonnet 5 and continue` into `Respond with PONGnet 5 and continue`.

Replaying a real captured session through both candidates: `x/vt` leaks the
title onto the screen, `hinshun/vt10x` does not. vt10x also renders the TUI's box
drawing, braille and block glyphs correctly, and handled the full session
end-to-end.

**Decision: use `github.com/hinshun/vt10x`.** Two consequences to plan around:

- `vt10x.String()` is plain text with no styling. Colour and bold survive per
  cell via `term.Cell(x, y)` → `Glyph{Char, Mode, FG, BG}`, which is the access
  path Kolo needs anyway to build the grid snapshot.
- The glyph attribute bits (`attrBold`, `attrReverse`, …) are **unexported**.
  Kolo must mirror the constants from `state.go`. The library is frozen (2022),
  so this is stable but worth a comment at the mirror site.

## 2. The emulator's reply stream must be drained, or the process deadlocks

`x/vt` answers terminal-capability queries by writing to an internal pipe. Claude
Code issues a Device Attributes query on startup; with nobody reading that pipe,
the emulator blocked mid-`Write` while holding its lock and the whole process
deadlocked on the first screen dump. Draining it and forwarding to the child's
stdin produced the expected `ESC[?62;1;6;22c`.

vt10x does not generate replies at all. Claude Code booted and ran fine without
them, so this is not a blocker for the switch — but it is the reason the runner
must never assume "no reply stream" means "nothing to drain" if the emulator is
ever changed back.

Where replies exist they must go through the **same mutex as guest input**: a
capability reply landing in the middle of a guest's line would corrupt it.

## 3. A bundled `text + \r` write does not submit — Enter must be a separate write

The spec's invariant is "whole line + `\r` in a single write". Against Claude Code
that does not submit the prompt. The text lands in the input box and sits there;
a second bundled line just appends as a second line of a multiline input. Sixty
seconds later nothing had been sent.

Writing the text, then `\r` as a **separate** write, submits correctly and the
agent answers.

The likely cause is paste detection: a chunk arriving as one read looks like a
paste, and a pasted `\r` is a literal newline rather than a submit.

**This changes the injection design.** Writes must still be atomic as a unit —
one mutex, nothing interleaved — but the unit is "text, then Enter as a separate
write", not "text and Enter in one write". Worth checking whether an explicit
bracketed-paste wrapper (`ESC[200~ … ESC[201~` then Enter) is more robust than
relying on timing.

## 4. Question 2 — a line arriving mid-work queues as a follow-up. Yes.

With the agent generating, a second line submitted mid-answer is accepted and
held. The TUI shows it above the prompt and the footer becomes
`Press up to edit queued messages`:

```
  ❯ and when you are done, say BANANA
────────────────────────────────────────
❯ Press up to edit queued messages
```

When the current answer finishes, the queued line is picked up and answered
normally. This is exactly the behaviour Kolo wants, and the on-screen state is
detectable, which is a cheap source of truth for the protocol's `queued` message.

**Caveat, and it is a sharp one: this only holds while the agent is "busy" in
the model.** If the agent is busy *running a shell command*, stdin belongs to
that command, not to the prompt. A line injected then is swallowed by the child
process and lost outright — no queue, no echo, no error. First attempt at this
question hit exactly that: the agent answered "count to 30" by running
`for i in $(seq 1 30); do echo $i; sleep 1; done`, and the follow-up vanished.
`BANANA` appeared nowhere in the capture.

So the runner has three distinct agent states to tell apart, not two:

| state | injecting a line does |
|---|---|
| idle at the prompt | submits normally |
| busy generating | queues as a follow-up |
| busy running a shell command | **silently lost** |
| permission dialog up | **approves the default** (finding 5) |

Two of those four are failure modes. Whatever detector Kolo grows for finding 5
has to cover the shell-command case too, and guest messages must be held rather
than injected in both.

## 5. Question 3 — guest input during a permission dialog: confirmed dangerous

This is the finding that matters.

With `Do you want to create note2.txt?` on screen and `❯ 1. Yes` highlighted:

| sent | result |
|---|---|
| printable text only, no Enter | **discarded** — dialog unchanged, text not buffered anywhere |
| Enter alone | **approves the highlighted default** — file written |

So an ordinary guest message — `hi everyone, what are we working on?`, no control
characters of any kind — silently approves whatever tool call is on screen,
because Kolo has to send Enter to submit it. The guest's actual words are thrown
away. They see their message vanish; the host sees a command approved.

It was reproduced by accident first: the very first probe run hit Claude Code's
workspace-trust dialog, and the first guest line auto-answered "Yes, I trust this
folder".

The escalation is worse than "approve the default". If the highlighted option is
`2. Yes, allow all edits during this session`, a single guest line disables
prompting for the rest of the session.

**This cannot be deferred past Milestone 2.** Injecting a guest line while a
dialog is up is not a rare race — the dialog is exactly when a guest is most
likely to type, because the agent has visibly stopped.

Options, cheapest first:

1. **Hold input while a dialog is detected.** Detect the dialog from the screen
   grid (the `❯ 1. Yes` / `Esc to cancel` footer is distinctive) and queue guest
   messages until it clears. Screen-scraping is fragile against agent UI changes,
   and it fails open — a missed detection approves a command.
2. **Never let a guest's Enter reach a dialog.** Only flush a queued guest line
   when the screen shows the normal input prompt, and drop the Enter otherwise.
   Same detection problem, but the failure mode is a stuck queue rather than an
   approval.
3. **Run the agent so it cannot prompt** (pre-approved permission mode), making
   the host's up-front choice the entire trust boundary. Honest and robust, but
   strictly more dangerous per-action and a bigger claim to make in the README.

Recommendation: (2) for v1, plus documenting in `docs/security.md` that guest
messages are held while the agent is asking the host something. (1) and (2) share
the same detector; (2) just fails safe.

## Incidental

- The dimmed text that appears in the input box (`ESC[2m`) is Claude Code's own
  suggested-prompt placeholder, not an emulator artifact. It is not real input.
- The child inherits `CLAUDE_CODE_CHILD_SESSION`, which disables transcript
  saving and prints a warning line in the footer. Kolo should decide whether to
  scrub that variable before exec.
- Claude Code does not use the alternate screen at the trust prompt but does once
  the main TUI is up.
