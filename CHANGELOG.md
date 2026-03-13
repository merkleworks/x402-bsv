# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.0] - 2026-03-13

### Added

- Gatekeeper middleware: HTTP 402 challenge-response settlement flow
- Delegator service: fee delegation with SIGHASH_ALL|ANYONECANPAY|FORKID (0xC1) co-signing
- UTXO pool management with in-memory and Redis backends
- Composite broadcaster with GorillaPool ARC support and circuit breaker
- Replay cache for duplicate proof detection
- BIP32 HD wallet key derivation
- Treasury management: fan-out, refill, sweep, and template repair
- React dashboard with monitoring, analytics, treasury, and testing tabs
- Server-Sent Events for live operational streaming
- Profile A (Open Nonce) and Profile B (Gateway Template) settlement modes
- CLI tools: server, client, delegator, keygen, setup
- Docker multi-stage builds for gateway and delegator
- Docker Compose deployment with Redis
- Adversarial testing harness with 6 attack scenarios
- Configurable WoC API URL and broadcaster selection
- Apache 2.0 license

[0.1.0]: https://github.com/merkleworks/x402-bsv/releases/tag/v0.1.0
