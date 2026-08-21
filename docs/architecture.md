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

## The log

Every action a member takes is written down: who created an agent, what they
sent it, who interrupted, restarted or stopped it, and when the host it was
running on went away. It lives on the hub, beside the org file, as a line of JSON
per entry — the hub is the only party that knows who anyone is, and a record kept
on the host would leave with the machine that lent itself.

It is what stands in for roles. Everyone may do everything, which is workable
only because everyone can also see who did.

`GET /v1/log` reads it back, for one agent or for the org.

**What a member types is part of the record.** The hub sees keystrokes rather
than messages, so a line is put back together as it is typed and written down
when it is sent — never before, so a line abandoned half typed is never
recorded. It is a reconstruction: a paste, a completion the agent filled in, or a
choice made with the arrow keys will not read back exactly. Nothing an agent
prints is kept, only what people asked it for.

Anything anyone sends an agent is therefore readable by every member of the org,
which follows from agents being communal, and is worth knowing before typing a
password into one.

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

What kolo does on a member's behalf, each permitted only where it is safe:

| action | permitted when |
|---|---|
| interrupt | the agent is working, and with that kind's own key |
| restart, start fresh, stop | always |

Interrupting exists so that stopping a runaway agent does not require taking the
keyboard first. It is the only key kolo presses that nobody pressed.

**Answering a question is not on that list.** Kolo used to read the choices off
the dialog and offer them as buttons, checking the label against the screen
before pressing the number. It worked, and it was still the wrong shape: which
choices a question has, what they mean, and which key selects one belong to the
agent, and a program deducing them from a picture of a terminal is guessing about
somebody else's interface — the same mistake as the message queue, in a smaller
frame. It held for exactly one agent's dialog, and only until that agent redrew
it.

So kolo says a question is up and stops there. Reading the question is what the
screen is for; answering it is what taking the keyboard is for. When an agent CLI
offers its questions through an interface of its own, that interface is what kolo
will use — a question the agent hands over is a question kolo can carry without
inventing anything.

## What kolo knows about each agent kind

Three things:

- how its screen looks when idle, working, and asking a question
- how to resume its last conversation
- which key stops it working

Not what it is asking. That is the agent's, and whoever holds its keyboard reads
it off the screen like a person sitting in front of it.

They live together in `internal/adapter`, one value per kind, looked up by the
name of the binary at the front of the command line — the arguments a host lends
it with change neither.

The interrupt key is the one of the three that is not read off the screen: it is
a key kolo presses rather than something the agent said. It is only ever pressed
while that kind's own busy marker is up, so a key that means stop while an agent
is working is never sent while it means something else. It was Esc for every kind
there was until kinds kolo does not ship could be described, and an agent that
stops on Ctrl-C was being sent the key that clears its input.

Claude Code is the one kolo ships. A host adds others in `~/.kolo/kinds.json`,
which replaces a shipped kind of the same name, so an org runs an agent kolo has
never heard of — or fixes one whose footer moved between releases — without
waiting for a release of kolo. See [hub.md](hub.md#which-agents).

The markers are the host's, and they travel to the hub on the agent's screen
socket rather than being looked up there. The hub knows no agent kinds at all: it
reads the screen it is handed with what it was handed to read it with. A second
table on the hub would be one more thing to agree with the host's, and learning a
new agent would mean upgrading the machine that never sees one.

An agent kind kolo has neither for gets the empty adapter: no marker matches, so
nothing is claimed about its screen, and it cannot be resumed, so it restarts
fresh. It is still watchable, and still typeable
by whoever holds its keyboard — which is the difference this change made. An
unknown kind used to be watchable and nothing else.

That is the whole of what "supporting an agent" means here. Anything that draws a
terminal runs and is shared; a kind with markers is also legible from the list
and survives a restart.

## Restart

Agents are supervised, and resume on restart. When resume fails — killed
mid-tool-call, CLI upgraded, state gone — the agent starts clean and the log says
so. Silent context loss is worse than visible context loss.

How to resume is the kind's, and comes in two shapes: an agent that asks for its
last conversation (`--continue`), and one that names a particular conversation
(`--resume <id>`). The id for the second is read off the agent's own screen, kept
by the host, and put back on the command line at the next launch — the same
principle as everything else here, which is that what is known about an agent is
what the agent said, next to the screen it said it on. An agent that has never
named a conversation is not resumed; a fresh start that says so beats a command
line with a hole in it.

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
carries keystrokes and interrupts; it never reads a screen to judge whether they
may be sent.

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
