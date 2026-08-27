# Security

## Reporting

Report a vulnerability privately through GitHub's [security advisory
form](https://github.com/whosgotch/kolo/security/advisories/new), not as a
public issue. Expect an acknowledgement within a few days.

Kolo is a small project without a paid security team. There is no bounty, and
no promised patch window. What there is: your report read properly, a fix
worked on in the open once it is safe to be open about it, and credit in the
advisory unless you would rather not have it.

## What kolo is, so a report can be aimed well

Kolo runs CLI agents on a machine somebody lends to their team, and puts their
terminals in a browser. That shape means some things are **by design** and not
vulnerabilities:

- **The hub executes code on the host.** Starting an agent is the product.
  Any authenticated member can start one, type at it, and stop it.
- **A running agent has the host user's whole account.** `-dir` and `-allow`
  bound what may be *started*, never what a started process may *reach*. An
  agent can read `~/.ssh`. The documented answer is to run the host as a user
  that owns only what the org should have.
- **There are no roles.** Every member can do everything. Attribution in the
  journal is what stands in for permissions, deliberately.
- **Plain HTTP sends a member's token readable.** Kolo says so, in `kolo up`'s
  own output and in the docs, and offers `-tls-domain` for anything crossing a
  network you do not trust.

What **is** worth reporting:

- Doing any of the above **without a valid member or host credential**, or as a
  member whose line has been removed from the org file.
- Reading or forging another org's, member's or host's credentials: token
  recovery from a hash, invite-token guessing, session-cookie forgery.
- Spending an invite past its expiry or its use count, or minting a member
  without one.
- Reaching an agent belonging to a host that never lent it, or escaping the
  `-dir` / `-allow` bounds on what may be **started**.
- Anything a browser can be made to do across origins: CSRF against the
  mutating routes, XSS on the org page, token leakage into a URL or a log.
- Getting the hub to serve a certificate for a domain not in `-tls-domain`.

## Where the secrets live

- `~/.kolo/org.json` holds **hashes, never member tokens**, so it is meant to
  be safe in version control. The one exception is the standing invite's own
  token, kept on purpose so the same link can be shown twice; an invite can
  only be spent on becoming a member and expires on its own.
- Host tokens are minted fresh on every `kolo up` and written down nowhere.
- Invite links carry their token in the **URL fragment**, which browsers never
  send to a server, so it stays out of access logs.

## Supported versions

The latest release, and `main`. There are no backports.
