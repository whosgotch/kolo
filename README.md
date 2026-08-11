# Kolo

A workspace where everyone has their own AI coding agent, and the org's knowledge
follows them into it.

**Execution is local. Context is shared.** Your agent runs on your machine, with
your files and your permissions — nobody else's agent can reach it. What the org
holds is what makes every member's agent behave like it works at the same
company: shared project context, promoted skills, and the conversation around the
work.

```
$ kolo serve                     # the hub, one org
$ kolo run claude                # your agent, on your machine, joined to it
$ kolo who
  artem   claude   online
  dana    claude   online
```

The agent's machine dials out to the hub and is never listened to: no inbound
port, no firewall rule, nothing to tunnel. See [docs/architecture.md](docs/architecture.md).

## Where it is going

Presence first, then shared **projects**, then promoted **skills**, then
**channels** where people and agents talk together. The ordering is deliberate
and the reasoning is in [docs/roadmap.md](docs/roadmap.md) — briefly, projects
come before skills because they are the same distribution problem carrying cargo
that cannot execute.

## What works today

The single-machine half: running an agent and letting other people watch it live
and send it messages, over localhost.

```
$ go build -o kolo ./cmd/kolo
$ ./kolo claude
session live: http://127.0.0.1:54321/
```

The agent runs as it always does — the terminal is still yours, keystrokes and
all. Open that URL and you see the same screen, live; opening it mid-session
repaints whatever is already there before the live stream starts. Several people
can watch at once.

Guests type into a box under the terminal, never into the terminal itself, and a
message is held until the agent is idle at its input box. It is not delivered
while the agent has a question on screen, because submitting requires Enter and
Enter would answer the question instead. A held message says so rather than
disappearing.

This is the mechanism a **shared agent** — one belonging to a channel or a
project rather than a person — will use once it is reachable through the hub.

Run `go test ./...` for the test suite. Some tests replay recorded agent
sessions; `KOLO_RAW=<capture> go test ./internal/term` points them at your own.

> **Security:** an agent edits files and runs commands on the machine it runs on,
> with that user's permissions. Today's link is localhost-only and has no
> authentication of any kind — do not expose it. Identity arrives with the hub.
