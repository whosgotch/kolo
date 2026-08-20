# Kolo

Multiplayer for AI. Your team lends one machine, and everybody gets shared agents
running on it — created from a browser, used by anyone, no install.

```
$ cd ~/work/api && kolo up                      # on the machine you lend
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

Reaching it from anywhere is `-tls-domain`: the hub gets and renews its own
certificate, so there is no proxy to run and no token crossing the network in
the clear.

Execution stays on a machine you own, with your files and your permissions.
That is the part that is not negotiable, and it is the difference between this
and a hosted agent that can only see what you upload.

See [docs/architecture.md](docs/architecture.md) for the shape,
[docs/hub.md](docs/hub.md) for how to run one, and
[docs/roadmap.md](docs/roadmap.md) for the order it gets built in.

## What works today

The loop. A host lends a machine, anyone in the org makes an agent on it from a
browser, watches it work, types at it, answers the questions it asks,
stops it when it is going wrong, and restarts it — with its conversation, or
without — and the agents survive being killed and outlive the host restarting.

Nothing is released yet, so running it is building it. `make install` puts
`kolo` on your PATH from source and says what to do if the directory it went
into is not on it:

```
$ make install
$ cd ~/work/api && kolo up
```

`make run` starts it without installing anything, passing flags through as
`ARGS`. `make test` is the suite.

That is the whole of it. `kolo up` runs the hub and lends this machine to it,
and makes what it needs on the way, all of it under `~/.kolo`: the org file,
this machine's credential, and an invite link to send the team — good for ten
people over a week, withdrawable with one command. It lends the directory it
was started in and runs whichever agents it finds installed, both of which
`-dir` and `-allow` override — with flags and all, since `-allow "claude --model
opus"` is the agent somebody actually wanted.

Any command that draws a terminal runs, and the org can watch it, type at it and
stop it without kolo knowing what it is. Knowing the kind buys two more things,
both read off the agent's own screen: the list saying which agents are asking
something, and a conversation that survives a restart — including for an agent
that resumes by naming a conversation rather than asking for the last one, whose
id kolo reads off its screen and keeps. Kolo ships with Claude
Code; a host describes anything else in `~/.kolo/kinds.json`, and one it has no
entry for is honestly labelled unknown rather than guessed at. See
[docs/hub.md](docs/hub.md#which-agents).

`kolo doctor` says what this machine can and cannot do with what it lends — which
commands are actually there, what kolo knows about each of them, and whether it
has been able to read what any of them are doing. That last one is what catches
an agent whose CLI upgraded and moved the strings kolo reads its screen by, which
otherwise shows up as a stop button that quietly does nothing.

`kolo help` is the rest of it: `invite` and `who` for letting people in and
seeing who is in, and `serve`, `token` and `host` for an org whose hub lives
somewhere other than the machine running the agents. See
[docs/hub.md](docs/hub.md).

Everyone else opens that link, says what to call them, and picks an agent.
The org file is read again whenever it changes, so joining, adding somebody by
hand and revoking them all take effect without a restart.

Anyone can take the agent's keyboard and type at it directly — the whole of its
interface, panels and modes and all — while everybody else watches and sees whose
hands are on it. Taking it needs no permission and cannot be refused.

An earlier version had members send messages into a queue kolo released when it
judged the agent idle. It is gone: it was a chat pretending a terminal was an
API, and the judgement was a guess about somebody else's screen that broke every
time they shipped. A moment earlier and the message is either swallowed
without trace or its Enter answers a question the agent was asking. See
[docs/probe-findings.md](docs/probe-findings.md); every part of it was found by
experiment, not guessed at.

Answering a question went the same way, and for the same reason. Kolo used to
read the choices off the screen and offer them as buttons; it does not any more.
Which choices a question has, what they mean, and which key selects one are the
agent's own business, and kolo reverse-engineering them from a picture of a
terminal is the same mistake in a smaller frame — right for the one agent it was
written against, wrong the moment that agent redraws its dialog or another agent
draws a different one.

What kolo says is that the agent is waiting on a person. The question itself is
on the screen everybody can already see, and whoever is free takes the keyboard
and presses what it says. When an agent CLI offers a real interface for its
questions, that is what kolo will use.

A restart resumes the conversation, and one whose resume is refused — the CLI
upgraded, the state gone — comes back clean and says so on the page. Silent
context loss is worse than visible context loss.

What is missing is a second agent kind, which is what would prove the seam is
real rather than a shape Claude Code happens to fit, and a log that outlives the
hub.

An earlier single-machine version — one agent, its host at the keyboard, a
secret in a URL — has been removed rather than kept alongside. The screen model
and the queue carry over; the shape around them did not.

> **Security:** an agent runs commands as the user who started it, so lending a
> machine lends that user's whole account — not just the directories named. Run
> a host as a user that owns nothing you would not hand to everyone in the org.
