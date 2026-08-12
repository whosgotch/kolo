# Kolo

Multiplayer for AI. Your team lends one machine, and everybody gets shared agents
running on it — created from a browser, used by anyone, no install.

```
$ kolo host --dir ~/work/api --allow claude     # on the machine you lend
```

Everyone else opens a link. The agent list is the front door: make one, join one
somebody is already using, send it work, answer its questions, take it off a
wrong path.

## The idea

An agent belongs to the org, not to a person. When something needs doing for the
whole company — the checkups nobody remembers to run, the release chore, the
sweep across every repo — you open the agent that does it and tell it to go,
alongside everyone else who might.

- **Agents are long-lived and communal.** They keep their conversation across
  restarts. Anyone may watch, send, interrupt or stop any of them, and every
  action carries the name of whoever took it.
- **The host is infrastructure.** They lend a machine and stay out of it. Nothing
  the org does with an agent requires them to be at their keyboard.
- **Members install nothing.** A browser and a token. The host dials out to the
  hub, so there is no port to open and nothing to tunnel.

Execution stays on a machine you own, with your files and your permissions.
That is the part that is not negotiable, and it is the difference between this
and a hosted agent that can only see what you upload.

See [docs/architecture.md](docs/architecture.md) for the shape,
[docs/hub.md](docs/hub.md) for how to run one, and
[docs/roadmap.md](docs/roadmap.md) for the order it gets built in.

## What works today

Everything but the browser. A host lends a machine, the org creates agents on it
from another machine, they survive being killed and outlive the host restarting,
and their screens reach anyone watching.

```
$ go build -o kolo ./cmd/kolo
$ ./kolo serve -org org.json                        # the hub
$ ./kolo host -dir ~/work/api -allow claude         # the machine you lend
$ curl -H "Authorization: Bearer $KOLO_TOKEN" $HUB/v1/agents
```

Creating, listing and stopping agents is HTTP; watching one is a websocket.
There is no page to open yet, which is the next thing.

The part worth reading is the queue that holds a message until the agent is idle
at its input box. Sent a moment earlier, a message is either swallowed without
trace or its Enter answers a question the agent was asking. See
[docs/probe-findings.md](docs/probe-findings.md); it was found by experiment,
not guessed at. Nothing is sent to an agent yet — that comes with the browser.

An earlier single-machine version — one agent, its host at the keyboard, a
secret in a URL — has been removed rather than kept alongside. The screen model
and the queue carry over; the shape around them did not.

Run `go test ./...` for the test suite.

> **Security:** an agent runs commands as the user who started it, so lending a
> machine lends that user's whole account — not just the directories named. Run
> a host as a user that owns nothing you would not hand to everyone in the org.
