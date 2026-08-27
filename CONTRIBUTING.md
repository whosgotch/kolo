# Contributing

Contributions are welcome. A patch, a bug report, or a note saying something
confused you are all useful, and kolo is early enough that any of them still
changes its shape. Thank you for caring about it.

```
go install ./cmd/kolo    # onto $(go env GOPATH)/bin, which may not be on PATH
go run ./cmd/kolo up     # a hub, lending this directory
go test ./...
```

## What lands

Build, vet, `gofmt -l .` and the tests clean. CI runs those on Linux and macOS.
A test for anything whose behaviour changed; `-race` is the run that counts.
Docs fixed in the commit that made them wrong.

## Commits

Small, and a plain sentence about what changed for somebody using kolo.

```
feat: one link an org keeps, not a drawer of them
fix(hub): one agent of each kind to a directory, not one agent
```

## Comments

Say what the code cannot: why it is this way, what breaks if it changes. Never
restate the line below.

## Agent kinds

Markers are literal strings off a real screen. Record one rather than guess:

```
go run ./cmd/kolorec -script cmd/kolorec/scripts/claude-turn.txt -dir /tmp/x claude
```

Recordings are gitignored and stay that way: they are a picture of somebody's
screen.

AGPL-3.0, like the project.
