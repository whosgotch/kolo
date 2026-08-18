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

Members type at the agent themselves. One holds its keyboard at a time; anybody
may take it from anybody, and everybody watching is told who has it — the same
thing that stops two people typing at one keyboard in a room. Nothing is gated,
because the member can see the screen their keys land on: an Enter at a question
is a decision rather than an accident.

That is what makes the agent's own interface reachable — a panel with keys of its
own (`d to day · w to week`), a filter box, a mode the footer offers. None of it
has a name kolo could learn, and all of it used to strand an agent.

Kolo held a queue of members' messages once, and released them only while the
screen said the agent was idle, because nobody could type for themselves. That is
gone. What is left of it is in `docs/probe-findings.md` #3–#5, which is worth
reading anyway: it is the evidence that typing at somebody else's TUI from a
program is harder than it looks.

Two things kolo still does on a member's behalf, each permitted only where it is
safe:

| action | permitted when |
|---|---|
| answer a question | a dialog with choices is on screen |
| interrupt | the agent is working |
| restart, start fresh, stop | always |

Answering exists for the board, where a member is not watching the screen at all:
the choices are read off it, and an answer carries the label the member was shown
so it either lands on the question they were offered or is refused. Interrupting
exists so that stopping a runaway agent does not require taking the keyboard
first.

## What kolo knows about each agent kind

Two things:

- how its screen looks when idle, working, and asking a question
- how to resume its last conversation

They live together in `internal/adapter`, one value per kind, looked up by the
name of the binary. It was three until members could type for themselves: which
lines address the agent's own CLI mattered while kolo was typing them.

Claude Code is the first. An agent kind kolo has neither for gets the empty
adapter: no marker matches, so nothing is claimed about its screen and no
question is answered on it, and it cannot be resumed, so it restarts fresh. It is
still watchable, and still typeable by whoever holds its keyboard — which is the
difference this change made. An unknown kind used to be watchable and nothing
else.

## Restart

Agents are supervised, and resume on restart. When resume fails — killed
mid-tool-call, CLI upgraded, state gone — the agent starts clean and the log says
so. Silent context loss is worse than visible context loss.

A restart replaces the screen, so the buffer is cleared and re-seeded from the
new process rather than repainting newcomers with a picture of a process that no
longer exists.

Context accumulates across everyone who has used the agent, and nobody owns the
decision to clear it. Hence **start fresh** as an ordinary, logged action.

## The size of the screen

One grid is shown in several windows at once. Each browser says what it can draw
and the smallest wins, as it does in tmux and for the same reason: anything wider
than the smallest window is drawn by an agent that cannot see where it is being
cut off. Each window then draws that grid as large as it will go.

## What runs where

| | host | hub |
|---|---|---|
| agent processes, their files and permissions | ✓ | |
| terminal emulation, the screen, reading what it says | ✓ | |
| members, tokens, attribution | | ✓ |
| the agent list and the browser UI | | ✓ |
| the log | | ✓ |

Whatever is decided from the screen is decided next to the screen. The hub
carries keystrokes and answers; it never reads a screen to judge whether they may
be sent.

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
