# kolo

Long-lived CLI agents (Claude Code, opencode, anything that draws a terminal)
shared by a whole team. A **host** machine lends itself to the org and runs the
agents under pseudo-terminals; everyone else watches each agent's screen live in
a browser and types at it directly. Whoever types last drives; everybody sees
who that is.

The host dials out to the hub over one websocket and is never listened to: no
inbound port, no firewall rule, no tunnel. Only hosts install kolo — members
open a link. One Go binary, no dependencies.

## Quick start

```
$ cd ~/work/api
$ kolo up

Open it and sign in with this. It is stored nowhere, so keep it:

    kolo_yPNNK8ZnHdvgKFDiQV2Oc…
```

`kolo up` starts a hub and, by default, lends any directory this user can
reach (`-dir` narrows that). Send the printed link to your team; whoever
opens it picks a name and is in. Later members: `kolo invite`. See the org:
`kolo who`.

For hub and hosts on separate machines, TLS, and every flag:
[docs/reference.md](docs/reference.md).

## Documentation

- [Reference](docs/reference.md) — deployment, agents and `kinds.json`, the
  input model, restart/resume, the log, security posture, repo layout.
- `kolo help` and `kolo doctor` say most of this from the binary itself.

## Development

```
make install   # build from source onto your PATH
make test      # go test ./...
make brand     # regenerate icons (Node + Chrome)
```

Commits are small and described in plain sentences; docs live next to the code
that makes them true.

## License

AGPL-3.0 — see [LICENSE](LICENSE). Kolo is a network service by nature; the
AGPL's condition is that a modified copy run as a service still owes its
users the source. Want to embed or host kolo under different terms? Open an
issue.
