# Setting up

Two things run: a **hub**, which the org connects to, and a **host**, which lends
a machine for agents to run on. They can be the same machine or different ones.

## One machine

When they are the same machine, one command is the whole of it:

```
$ cd ~/work/api
$ kolo up
Created /Users/you/.kolo/org.json for api.

api is up at http://192.168.1.24:7300
Lending /Users/you/work/api, running claude.

Open it and sign in with this. It is stored nowhere, so keep it:

    kolo_yPNNK8ZnHdvgKFDiQV2Oc…
```

`kolo up` makes whatever is missing: the org file, named after the directory
unless `-name` says otherwise; a credential for this machine, minted fresh on
every start and never written down; and, the first time, a member token for
whoever ran it. It lends the directory it was started in and allows whichever
agents kolo knows about and finds on `PATH`. `-dir` and `-allow` say otherwise,
and repeat.

Everything a machine remembers about kolo lives in `~/.kolo` — the org, the
agents running here, the certificates it has been issued — so it is the same
path on every machine, `kolo up` finds it from wherever you started, and
deleting that directory is starting over. `$KOLO_HOME` moves it, and `-org`,
`-state` and `-tls-cache` move one file each.

It listens on every interface, so the org can reach it — over plain HTTP unless
`-tls-domain` says otherwise. See [Being reachable from
anywhere](#being-reachable-from-anywhere). `-addr 127.0.0.1:7300` keeps it to
this machine.

The link is the whole of joining, for the first ten people who open it within a
week. Whoever opens it says what to call them and is in — no token to paste, nothing to install, and no restart, because an invite
is read when it is spent rather than at startup. Later ones come from
`kolo invite`:

```
$ kolo invite -hub http://192.168.1.24:7300
Send this to everyone who should have an agent. It works for 7 days:

    http://192.168.1.24:7300/join#kolo_4aGx1SI4Sgb…
```

An invite can only be spent on becoming a member, and it is bounded twice: by
**how many people** may spend it — ten unless `-uses` says otherwise, or `0` for
anyone holding it — and by **how long** it lasts, seven days unless `-days` says
otherwise.

Neither bound is security on its own. Whoever holds the link can spend it, and
they choose the name they appear under. The bounds are what keeps a leak small
enough to notice and undo:

```
$ kolo who
acme

members       2
  dana        Dana        joined 19 Aug 14:23 via team
  artem       Artem       added by hand

machines      1
  devbox

links that still work   1
  team        8 uses left   until Wed 26 Aug 14:23
```

```
$ kolo invite -off team
```

Withdrawing takes effect within a couple of seconds and does not remove whoever
already came through — they are members now, and `via` in `kolo who` is how you
find them. Removing one is deleting their line.

A member's id comes from the name they gave — Dana Scully becomes `dana-scully`
— and a name already spoken for gets a number after it. Two people called Dana
is a thing that happens.

The rest of this page is the other deployment: a hub somewhere the org can
already reach, and one or more machines lending themselves to it from elsewhere.

## The hub

An org file starts as its name. It is the only thing kolo cannot pick for you:

```
$ mkdir -p ~/.kolo && echo '{"org": "acme"}' > ~/.kolo/org.json
```

Then mint credentials — one per member, and one for each machine that will run
agents. Each is written into the org file and printed once, here, and nowhere
else; losing one means issuing another.

```
$ kolo token -id dana -name "Dana"
Added Dana to /Users/you/.kolo/org.json.

Send Dana these two, once. The token is stored nowhere:

    http://127.0.0.1:7300
    kolo_bAXFSPCQ01No…
```

```
$ kolo token -host -id devbox -hub https://hub.acme.com
Added devbox to /Users/you/.kolo/org.json.

Run this on devbox. It carries both the hub and the token, and is stored nowhere:

    kolo host -join kolo_join_eyJodWIiOiJod… \
        -dir <a directory to lend> -allow claude
```

`-hub` is where they will reach the hub, and it defaults to the address `kolo
serve` listens on, so everything on one machine needs no flag at all.

The file that results is small enough to read at a glance, and safe to keep in
version control if you want the org reviewable — it holds hashes, never tokens:

```json
{
  "org": "acme",
  "members": [
    {"id": "dana", "name": "Dana", "token_hash": "1d7fe6…"}
  ],
  "hosts": [
    {"id": "devbox", "token_hash": "f9e1ac…"}
  ]
}
```

A machine authenticates as itself, not as whoever set it up, so the log can say
which machine ran something without pinning it on a person who was not involved.
Ids are unique across both lists, and `kolo token` refuses one that is taken
rather than writing an org the hub would then refuse to start.

**A running hub reads this file again when it changes**, within a couple of
seconds, so minting somebody with `kolo token` lets them in without a restart and
removing their line shuts them out. An edit that will not parse is complained
about and ignored — a typo made while adding one person is not a reason for
nobody to be able to connect.

Start it. It listens on localhost unless told otherwise:

```
$ kolo serve -addr 0.0.0.0:7300
kolo: hub for acme on 0.0.0.0:7300, 2 member(s)
```

## Being reachable from anywhere

A member's token is sent in a header. Over plain HTTP that header crosses the
network in the clear, and anyone in between can take it and use it — which means
driving an agent, which means running commands as the host user. On a network
you trust that is a considered choice. Anywhere else it is not.

Point a domain at the machine and let kolo get its own certificate:

```
$ kolo up -tls-domain hub.acme.com
$ kolo serve -tls-domain hub.acme.com                  # the hub on its own
```

Kolo asks Let's Encrypt for a certificate the first time somebody connects,
caches it, and renews it from then on. There is no proxy to run, no certificate
file to place and no renewal to remember.

It needs two things of the network:

- **The domain resolves to this machine.** A certificate is issued for a name.
- **Ports 80 and 443 are reachable from the internet.** Let's Encrypt connects
  back on 80 to check the machine really answers for the name; 443 is where the
  org arrives. Anything else arriving on 80 is redirected to https, so somebody
  typing the bare name still ends up somewhere encrypted.

Certificates are cached in `~/.kolo/certs`, `-tls-cache` to move them.
Keep that directory: losing it means asking for new certificates, and Let's
Encrypt counts how often that happens. `-tls-staging` asks a test service whose
certificates browsers do not trust and whose limits are generous — worth using
while getting DNS and ports right.

A machine behind NAT with no public name cannot do this, and no flag will change
that. The options there are a port forward, or a hub on a machine that does have
a name.

## The host

One machine in the org runs agents for everybody. It should be one that stays
on — a dev box or a spare desktop, not a laptop that closes at six.

Run what `kolo token -host` printed, saying which directories this machine lends:

```
$ kolo host -join kolo_join_eyJodWIiOiJod… -dir ~/work/api -dir ~/work/web -allow claude
kolo: lending /Users/you/work/api /Users/you/work/web, running claude
kolo: joined acme as devbox
```

The join string carries the hub and this machine's token together, because they
were minted together and are useless apart. A host that keeps its two halves
somewhere else — a secret store, a unit file — can pass `-hub` and `-token`
instead, or `$KOLO_HUB` and `$KOLO_TOKEN`.

Anyone in the org can now create an agent in one of those directories, running
one of those commands, and nothing else.

An `-allow` entry is a whole command line, so the flags an agent needs are part
of what the org may start rather than something somebody types at it once it is
up: `-allow "claude --model opus"`.

That bounds what can be **started**, not what a running agent can **reach**: it
has the host user's account, so `~/.ssh` and every other repo on the disk are
open to whoever is driving it. Run the host as a user that owns only what the org
should have — a dedicated account, or a machine that holds nothing else.

The host dials out, so there is no port to open. An unreachable hub is reported
and retried; agents keep running and say what they are when it comes back.

Agents are meant to outlive the things that interrupt them. One that exits is
started again, and one that will not stay up is marked failed rather than
restarted for ever. What is running is written to `-state`, so restarting the
host — or the machine — brings the org's agents back rather than an empty
machine.

Whoever runs the host does not use it. They do not need a terminal open, and
nothing the org does with an agent should ever require them.

## Which agents

Any command that draws a terminal will run. The org can watch it, take its
keyboard, type at it, stop it and restart it, and none of that depends on kolo
knowing what the command is.

Two things do, and both are read off the agent's own screen:

- **which of them are asking something**, which is what makes the agent list a
  board rather than a row of black rectangles, and what lets a question be
  answered by whoever is free instead of by whoever takes the keyboard
- **how to bring back its last conversation**, without which every restart is a
  fresh start

Kolo ships with those for `claude`. For anything else, describe it in
`~/.kolo/kinds.json` — the host's file, because the host is the machine that
knows what it is running, and the hub is told rather than configured:

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

| field | what it is |
|---|---|
| `idle` | the hints the input box carries when it can take a line. Any one of them is enough: they change between versions and modes while meaning the same thing |
| `busy` | what the agent puts on screen while it is working. This is the one that matters most — without it, working is indistinguishable from waiting |
| `dialogFooter` | what a question's footer says |
| `dialogSelected` | the sigil in front of the highlighted choice |
| `resume` | what to append to the command line to continue the last conversation |
| `settle` | for an agent that says nothing while it waits: how long the screen must be unchanged to read as idle. Prefer leaving it out — see `docs/probe-findings.md` #6 |

An entry replaces a kind kolo ships with rather than merging into it, so an agent
that moved its footer between releases is fixed here without one of kolo.

These are strings from that agent's screen, not from its source, and getting one
subtly wrong is worse than leaving it out: an agent that reads as idle while it
is working swallows what anybody sends it. Record a session rather than guess —
`cmd/kolorec` drives an agent through a script and writes down what came back.

A command with no entry still runs. It is watched and typed at like any other;
what it loses is the list saying what it is doing, the answer buttons, and its
conversation across a restart. Kolo says so rather than guessing: an unreadable
screen reads as unknown, and nothing is claimed about it.

## Everyone else

Open the invite link and say what to call you. Failing that — an org that mints
one token per person with `kolo token` — open the hub and paste the token that
was sent to you. The agent
list is the front door: create one, join one someone else is already using, send
it work, answer its questions, restart it, stop it.

## Revoking a member

Remove their entry from the org file. Within a couple of seconds their token
stops working and anything they had open — a screen they were watching — is
disconnected, because a browser being handed frames makes no further request for
a check to fail. A hash cannot be turned back into a token, so there is nothing
else to clean up.

The same goes for a machine: delete a host's line and it is dropped rather than
left connected and obeying. It will keep trying to reconnect, and keep being
refused; stopping it is a separate act on that machine.

Nothing here reaches an agent that is already running. Revoking says who may
drive, not what is on the disk.
