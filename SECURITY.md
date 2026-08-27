# Security

Report privately through [GitHub's advisory form][form], never a public issue.

[form]: https://github.com/whosgotch/kolo/security/advisories/new

## By design, not bugs

Kolo runs agents on a machine somebody lends to their team:

- **Any member can start one, type at it, and stop it.** That is the product.
- **A running agent has the host user's whole account.** `-dir` and `-allow`
  bound what may be *started*, not what it can *reach*.
- **There are no roles.** Everyone can do everything. The log says who did.
- **Plain HTTP sends tokens in the clear.** Use `-tls-domain` off a trusted network.

## Worth reporting

Any of it without a valid credential, or with a revoked one. Forged or
recovered tokens. An invite spent past its limit. XSS, CSRF, or a token leaking
into a URL or a log.

Latest release and `main`.
