# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in this project, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, email **security@merkleworks.com** with:

- A description of the vulnerability
- Steps to reproduce the issue
- The potential impact
- Any suggested fix (optional)

## Response Timeline

| Step | Timeframe |
|------|-----------|
| Acknowledgement | Within 48 hours |
| Initial assessment | Within 5 business days |
| Fix or mitigation plan | Within 30 days for confirmed issues |
| Public disclosure | After a fix is available |

## Scope

This policy covers the x402 reference implementation in this repository. For protocol-level concerns (challenge/proof format, settlement semantics), please file against the [specification repository](https://github.com/ruidasilva/merkleworks-x402-spec).

## Security Considerations

This is a reference implementation of a payment protocol. Operators deploying this software should:

- Never expose private keys in environment variables on shared systems
- Use Redis with authentication enabled in production
- Run behind a reverse proxy with TLS termination
- Monitor UTXO pool levels to prevent exhaustion
- Review the [deployment checklist](README.md) before going live
