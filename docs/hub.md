# Setting up

Two things run: a **hub**, which the org connects to, and a **host**, which lends
a machine for agents to run on. They can be the same machine or different ones.

## The hub

An org file starts as its name. It is the only thing kolo cannot pick for you:

```
$ echo '{"org": "acme"}' > org.json
```

Then mint credentials — one per member, and one for each machine that will run
agents. Each is written into the org file and printed once, here, and nowhere
else; losing one means issuing another.

```
$ kolo token -id dana -name "Dana"
Added Dana to org.json.

Send Dana these two, once. The token is stored nowhere:

    http://127.0.0.1:7300
    kolo_bAXFSPCQ01No…
```

```
$ kolo token -host -id devbox -hub https://hub.acme.com
Added devbox to org.json.

Run this on devbox. It carries both the hub and the token, and is stored nowhere:

    kolo host -join kolo_join_eyJodWIiOiJod… \
        -dir <a directory to lend> -allow claude
```

`-hub` is where they will reach the hub, and it defaults to the address `kolo
serve` listens on, so everything on one machine needs no flag at all.

The file that results is small enough to read at a glance and belongs in version
control — it holds hashes, never tokens:

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

**The hub reads this file once, at startup.** Somebody minted after it started
cannot connect until it is restarted, which is the same thing that makes
revoking work.

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

Open the hub in a browser and sign in with the token that was sent to you — the
two lines `kolo token` printed are the whole of what a member needs. The agent
list is the front door: create one, join one someone else is already using, send
it work, answer its questions, restart it, stop it.

## Revoking a member

Remove their entry from the org file and restart the hub. Their token stops
working; a hash cannot be turned back into one, so there is nothing else to clean
up.
