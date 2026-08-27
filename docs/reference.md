# kolo reference

How kolo works, and why. For *running* it — `kolo up`, a hub and hosts on
separate machines, TLS, every flag — the binary answers better than a file
can: `kolo help`, `kolo help <command>`, and `kolo <command> -h`. For the
threat model and how to report a hole, see [SECURITY.md](../SECURITY.md).

kolo puts long-lived CLI agents (Claude Code, opencode, anything that draws a
terminal) in front of a whole team. A **host** machine lends itself to the org
and runs the agents under pseudo-terminals. Everyone else watches each agent's
screen live in a browser and types at it directly. Whoever types last drives,
and everybody sees who that is.

The host dials out to the hub over one websocket and never accepts an inbound
connection: no open port, no firewall rule, no tunnel. Only hosts install kolo.
Members open a link. Everything ships as one Go binary.

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

Agents are communal. Anyone may watch, type at, or stop any of them. There are
no roles; every action is attributed instead, and that record keeps the
arrangement workable.

Everything a machine remembers lives in `~/.kolo`: `$KOLO_HOME` moves the lot,
and `-org`, `-state` and `-tls-cache` move one file each.

## Agents

Any command that draws a terminal will run: the org can watch it, type at it
and stop it. Two things depend on knowing the kind, and both are read off the
agent's own screen:

- which state it is in (idle, busy, asking something), so the list can say what
  agents are doing instead of showing black rectangles
- how to resume its conversation, without which every restart is a fresh start

Kolo ships descriptions for `claude` and `opencode`. Describe others in
`~/.kolo/kinds.json`, which lives on the host. An entry there replaces a
shipped kind outright; the two are never merged.

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

| field | meaning |
|---|---|
| `idle` | hints the input box shows when it can take a line; any one matches |
| `busy` | what the screen says while working — without it, working reads as waiting |
| `dialogFooter` / `dialogSelected` | recognise a question; never used to answer it |
| `resume` | args appended to continue the last conversation |
| `pin` | args carrying `{session}`, filled with an id kolo mints at first launch; the same id goes back in `resume`. For agents that take a session id at start (`claude --session-id`) |
| `continue` | appended when a restart has no id to resume by; only safe while this agent is alone in its directory |
| `session` | pattern whose capture is the id read off the screen, for kinds that print their conversation id (`resume` carries `{session}`) |
| `interrupt` | key that stops it: `esc`, `ctrl+c`, or one character; default `esc`. Sent only while `busy` is up |
| `settle` | seconds the screen must sit unchanged to read idle, for kinds whose idle is silence |

Markers are literal strings from a real screen. Record one with `cmd/kolorec`
instead of guessing at it. After an agent upgrades, `kolo doctor` will tell you
whether its kind still fits; it also reports what a machine can run and lend,
reads the state file, and exits non-zero, so it can end a setup script.

A kind with no description still runs, and the org can watch it and type at it.
What it loses is the status in the list and its conversation across restarts.
Its screen reads as *unknown*, and kolo claims nothing more about it.

**Directory sharing**: one agent of each kind per directory, because most kinds
resume by "the last conversation in this directory" and two of the same kind
would come back as each other. Kinds that name or pin their conversations can
prove which one is theirs and may share. Sharing a directory still means
sharing its files, and kolo does not referee that.

## Input model

Members type at the agent themselves. There is nothing to take and nobody to
ask: keystrokes relay as pressed, whoever's keys arrived last is announced to
watchers as the typist, and everything typed lands in the log attributed. Two
people typing at once interleave keystrokes in view of everybody, much like two
people reaching for one keyboard in a room. Nothing is gated, because the
member can see the screen their keys land on. Pressing Enter at a question is a
decision they are making with their eyes open.

What kolo presses on somebody's behalf, and when:

| action | offered by | permitted when |
|---|---|---|
| stop | the page | always |
| interrupt | the protocol only | only while the kind's own busy marker is up, using that kind's key |
| restart / start fresh | the protocol only | always |

The page draws a stop control and nothing else. Interrupt, restart and start
fresh travel on the watch websocket and are honoured by the host, but the page
stopped offering buttons for them once it turned out nobody reached for them.
Anything that speaks the protocol can still send them, and the log records them
like every other action.

Answering a question is not on that list. Kolo says a question is up; the
question itself is on the screen, and answering it means typing. An earlier
version read choices off dialogs and offered buttons. It held for exactly one
agent's dialog and guessed at everybody else's. If an agent ever hands its
questions over through a real interface, kolo will use that instead.

## Restart and resume

Agents are supervised: an exit is restarted, one that will not stay up is
marked failed instead of being restarted forever, and a human restart does not
count toward giving up. What is running is written to `-state`, so restarting
the host, or the whole machine, brings the org's agents back in the order they
were made.

Resume comes in two shapes: ask for the last conversation (`--continue`), or
name one (`--resume {session}`, with the id read off the screen or pinned at
birth). The id travels in the state file, so it survives the machine. A failed
resume starts clean and says so out loud, because losing context silently is
worse than losing it visibly. Start fresh drops the conversation deliberately,
and is logged like every other action.

## The log

Every member action is recorded on the hub beside the org file (JSON lines,
`GET /v1/log`): created, said, interrupted, restarted, stopped, failed. Typed
lines are reconstructed from keystrokes and written only on Enter, so an
abandoned half-line is never recorded. Because it reconstructs, pastes and
arrow-key menu choices will not read back exactly. Nothing an agent prints is
kept.

The log stands in for roles. Everyone may do everything, because everyone can
see who did.

## Screen size

Each watching browser proposes the grid it can draw, and the smallest wins, as
in tmux. Anything wider than the smallest window would be drawn by an agent
that cannot see where it is being cut off.

## Status and layout

Working and tested across two machines: watching, typing, stopping,
restart/resume (including pinned identities), discovery of installed agents
(`-allow '*'`), the journal. Not built yet: notification when an agent stalls
on a dialog while nobody watches; putting the journal in front of people in the
UI; buttons for the interrupt, restart and start-fresh the protocol already
carries.

```
brand             the mark: geometry, tokens, and the icon build
cmd/kolo          the binary: up, serve, host, invite, token, who, doctor
cmd/kolorec       records an agent session as a test fixture, and its scripts
internal/adapter  agent-kind table: markers, resume, pin, interrupt
internal/agent    PTY process management
internal/config   ~/.kolo
internal/detect   screen state classification
internal/host     the machine half: supervision, state file, streaming
internal/hub      the server half: auth, registry, screens, keyboard, journal
internal/relay    sole writer to a PTY; keystroke gating rules
internal/session  one agent's screen fanned out to viewers
internal/term     vt10x-backed screen model and repaint
internal/ui       embedded web page and assets
```

The page lives in `internal/ui` rather than a top-level `web/` for two reasons:
`go:embed` cannot reach outside its own package, so `index.html` has to sit
beside the Go file that embeds it, and `internal` keeps it from becoming
importable API that the module would then owe compatibility on.

The icons and token stylesheet under `internal/ui/assets` are checked in, but
they are build output: `node brand/build.mjs` regenerates them from
`brand/ring.ts` and `brand/tokens.css`, which needs Node and a Chrome. Edit the
sources under `brand/`, never the generated files.
