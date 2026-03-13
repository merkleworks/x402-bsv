# Project Governance

## Overview

The x402 protocol and this reference implementation are governed by a specification-first model. The canonical specification lives in the [merkleworks-x402-spec](https://github.com/ruidasilva/merkleworks-x402-spec) repository. This repository contains the reference gateway implementation.

## Authority Hierarchy

| Level | Authority |
|-------|-----------|
| Tier 0 | Frozen protocol invariants — foundational principles that do not change |
| Tier 1 | Wire-level protocol: HTTP headers, challenge/proof format, status codes |
| Tier 2 | Reference implementation architecture: component roles, signing rules, pool management |
| Code | Implementation (this repository) |

Higher tiers always prevail. If code behavior conflicts with the specification, the code is wrong.

## Protocol Changes

Protocol changes (Tier 0–2) must be proposed in the [specification repository](https://github.com/ruidasilva/merkleworks-x402-spec). Tier 0 invariants are frozen and are not open for modification.

## Implementation Changes

### Contribution Process

1. **Bug fixes** — Open an issue describing the bug, then submit a pull request referencing the issue
2. **Enhancements** — Open an issue to discuss the proposal before writing code. Major changes should be agreed upon before implementation begins
3. **New features** — Must not contradict the specification. Open an issue first to confirm alignment

### Pull Request Requirements

- All tests must pass (`make test`)
- Code must pass linting (`make lint`)
- Include tests for new functionality
- Keep changes focused — one concern per PR
- Do not introduce behavior that contradicts the specification

### Review Process

Pull requests are reviewed by project maintainers (see [MAINTAINERS.md](MAINTAINERS.md)). At least one maintainer approval is required before merging.

### Merging Policy

- Feature branches merge into `main` via squash-and-merge (clean history) or merge commit (preserving branch context) at maintainer discretion
- Force-pushing to `main` is prohibited
- All CI checks must pass before merge

## Versioning

This implementation follows semantic versioning:
- **Major** — breaking API changes or specification version bumps
- **Minor** — new features, new endpoints, backward-compatible changes
- **Patch** — bug fixes, documentation, internal refactoring

## Dispute Resolution

If a disagreement arises about whether implementation behavior is correct, the specification is the arbiter. See the [specification repository](https://github.com/ruidasilva/merkleworks-x402-spec) for the normative documents.
