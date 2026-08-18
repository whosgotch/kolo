# Probe findings

Findings 1–5 are Milestone 0, probed against `claude` (Claude Code v2.1.226) in a
PTY at 120x40 on macOS, `TERM=xterm-256color`, child confined to a scratch
directory. Finding 6 is roadmap step 6 and probes a second kind against the same
questions; it is here rather than in its own file because it is an answer to
finding 4.

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

## 6. A second kind does not wear its state for the whole turn

Probed against `codex` (codex-cli 0.147.0), same PTY at 120x40, driven by
`scripts/codex-idle.txt` and `scripts/codex-busy.txt`. This is roadmap step 6,
and the point of it was to find out whether the marker seam is real or whether it
is a shape Claude Code happens to fit. It is the second.

Two of the three markers carry over, one of them exactly:

| | Claude Code | codex |
|---|---|---|
| working | `esc to interrupt` | `Working (2s • esc to interrupt)` |
| dialog selection | `❯ 1.` | `› 1.` |
| dialog footer | `Esc to cancel` | `Press enter to continue` |
| idle | `? for shortcuts` | **nothing** |

The idle row is the finding. Claude Code keeps a hint under its input box in
every state, so idle is a thing the screen says. codex's box carries a rotating
placeholder suggestion (`› Summarize recent commits`) and under it a status line
of model and directory — both present while it works and while it waits, neither
saying which.

Worse, the working line is not up for the whole turn. Sampled once a second
across one turn, `esc to interrupt` was on screen for the first three seconds and
gone for the next eight, while the reply was still streaming a line at a time:

```
t01  esc to interrupt    ◦ Working (0s • esc to interrupt)
t02  esc to interrupt    ◦ Working (1s • esc to interrupt)
t03  esc to interrupt    • Working (2s • esc to interrupt)
t04  —                   • 1. One, starting steadily.
...                                 (still growing)
t11  —
t12  —                   (finished; t12, t15 and a dump 10s later are identical)
```

Diffed against the settled screen, a mid-stream screen differs only in how much
of the transcript is on it. So for eight seconds of a twelve-second turn, codex
working and codex waiting are the same picture, and the only thing telling them
apart is that one of them is *changing*.

That breaks the assumption underneath `internal/detect`, which is that a state is
a property of one screen. It is not, for this kind. Nothing unsafe follows today
— an unrecognised screen is Unknown and Unknown holds the queue, so a codex agent
is simply never sendable — but a second kind that can never be sent anything is
not a second kind.

What the seam has to become: a kind declares its markers *and* whether idle is
allowed to be inferred from the screen having stopped moving. Claude Code says no
and keeps a pure per-screen answer. codex says yes, and its idle is "no working
line, and nothing has changed for a settle period". The settle period is the cost
of a TUI that does not announce itself, and it is paid per kind rather than by
everybody.

## 7. The screen kolo was written against has changed

Probed against `claude` (Claude Code v2.1.234) in the same PTY at 120x40, driven
by `scripts/claude-turn.txt`. This was not a scheduled probe: kolo was run for
real against a real agent, and the page said *"kolo does not recognise this
screen"* while the agent sat plainly idle at its input box.

The hint finding #4 turns on is gone. Three footers, one version apart:

| | v2.1.226 | v2.1.234 |
|---|---|---|
| idle | `⏸ manual mode on · ? for shortcuts · ← for agents` | `⏵⏵ auto mode on (shift+tab to cycle) · ← for agents` |
| working | `… · esc to interrupt · …` | `⏵⏵ auto mode on (shift+tab to cycle) · esc to interrupt · ← for agents` |
| idle, something in the box | — | `⏵⏵ auto mode on (shift+tab to cycle)` |

Three things follow, in order of how much they cost:

1. **Idle has no hint of its own any more.** It is the footer *minus* the working
   segment, which makes idle an absence rather than a presence — the thing the
   detector was written to avoid. What is left to key on is the permission mode,
   which is on screen in every state, so the order the states are tested in is
   now what does the work rather than a last line of defence.
2. **The footer sheds segments as soon as the box has anything in it.** `← for
   agents` is there on an empty box and gone on a full one, so a marker that
   looked stable across two versions is not stable across two seconds.
3. **A dialog no longer replaces the input box.** The auto-mode question is drawn
   *above* it, so one screen now carries a question and the idle footer at once.
   Finding #4's arrangement — dialogs hide the box — is no longer true, and
   testing Dialog first is the only reason a queued line does not go out under a
   question nobody has answered.

An agent kind's markers are not a fact about the kind. They are a fact about the
version of it that was recorded, and the day it upgrades is the day the org's
agents stop taking messages, silently. Hence a list per state rather than a
string, holding both versions at once.

## 8. Two writes are not enough — the Enter has to arrive late

Found the same way: a message typed from the browser landed in the agent's input
box and stayed there, with the cursor on a second line. The Enter had been read
as a newline in pasted content — finding #3 all over again, except that kolo was
already writing the text and the Enter separately, as #3 says to.

What #3 missed is that the recorder which proved it slept 150ms between the two
writes (`cmd/kolorec`), and the relay did not. Separate writes are necessary and
not sufficient: an Enter hard behind the text is still inside the agent's paste
window. With the two writes back to back the line submits sometimes — which is
worse than never, because it looks like it works.

The relay now waits `enterDelay` (150ms, the value the recorder has always used)
between them. It is a timing guess about somebody else's TUI, and it is the kind
of thing to re-probe rather than trust: a slower machine may need more.

## Incidental

- codex asks the same trust question on a fresh directory, with the same shape
  and the same danger as finding 5: two numbered options, the safe-sounding one
  first, and Enter answers it.
- Model prose is a rich source of false dialogs. `count from one to twenty`
  renders as `1.`, `2.`, `3.` down consecutive lines — exactly what
  `detect.Options` looks for. It is only safe because `Options` refuses to read
  anything unless `Of` already said Dialog.
- The dimmed text that appears in the input box (`ESC[2m`) is Claude Code's own
  suggested-prompt placeholder, not an emulator artifact. It is not real input.
- The child inherits `CLAUDE_CODE_CHILD_SESSION`, which disables transcript
  saving and prints a warning line in the footer. Kolo should decide whether to
  scrub that variable before exec.
- Claude Code does not use the alternate screen at the trust prompt but does once
  the main TUI is up.
