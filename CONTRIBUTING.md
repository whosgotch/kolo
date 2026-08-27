# Contributing

## Getting it running

```
go install ./cmd/kolo          # put this working tree on your PATH as kolo
go run ./cmd/kolo up           # start a hub and lend this directory
go test ./...
go vet ./...
gofmt -l -w .
```

There is no build system beyond the Go toolchain, and no version to pass in:
`go build` and `go install` record the commit they came from, and kolo reads it
back out to say which build it is. A `go run` binary calls itself `dev`, which
is what it is.

`go install` puts kolo wherever `go env GOBIN` points, falling back to
`$(go env GOPATH)/bin` — which is often not on `PATH`. If `kolo` is not found
after installing, that is why:

```
export PATH="$(go env GOPATH)/bin:$PATH"
```

Running kolo in this repository writes an `org.json` here; it is gitignored,
and it is a scratch org rather than a real one.

The only build dependency is Go — the version in `go.mod`. The icons are the
exception: `node scripts/brand.mjs` regenerates them and the token stylesheet,
needs Node and a Chrome, and is only for changes in `docs/brand`.

## What lands

- **`go build`, `go vet`, `gofmt -l .` and `go test ./...` all clean.** CI runs
  exactly these, on Linux and macOS.
- **A test for behaviour that changed.** Almost everything here is a goroutine
  talking to another one, so `go test -race ./...` is the run that counts.
- **Docs updated in the same commit.** `docs/reference.md` is meant to describe
  what actually ships. A feature whose docs land later is a feature that spent
  time being a lie.

## Commits

Small, and described in a plain sentence that says what changed for somebody
using kolo — not what was refactored. The log reads as prose on purpose:

```
feat: one link an org keeps, not a drawer of them
feat: kolo doctor says a thing once
fix(hub): one agent of each kind to a directory, not one agent
```

Prefix with `feat:`, `fix:`, `docs:` or `chore:`, and a package in parentheses
when it helps. The body is for why, when why is not obvious.

## Comments

Comments say what the code cannot: why a thing is the way it is, what breaks if
it changes, which upstream bug is being worked around. They do not restate the
line below them. If a comment would only paraphrase the code, the code should
be clearer instead.

## Agent kinds

Kolo reads an agent's state off its screen, so a new kind is a set of literal
strings from a real terminal. Record one rather than guessing:

```
go run ./cmd/kolorec -script scripts/claude-turn.txt -dir /tmp/scratch claude
```

That writes a `.raw` recording and `.screen` dumps. **Recordings are gitignored
and must stay that way** — they are a picture of somebody's screen, paths and
all. Put the strings you found in `internal/adapter`, and a written-out screen
in the tests.

`kolo doctor` is what tells you a kind has stopped fitting after the agent
upgraded. Run it before assuming markers still hold.

## Repo layout

`docs/reference.md` ends with a map of the tree and what each package is for.

## Licence

Kolo is AGPL-3.0. Contributions are taken under the same licence.
