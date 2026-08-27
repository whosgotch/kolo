# kolo

Long-lived CLI agents (Claude Code, opencode, anything that draws a terminal)
shared by a whole team. A **host** machine lends itself to the org and runs the
agents under pseudo-terminals. Everyone else watches each agent's screen live
in a browser and types at it directly. Whoever types last drives, and everybody
sees who that is.

The host dials out to the hub over one websocket and never accepts an inbound
connection: no open port, no firewall rule, no tunnel. Only hosts install kolo.
Members open a link. One Go binary, no dependencies.

## Quick start

On the machine you are lending, which is the only one that installs anything:

```
$ go install github.com/whosgotch/kolo/cmd/kolo@latest

$ cd ~/work/api
$ kolo up

api is up at http://192.168.0.12:7300
Lending anywhere · claude codex · 3 members

Invite  http://192.168.0.12:7300/join#kolo_yPNNK8ZnHdvgKFDiQV2Oc…
        10 uses left, until Mon 31 Aug 20:34 · kolo invite -new for a fresh one
```

`kolo up` starts a hub and lends any directory this user can reach; `-dir`
narrows it. Send the invite to your team. Whoever opens it picks a name and is
in. The org gets a single link, reprinted on every start and by `kolo invite`,
so nobody has to hand out one per person. If it leaks, `kolo invite -new`
replaces it. To see the org, run `kolo who`.

For hub and hosts on separate machines, TLS, and every flag: `kolo help` and
`kolo help <command>`.

## Documentation

- `kolo help`, `kolo help <command>` and `kolo doctor` — running it, every
  flag, and what this machine can and cannot do. The binary is the manual.
- [Reference](docs/reference.md) — how it works and why: agents and
  `kinds.json`, the input model, restart/resume, the log, repo layout.
- [Security](SECURITY.md) — the threat model, and reporting a hole.
- [Contributing](CONTRIBUTING.md) — building it, and what lands.

## Development

```
go install ./cmd/kolo     # put this working tree on your PATH as kolo
go test ./...
node brand/build.mjs      # regenerate icons (Node + Chrome)
```

Commits are small and described in plain sentences; docs live next to the code
that makes them true.

## License

AGPL-3.0 — see [LICENSE](LICENSE). Kolo is a network service by nature, and the
AGPL's condition is that a modified copy run as a service still owes its users
the source. Want to embed or host kolo under different terms? Open an issue.

The page also carries other people's work under their own terms. xterm.js draws
the terminal (MIT, `internal/hub/ui/assets/xterm-MIT.txt`), and the fonts are
Archivo and JetBrains Mono (OFL, beside them).
