# Kolo reference

How kolo works, and why it works that way.

To *run* it, the binary is the better manual: `kolo help`, `kolo help <command>`
and `kolo <command> -h` cover every command and flag. For the threat model and
how to report a hole, see [SECURITY.md](../SECURITY.md).

## Contents

- [The shape of it](#the-shape-of-it)
- [Agents](#agents)
- [Typing and control](#typing-and-control)
- [Restart and resume](#restart-and-resume)
- [The log](#the-log)
- [Screen size](#screen-size)
- [Where files live](#where-files-live)
- [What works today](#what-works-today)
- [Repo layout](#repo-layout)

## The shape of it

Kolo runs long-lived CLI agents (Claude Code, opencode, anything that draws a
terminal) so a whole team can use them.

There are two halves:

- a **host**, a machine somebody lends to the team, which runs the agents under
  pseudo-terminals
- a **hub**, the server everyone opens in a browser

```
   browser      browser                       host machine
   (dana)       (artem)                     ┌─────────────────┐
      │            │                        │ kolo host       │
      └─────┬──────┘                        │   ├ agent (PTY) │
            ▼                               │   ├ agent (PTY) │
        ┌───────┐ ◄──── outbound websocket ─┤   └ agent (PTY) │
        │  hub  │                           └─────────────────┘
        └───────┘
```

`kolo up` runs both halves in one process, which is the usual way to start.
`kolo serve` and `kolo host` split them across machines.

**The host dials out.** It opens one websocket to the hub and never accepts an
inbound connection. No open port, no firewall rule, no tunnel.

**Only hosts install kolo.** Everyone else opens a link. It is one Go binary
with nothing beside it.

**Agents are communal.** Anyone in the org can watch any agent, type at it, or
stop it. There are no roles and no permissions. Instead every action is
recorded against the person who took it, and that record is what makes the
arrangement work.

## Agents

Any command that draws a terminal will run. The org can watch it, type at it,
and stop it without kolo knowing anything about it.

Two things do need kolo to know the kind of agent, and it reads both off the
agent's own screen:

1. **What state it is in** (idle, busy, or asking a question), so the agent
   list can say what each one is doing instead of showing black rectangles.
2. **How to resume its conversation.** Without this, every restart starts over.

Kolo ships descriptions for `claude` and `opencode`.

### Describing another kind

Put it in `~/.kolo/kinds.json` on the host. An entry there replaces a shipped
kind completely. The two are never merged.

```json
{
  "codex": {
    "markers": {
      "idle": ["? for shortcuts"],
      "busy": "esc to interrupt",
      "dialogFooter": "Esc to cancel",
      "dialogSelected": "❯"
    },
    "resume": ["--continue"]
  }
}
```

**Reading the screen.** These say how to tell what the agent is doing.

| field | what it is |
|---|---|
| `idle` | hints the input box shows when it can take a line. Any one of them matching is enough |
| `busy` | what the screen says while the agent is working. Without it, working looks the same as waiting |
| `dialogFooter`, `dialogSelected` | how to recognise that a question is up. Never used to answer one |
| `settle` | seconds the screen must sit unchanged before it reads as idle. For agents whose idle state is silence |

**Resuming a conversation.** These say how to bring one back after a restart.

| field | what it is |
|---|---|
| `resume` | arguments appended to continue the last conversation |
| `continue` | arguments used when a restart has no id to resume by. Only safe while this agent is alone in its directory |
| `pin` | arguments carrying `{session}`, filled with an id kolo mints at first launch. The same id goes back into `resume`. For agents that accept a session id at start, like `claude --session-id` |
| `session` | a pattern whose capture is the conversation id, read off the screen. For agents that print their id. Use it when `resume` carries `{session}` |

**Stopping it.**

| field | what it is |
|---|---|
| `interrupt` | the key that stops this agent: `esc`, `ctrl+c`, or a single character. Defaults to `esc`. Only sent while the `busy` marker is on screen |

### Getting the markers right

Markers are literal strings copied from a real screen, not guesses. Record a
real session with `cmd/kolorec` and read them off the dump.

After an agent updates, run `kolo doctor`. It says whether each kind still fits
what the agent draws, what this machine can run and lend, and exits non-zero,
so it can be the last line of a setup script.

An agent kolo has no description for still runs, and the org can still watch it
and type at it. What it loses is its status in the list and its conversation
across restarts. Its screen reads as *unknown*, and kolo claims nothing more
about it.

### One of each kind per directory

Most agents resume by asking for "the last conversation in this directory", so
two of the same kind in one directory would come back as each other. Kolo
refuses that.

Agents that name or pin their conversations can prove which one is theirs, so
they are allowed to share a directory.

Sharing a directory still means sharing its files. Kolo does not referee that.

## Typing and control

Anyone in the org can type at any agent. There is no lock to take and nobody to
ask.

Keystrokes go to the agent as you press them. Whoever typed last is shown to
everyone else as the typist. Everything typed goes into the log under your
name.

If two people type at once their keystrokes interleave, and everyone watching
sees it happen. It works the way two people reaching for one keyboard works.

Nothing is gated, because you can see the screen your keys are landing on.
Pressing Enter at a question is a decision made with your eyes open.

A paste arrives as one message rather than a key at a time, and goes through
whole up to 64 KB. Past that the host refuses it and says so on the screen,
because a paste that quietly went nowhere would look like one that worked.

### What kolo can press for you

| action | offered by | allowed when |
|---|---|---|
| stop | the page | always |
| interrupt | the protocol only | only while that kind's `busy` marker is on screen, using that kind's key |
| restart, start fresh | the protocol only | always |

The page draws a stop control and nothing else. Interrupt, restart and start
fresh travel on the watch websocket and the host honours them, but the page
stopped offering buttons for them once it was clear nobody used them. Anything
speaking the protocol can still send them, and the log records them like any
other action.

### Why there is no button to answer a question

Kolo tells you a question is up. It does not tell you what the question is, and
it will not answer one for you. The question is on the screen, and answering it
means typing.

An earlier version read the choices off dialogs and drew buttons. It worked for
exactly one agent's dialog and guessed at everyone else's. If an agent ever
offers its questions through a real interface, kolo will use that instead.

## Restart and resume

**Agents are supervised.** If one exits, kolo starts it again. One that will
not stay up is marked failed rather than restarted forever. A restart somebody
asked for does not count toward giving up.

**What is running is written down**, to the file `-state` names. Restarting the
host, or the whole machine, brings the org's agents back in the order they were
created.

**Resume works two ways:**

- ask for the last conversation, with `--continue` or similar
- name a conversation, with `--resume {session}`, where the id was either read
  off the screen or pinned when the agent first started

The id is kept in the state file, so it survives the machine restarting.

**A failed resume starts clean and says so.** Losing context quietly is worse
than losing it visibly.

**Start fresh** drops the conversation on purpose, and is logged like anything
else.

## The log

Every action a member takes is recorded on the hub, beside the org file, as
JSON lines. Read it with `GET /v1/log`.

Recorded: created, said, interrupted, restarted, stopped, failed.

**Typed lines are rebuilt from keystrokes** and only written when you press
Enter, so a line you abandon halfway is never recorded. Because it is a
reconstruction, pasted text and menu choices made with arrow keys will not read
back exactly.

**Nothing an agent prints is kept.** Only what people did.

The log is what stands in for roles. Everyone may do everything, because
everyone can see who did what.

## Screen size

Every browser watching proposes the grid it can draw, and the smallest one
wins, the same way tmux handles it.

Anything wider than the smallest window would be drawn by an agent that cannot
see where it is being cut off.

## Where files live

Everything a machine remembers is in `~/.kolo`.

| | |
|---|---|
| `$KOLO_HOME` | moves the whole directory |
| `-org` | moves the org file |
| `-state` | moves the record of what is running |
| `-tls-cache` | moves the certificate store |

## What works today

**Working, and tested across two machines:** watching, typing, stopping,
restart and resume (including pinned conversation ids), discovery of installed
agents (`-allow '*'`), and the log.

**Not built yet:** a notification when an agent stalls on a question and nobody
is watching; the log shown in the browser; buttons for the interrupt, restart
and start-fresh that the protocol already carries.

## Repo layout

```
brand             the mark: geometry, tokens, and the icon build
cmd/kolo          the binary: up, serve, host, invite, token, who, doctor
cmd/kolorec       records an agent session as a test fixture, and its scripts
internal/adapter  agent-kind table: markers, resume, pin, interrupt
internal/agent    PTY process management
internal/config   ~/.kolo
internal/detect   screen state classification
internal/host     the machine half: supervision, state file, streaming
internal/hub      the server half: auth, registry, screens, journal, the page
internal/relay    sole writer to a PTY; keystroke gating rules
internal/session  one agent's screen fanned out to viewers
internal/term     vt10x-backed screen model and repaint
```

**Why the page lives in `internal/hub/ui`.** `go:embed` can reach into a
directory below the package it is written in, but never outside one. A page in
a package of its own would be a package whose only job was holding one embed
directive. `internal` keeps it from becoming importable API the module would
owe compatibility on.

**The icons and token stylesheet are build output.** They are checked in under
`internal/hub/ui/assets`, but `node brand/build.mjs` regenerates them from
`brand/ring.ts` and `brand/tokens.css`. That needs Node and a Chrome. Edit the
sources under `brand/`, never the generated files.
