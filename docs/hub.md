# Running a hub

Everything below is the presence slice: members' agents connect to an org and
can see each other. Nothing is sent anywhere yet.

## On the machine that will host the org

Mint credentials for each member. The token is printed once and is not stored;
losing it means issuing another.

```
$ kolo token -id artem -name "Artem"
$ kolo token -id dana  -name "Dana"
```

Collect the member entries into an org file:

```json
{
  "org": "acme",
  "members": [
    {"id": "artem", "name": "Artem", "token_hash": "a9b6…"},
    {"id": "dana",  "name": "Dana",  "token_hash": "4c11…"}
  ]
}
```

Start the hub. It listens on localhost unless told otherwise:

```
$ kolo serve -org org.json -addr 0.0.0.0:7300
kolo: hub for acme on 0.0.0.0:7300, 2 member(s)
```

> **The hub carries no TLS of its own.** A member's token is sent in a header,
> and over plain HTTP that header crosses the network in the clear, where anyone
> between the two machines can take it and use it. Reaching a hub across the
> internet means putting it behind something that terminates TLS — a reverse
> proxy, or a tunnel that does. On a trusted network, or over a VPN, plain HTTP
> is a considered choice rather than an accident.

## On each member's machine

```
$ export KOLO_HUB=https://hub.example.com
$ export KOLO_TOKEN=kolo_…
$ kolo run claude
session live: http://127.0.0.1:54321/
kolo: joined acme as artem
```

The agent runs exactly as it would without kolo. Joining the hub adds the org's
knowledge that it exists; it adds nobody's control over it.

An unreachable hub does not stop the session — it is reported and retried, and a
laptop that sleeps rejoins on its own when it wakes.

## Seeing who is there

```
$ kolo who
acme
artem   claude   artem-mbp    12m
dana    claude   dana-thinkpad just now
```

A member connected from two machines appears twice, which is the point: closing
the laptop should not remove the desktop.

## Revoking a member

Remove their entry from the org file and restart the hub. Their token stops
working; a hash cannot be turned back into one, so there is nothing else to
clean up.
