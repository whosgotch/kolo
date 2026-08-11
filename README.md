# Kolo

Make a locally running CLI AI agent multiplayer.

```
$ kolo claude
  session live: https://<tunnel-host>/s/<secret>
```

Guests open the link, see the agent's terminal live, and can send it text prompts.
Every guest message is injected into the agent's stdin prefixed with the sender's
nickname.

A guest can send text and nothing else — no keystrokes, no control characters, no
shell access, no ability to interrupt. The link grants access to a *conversation*,
not to a machine.

> **Security:** anyone with the link can send prompts to an agent that edits files
> on the host's machine with the host's permissions. The URL secret is the only
> access control. Share the link only with people you already trust.

## What works today

Milestone 1: watching. Everything above except the guest input and the tunnel.

```
$ go build -o kolo ./cmd/kolo
$ ./kolo claude
session live: http://127.0.0.1:54321/
```

The agent runs as it always does — the terminal is still yours, keystrokes and
all. Open that URL in a browser and you see the same screen, live. Opening it
mid-session works: the page is caught up to whatever is already on screen before
the live stream starts.

Today the link is localhost only, serves one viewer at a time, and is read-only:
the socket disconnects a browser that tries to send anything. Guest messages, the
gate that holds them while the agent is asking the host to approve something, and
the tunnel are the milestones after this one.

Run `go test ./...` for the test suite. Two of them replay a recorded session
through the emulator; `KOLO_RAW=<capture> go test ./internal/term` points them at
a real one.
