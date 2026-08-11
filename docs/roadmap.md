# Roadmap

The order matters more than the list. Each step exists to make the next one
cheap, and two of them are deliberately later than they look.

## Now — presence

Two people, two machines, one org, each able to see the other's agent is there.

- `kolo serve` — the hub. One org, members and hashed tokens in a config file.
- `kolo run <agent>` — dials the hub, authenticates, registers, stays connected.
- `kolo who` — lists who is connected.

Nothing is sent anywhere. No channels, no projects, no skills, no web UI.

This is the floor for calling anything multiplayer: it cannot be faked on one
machine. Everything smaller is infrastructure you cannot tell is working, and the
moment two machines see each other, the outbound connection, identity, org
membership, presence and shared state are all proven at once.

The token model and reconnection get done properly even here. Both are cheap now
and awkward to retrofit, and everything later inherits them.

## Next — projects

Shared context bound to a body of work: the repo, the conventions, the decisions,
how things are done here. Distributed to every member's agent.

The payoff is immediate and easy to feel with three people: a new joiner's agent
knows the conventions on day one, instead of each person explaining the same
things privately and each agent learning them separately.

## Then — skills

Someone writes a skill that works. It is reviewed, versioned, promoted, and every
member's agent has it. The org gets better at its own work instead of each person
rediscovering the same tricks.

**Projects come before skills on purpose.** They are the same distribution
problem — the org has a thing, every member's agent should have it — but a
project is data and a skill is code that executes on every member's machine with
their permissions. Get the pipe right while it is carrying cargo that cannot hurt
anyone, then upgrade the cargo and spend the design effort on promotion: who may
promote, what review it gets, versioning, rollback.

## Last of the four — channels

Shared places where people and agents talk, with agents as members rather than
tools.

They are last because they are the most familiar part of the idea and the least
differentiating, and because they are much better once agents already share
project context — there is something worth talking about that both sides
understand. Built first, this is a chat app.

## Carried over

Work already finished that the above depends on, or that returns later:

- Running an agent under a PTY, modelling its screen, and repainting that screen
  for someone who joins mid-session.
- The injection gate for **shared agents** — an agent belonging to a channel or a
  project rather than a person, which several people send to. See
  `docs/probe-findings.md` #4 and #5 for why sending at the wrong moment is
  dangerous rather than untidy.
- One gap remains in that gate: the detector cannot yet recognise an agent that
  is busy running a shell command, where an injected line is silently swallowed.
  Closing it means recording that state locally and reading what distinguishes
  it; the recording itself stays out of the repository, since one is a picture
  of somebody's session.

## Not doing

- A tunnel. The outbound connection removes the need for one.
- Peer-to-peer. It keeps the no-infrastructure property but makes presence,
  membership and history hard, and cannot become a hosted product without a
  rewrite.
