# Setting up

Two things run: a **hub**, which the org connects to, and a **host**, which lends
a machine for agents to run on. They can be the same machine or different ones.

The hub exists today. The host does not yet — `kolo run` is still the
single-machine shape it replaces. See `docs/roadmap.md`.

## The hub

Mint credentials for each member, and one for each machine that will run agents.
A token is printed once and is not stored; losing it means issuing another.

```
$ kolo token -id artem -name "Artem"
$ kolo token -id dana  -name "Dana"
$ kolo token -host -id devbox
```

Collect the entries into an org file:

```json
{
  "org": "acme",
  "members": [
    {"id": "artem", "name": "Artem", "token_hash": "a9b6…"},
    {"id": "dana",  "name": "Dana",  "token_hash": "4c11…"}
  ],
  "hosts": [
    {"id": "devbox", "token_hash": "7e02…"}
  ]
}
```

A machine authenticates as itself, not as whoever set it up, so the log can say
which machine ran something without pinning it on a person who was not involved.
Ids are unique across both lists.

Start it. It listens on localhost unless told otherwise:

```
$ kolo serve -org org.json -addr 0.0.0.0:7300
kolo: hub for acme on 0.0.0.0:7300, 2 member(s)
```

> **The hub carries no TLS of its own.** A member's token is sent in a header,
> and over plain HTTP that header crosses the network in the clear, where anyone
> in between can take it and use it. Reaching a hub across the internet means
> putting it behind something that terminates TLS. On a trusted network, or over
> a VPN, plain HTTP is a considered choice rather than an accident.

## The host

One machine in the org runs agents for everybody. It should be one that stays
on — a dev box or a spare desktop, not a laptop that closes at six.

```
$ export KOLO_HUB=https://hub.example.com
$ export KOLO_TOKEN=kolo_…
$ kolo host --dir ~/work/api --dir ~/work/web --allow claude
kolo: hosting for acme, 2 directories
```

Anyone in the org can now create an agent in one of those directories, running
one of those commands, and nothing else.

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

## Everyone else

Open the hub in a browser and sign in with a token. The agent list is the front
door: create one, join one someone else is already using, send it work, answer
its questions, stop it.

## Revoking a member

Remove their entry from the org file and restart the hub. Their token stops
working; a hash cannot be turned back into one, so there is nothing else to clean
up.
