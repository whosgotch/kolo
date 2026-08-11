# Architecture

## The shape

**Execution is local. Context is shared.**

Every member runs their own agent on their own machine, with their own files and
their own permissions. Nobody's agent can reach anyone else's machine. Adding a
member adds their machine; nothing gets more loaded.

What the org holds is everything that makes those agents behave like they work at
the same company: shared project context, promoted skills, and the conversation
around the work.

```
  Artem's machine            Kolo hub               Dana's machine
  ┌──────────────┐                                 ┌──────────────┐
  │ agent (PTY)  │                                 │ agent (PTY)  │
  │      │       │                                 │      │       │
  │  kolo client ├──── outbound websocket ────────►│  kolo client │
  └──────────────┘         ▲         ▲             └──────────────┘
                           │         │
                    presence, later: projects,
                    skills, channels
```

The agent's machine **dials out**. It is never listened to. That means no inbound
port, no firewall rule, and no NAT traversal problem — and it is the same shape
whether the hub is a VPS today or Kolo Cloud later. It also removes the need for
a tunnel: there is nothing to tunnel to.

## Identity

A member is a person in an org. They authenticate with a token, not a nickname.

- The hub's config lists the org, its members, and a **hash** of each member's
  token. A leaked config file does not hand over anyone's token.
- Tokens are long random strings carrying a `kolo_` prefix, so one can be
  recognised on sight and by secret scanners.
- The client sends its token in the `Authorization` header of the connection
  upgrade. It never appears in a URL, where it would be logged by every proxy in
  between.
- Revoking a member is removing their line from the config.

One member may be connected from several machines at once, so presence is keyed
by member *and* connection, not by member alone.

## Protocol

One websocket per connected agent, carrying JSON text frames with a `type`
field. It starts deliberately small — presence and nothing else — because every
later feature is a new payload on a pipe whose shape is already settled.

| direction | type | meaning |
|---|---|---|
| client → hub | `hello` | agent kind, machine name, kolo version |
| hub → client | `welcome` | who the hub thinks you are, and which org |
| hub → client | `error` | why the connection is being closed |

Liveness is the websocket's own ping/pong rather than an application-level
heartbeat, so a dead connection is noticed by the transport that owns it.

`kolo who` is a plain authenticated `GET /v1/presence`, not a websocket. Asking a
question and getting an answer does not need a persistent connection.

## Reconnection

Laptops sleep and wifi drops. A client reconnects on its own with exponential
backoff and jitter, and an agent that disappears for thirty seconds must not need
restarting to come back.

Presence is derived from live connections: a drop removes the member, a
reconnection re-adds them. There is no separate liveness bookkeeping to get out
of step with reality.

## What runs where

| | host's machine | hub |
|---|---|---|
| the agent process | ✓ | |
| the agent's files and permissions | ✓ | |
| the virtual terminal and its snapshot | ✓ | |
| the injection gate for shared agents | ✓ | |
| identity and org membership | | ✓ |
| presence | | ✓ |
| projects, skills, channels (later) | | ✓ |

The gate that decides when a message may be typed into a shared agent stays on
the machine that agent runs on, next to the screen it reads. No part of that
trust boundary moves onto the network.

## Security posture

The dangerous thing in this design is not a message: it is a **promoted skill**,
which is code that runs on every member's machine with their permissions.
Promotion is a supply-chain event and is designed as one when it arrives — who
may promote, what review it gets, how it is versioned, and how it is rolled back.

That is why the distribution pipe is built first carrying **projects**, which are
data, and only then upgraded to carry skills, which are not.
