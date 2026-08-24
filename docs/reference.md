# kolo

kolo puts long-lived CLI agents (Claude Code, opencode, anything that draws a
terminal) in front of a whole team. A **host** machine lends itself to the org
and runs the agents under pseudo-terminals; everyone else watches each agent's
screen live in a browser and types at it directly. Whoever types last drives;
everybody sees who that is.

The host dials out to the hub over one websocket and is never listened to: no
inbound port, no firewall rule, no tunnel. Only hosts install kolo — members
open a link. Everything ships as one Go binary.

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

Agents are communal: anyone may watch, type at, interrupt, restart or stop any of
them. There are no roles — every action is attributed instead, and the record of
who did what is what makes that workable.

## Running it

### One machine

```
$ cd ~/work/api
$ kolo up
```

`kolo up` makes whatever is missing: the org file (`~/.kolo/org.json`), a fresh
credential for this machine, and — the first time — a member token for whoever
ran it, printed once and stored nowhere. It lends the directory it was started
in and allows the agents kolo knows about and finds on `PATH`. `-dir` and
`-allow` say otherwise, and repeat. `-addr 127.0.0.1:7300` keeps it off the
network; `-tls-domain hub.acme.com` gets a Let's Encrypt certificate (ports 80
and 443 reachable, domain resolving here).

The printed link is the whole of joining: whoever opens it within ten uses /
seven days picks a name and is in. Later members come from `kolo invite`;
`kolo who` lists the org; `kolo invite -off team` kills a link without touching
whoever already came through it.

### Hub and hosts apart

```
$ mkdir -p ~/.kolo && echo '{"org": "acme"}' > ~/.kolo/org.json
$ kolo token -id dana -name "Dana"            # member credential, printed once
$ kolo token -host -id devbox -hub https://hub.acme.com
$ kolo serve -addr 0.0.0.0:7300               # the hub
$ kolo host -join <join string> -dir ~/work/api -allow claude   # on devbox
```

The org file holds hashes, never tokens, so it can live in version control. A
running hub re-reads it within seconds of a change — minting somebody lets them
in without a restart, removing their line shuts them out mid-connection. An edit
that does not parse is complained about and ignored.

Everything a machine remembers lives in `~/.kolo` (`$KOLO_HOME` moves it;
`-org`, `-state`, `-tls-cache` move individual files).

### Security posture

- `-dir` and `-allow` bound what may be **started**, not what a running agent can
  **reach**: it runs as the host user and can read `~/.ssh`. Run the host as a
  user that owns only what the org should have.
- The hub is a remote-code-execution endpoint; its authentication is what stands
  between the internet and the host's account. Plain HTTP is fine on a trusted
  network only — across the internet use `-tls-domain`.
- Every member can stop an agent mid-task or answer its permission dialog. That
  is the product; the log is what keeps it honest.

## Agents

Any command that draws a terminal will run: the org can watch it, type at it,
stop it and restart it. Two things depend on knowing the kind, and both are read
off the agent's own screen:

- which states it is in — idle, busy, asking something — so the list says what
  agents are doing instead of showing black rectangles
- how to resume its conversation, without which every restart is a fresh start

Kolo ships with these for `claude` and `opencode`. Describe others in
`~/.kolo/kinds.json` (the host's file; it replaces a shipped kind rather than
merging):

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

Markers are strings off a real screen, not source code — record one with
`cmd/kolorec` rather than guessing, and check a kind still fits after an agent
upgrade with `kolo doctor` (also reports what a machine can run/lend, exits
non-zero, reads the state file, and is meant to end setup scripts).

A kind with no description still runs, watches and types; what it loses is the
list saying what it is doing and its conversation across restarts. Its screen
reads as *unknown*, and kolo claims nothing about it.

**Directory sharing**: one agent of each kind per directory, because most kinds
resume by "the last conversation in this directory" and two of the same kind
would come back as each other. Kinds that name or pin their conversations prove
which one is theirs and may share. Sharing a directory still means sharing its
files; kolo does not referee that.

## Input model

Members type at the agent themselves. There is nothing to take and nobody to
ask: keystrokes relay as pressed, whoever's keys arrived last is announced to
watchers as the typist, and everything typed lands in the log attributed. Two
people typing at once interleave keystrokes in view of everybody — the same as
two people reaching for one keyboard in a room. Nothing is gated, because the
member can see the screen their keys land on: an Enter at a question is a
decision, not an accident.

What kolo presses on somebody's behalf, and when:

| action | permitted when |
|---|---|
| interrupt | only while the kind's own busy marker is up, using that kind's key |
| restart / start fresh / stop | always |

Answering a question is not on that list. Kolo says a question is up; the
question itself is on the screen, and answering it means typing. An earlier
version read choices off dialogs and offered buttons — it held for exactly one
agent's dialog and was a guess about everybody else's. When an agent hands its
questions over through a real interface, that interface is what kolo will use.

## Restart and resume

Agents are supervised: an exit is restarted, one that will not stay up is marked
failed rather than restarted forever, and a human restart does not count toward
giving up. What is running is written to `-state`, so restarting the host — or
the whole machine — brings the org's agents back in the order they were made.

Resume comes in two shapes: ask for the last conversation (`--continue`), or
name one (`--resume {session}`, id read off the screen or pinned at birth). The
id travels in the state file, so it survives the machine. A failed resume starts
clean and says so — silent context loss is worse than visible loss. Start fresh
drops the conversation deliberately, and is logged like every other action.

## The log

Every member action is recorded on the hub beside the org file (JSON lines,
`GET /v1/log`): created, said, interrupted, restarted, stopped, failed. Typed
lines are reconstructed from keystrokes and written only on Enter, so an
abandoned half-line is never recorded. It is a reconstruction, not a transcript:
pastes and arrow-key menu choices will not read back exactly. Nothing an agent
prints is kept.

The log is what stands in for roles: everyone may do everything because everyone
can see who did.

## Screen size

Each watching browser proposes the grid it can draw; the smallest wins, as in
tmux, because anything wider than the smallest window is drawn by an agent that
cannot see where it is being cut off.

## Status and layout

Working and tested across two machines: watching, typing, interruption,
restart/resume (including pinned identities), discovery of installed agents
(`-allow '*'`), the journal. Not built yet: notification when an agent stalls on
a dialog while nobody watches; putting the journal in front of people in the UI.

```
cmd/kolo          the binary: up, serve, host, invite, token, who, doctor
cmd/kolorec       records an agent session as a test fixture
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
docs/brand        geometry and tokens the icon build (make brand) generates from
```

Brand assets are generated, never stored: `make brand` renders icons from
`docs/brand/ring.ts` and `docs/brand/kolo-tokens.css` (needs Node and a Chrome).
