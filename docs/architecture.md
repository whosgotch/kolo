# Architecture

## The shape

A host lends a machine to the org. Agents run on it. Everyone else uses a
browser.

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

The host dials out and is never listened to: no inbound port, no firewall rule,
nothing to tunnel. It is the same shape whether the hub is a VPS today or hosted
later.

Only hosts install kolo. Members open a link.

## An agent

An agent is a name, a directory on the host, and a command. Anyone in the org
creates one from the browser and the host spawns it.

**One agent per directory.** Resuming a conversation is directory-scoped, and two
agents editing the same files would collide, so creating a second agent in a
directory that already has one is refused. Working on the same repo in parallel
means a second checkout.

Agents are communal. Anyone may watch, send to, interrupt, restart or stop any of
them. There are no roles; every action is attributed instead, which is what makes
their absence workable.

## The host

```
kolo host --dir ~/work/api --dir ~/work/web --allow claude
```

The host contributes a machine and does not participate. Nothing about the
product may require the host to be at their keyboard.

The flags bound what the org can **start**: those directories, those commands,
nothing else. They do not bound what a running agent can **reach**. It runs as
the host's user and can read `~/.ssh`, `~/.aws`, and every other repo on the
disk, because any member can ask it to.

Containment is therefore the host's own arrangement, not something kolo
provides — a dedicated user account that owns only what it should, a container,
or a machine that holds nothing else.

## Identity

Members authenticate with a token and the hub decides who they are — never the
client, which is why attribution on a message cannot be forged. The hub's config
holds the org, its members, and a hash of each token; revoking a member is
removing their line.

Tokens carry a `kolo_` prefix so one is recognisable on sight and by secret
scanners, and travel in the `Authorization` header rather than a URL.

## Input

A message is never typed straight into the agent. It joins a queue and is
released only while the screen says the agent is idle at its input box. Released
at the wrong moment it is swallowed without trace, or its Enter answers a
question the agent was asking. See `docs/probe-findings.md` #3–#5 — this is the
sharpest thing kolo knows.

Three things are not messages. They go directly, each permitted only where it is
safe:

| action | permitted when |
|---|---|
| answer a dialog | a dialog is on screen |
| close a panel | a dialog is on screen with nothing to answer |
| interrupt | always |
| restart, start fresh, stop | always |

Closing a panel is the exception that had to be made. A slash command can open a
view (`/status`, `/config`) that carries a dialog's footer and no choices: the
queue is held, there is nothing to offer as buttons, and an agent the whole org
is using is stuck until somebody restarts it. So Esc — the key that screen's own
footer offers — may be sent there, and only there: where there are choices, Esc
is not "close this" but an answer nobody chose.

Answering a dialog is the inverse of the danger above: the keystroke that must
never arrive by accident is fine when it is deliberate, attributed, and gated on
the same detection.

A message beginning with `/`, `!` or `#` is the agent's CLI being addressed
rather than its model, and it goes through the queue and the gate like any other
line — but unattributed, because a name in front of it puts the sigil out of the
first column and the CLI reads the whole thing as prose. So attribution moves to
the event every watcher is sent: the page says who ran what. This is the only
input kolo does not sign, and it is the reason it is worth naming here.

## What kolo knows about each agent kind

Three things:

- how its screen looks when idle, and when it is asking a question
- how to resume its last conversation
- which lines are for its own CLI rather than for the model behind it

They live together in `internal/adapter`, one value per kind, looked up by the
name of the binary. The pieces that use them — the detector, the screen, the
queue — are given a kind rather than knowing one.

Claude Code is the first. An agent kind kolo has none of these for gets the empty
adapter, and the empty adapter is harmless by construction: no marker matches, so
its screen never reads as idle and nothing is ever sent to it; it cannot be
resumed, so it restarts fresh; and no line of its is taken for a command. Such an
agent is watchable, not drivable.

## Restart

Agents are supervised, and resume on restart. When resume fails — killed
mid-tool-call, CLI upgraded, state gone — the agent starts clean and the log says
so. Silent context loss is worse than visible context loss.

A restart replaces the screen, so the buffer is cleared and re-seeded from the
new process rather than repainting newcomers with a picture of a process that no
longer exists.

Context accumulates across everyone who has used the agent, and nobody owns the
decision to clear it. Hence **start fresh** as an ordinary, logged action.

## What runs where

| | host | hub |
|---|---|---|
| agent processes, their files and permissions | ✓ | |
| terminal emulation, the screen, the input gate | ✓ | |
| members, tokens, attribution | | ✓ |
| the agent list and the browser UI | | ✓ |
| the log | | ✓ |

The gate that decides when a line may be typed stays next to the screen it reads.
No part of that boundary moves onto the network.

## Security

- `--dir` and `--allow` decide what may be started, not what may be reached. The
  agent has the host user's whole account; run the host as a user that owns
  nothing you would not hand over.
- The hub is a remote code execution endpoint. Its authentication is what stands
  between the internet and the host's machine.
- The hub carries no TLS. Reaching it across the internet means putting it behind
  something that does.
- Every member has the same power over every agent, including stopping one
  mid-task and approving a permission dialog for everybody. Roles wait until
  there is evidence of the shape they should take; the log stands in until then.
