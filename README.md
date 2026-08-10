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

Status: in development.
