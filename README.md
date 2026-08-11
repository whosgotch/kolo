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

Everything above except the tunnel: guests watch, and guests can send.

```
$ go build -o kolo ./cmd/kolo
$ ./kolo claude
session live: http://127.0.0.1:54321/
```

The agent runs as it always does — the terminal is still yours, keystrokes and
all. Open that URL in a browser and you see the same screen, live. Opening it
mid-session works: the page is caught up to whatever is already on screen before
the live stream starts.

Guests type into a box under the terminal, never into the terminal itself, and a
message is held until the agent is idle at its input box. It is not sent while
the agent has a question on screen, because kolo has to press Enter to submit and
Enter would answer the question instead. A held message says so, rather than
disappearing.

Today the link is localhost only. The tunnel is the milestone after this one.

Run `go test ./...` for the test suite. Two of them replay a recorded session
through the emulator; `KOLO_RAW=<capture> go test ./internal/term` points them at
a real one.
