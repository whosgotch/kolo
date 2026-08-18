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

The loop. A host lends a machine, anyone in the org makes an agent on it from a
browser, watches it work, sends it messages, answers the questions it asks,
stops it when it is going wrong, and restarts it — with its conversation, or
without — and the agents survive being killed and outlive the host restarting.

```
$ go build -o kolo ./cmd/kolo
$ echo '{"org": "acme"}' > org.json
$ ./kolo token -host -id devbox                     # prints the line to run below
$ ./kolo token -id dana -name "Dana"                # prints what to send Dana
$ ./kolo serve -org org.json                        # the hub

$ ./kolo host -join kolo_join_… -dir ~/work/api -allow claude   # the machine you lend
```

Minting writes to the org file rather than printing something to paste into it,
and a machine's hub and token travel as one join string, because they were minted
together and are useless apart.

Everyone else opens the hub, pastes their token once, and picks an agent.

Anyone can take the agent's keyboard and type at it directly — the whole of its
interface, panels and modes and all — while everybody else watches and sees whose
hands are on it. Taking it needs no permission and cannot be refused.

The part worth reading is the queue that holds a message until the agent's own
screen says it may be sent, for the lines nobody typed live. A moment earlier and the message is either swallowed
without trace or its Enter answers a question the agent was asking. See
[docs/probe-findings.md](docs/probe-findings.md); every part of it was found by
experiment, not guessed at.

Answering works the same way round. The choices are read off the agent's screen
and offered as buttons, and the answer carries the label the member was shown —
so an answer either lands on the question they were looking at or is refused. No
keystroke a member makes ever reaches the terminal.

A restart resumes the conversation, and one whose resume is refused — the CLI
upgraded, the state gone — comes back clean and says so on the page. Silent
context loss is worse than visible context loss.

What is missing is a second agent kind, which is what would prove the seam is
real rather than a shape Claude Code happens to fit, and a log that outlives the
hub.

An earlier single-machine version — one agent, its host at the keyboard, a
secret in a URL — has been removed rather than kept alongside. The screen model
and the queue carry over; the shape around them did not.

Run `go test ./...` for the test suite.

> **Security:** an agent runs commands as the user who started it, so lending a
> machine lends that user's whole account — not just the directories named. Run
> a host as a user that owns nothing you would not hand to everyone in the org.
