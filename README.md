# x402 Settlement Gateway

[![CI](https://github.com/merkleworks/x402-bsv/actions/workflows/ci.yml/badge.svg)](https://github.com/merkleworks/x402-bsv/actions/workflows/ci.yml)

## Protocol Status

| | |
|---|---|
| **x402 Protocol** | v1.0-spec (Frozen) |
| **Specification Repository** | https://github.com/ruidasilva/merkleworks-x402-spec |
| **Reference Implementation** | this repository |

The x402 protocol defines a stateless settlement-gated HTTP authorization model where request execution is conditioned on verifiable economic settlement. The canonical protocol specification is maintained separately in the [merkleworks-x402-spec](https://github.com/ruidasilva/merkleworks-x402-spec) repository.

This repository provides the reference gateway implementation. When implementation behavior diverges from the specification, the specification prevails.

## Overview

The gateway implements settlement-gated HTTP execution using the x402 protocol:

- **HTTP 402 challenge-proof-retry flow** for protected endpoints
- **Nonce-UTXO issuance** for replay-safe payment challenges
- **Deterministic request binding** using canonical hashing
- **Fee delegation** — delegator adds miner-fee inputs and signs only its own inputs
- **Stateless proof verification** before endpoint execution
- **Configurable acceptance semantics** (mempool visibility or confirmation depth)
- **Operational monitoring** via React dashboard

Replay protection is enforced by UTXO single-use at the network layer. Correctness does not depend on nonce databases, account ledgers, or balance tracking.

## Quick Start

```bash
# 1. Generate keys
make setup

# 2. Start in demo mode (in-memory, mock broadcaster)
make demo

# 3. Test the settlement flow (in another terminal)
make client

# 4. Open dashboard
open http://localhost:8402
```

For production deployment with Docker and Redis, see [Deployment](docs/deployment.md).

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                   x402 Settlement Gateway                    │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐    │
│  │  Gatekeeper │    │  Delegator  │    │  Dashboard  │    │
│  │  (402 flow) │    │ (fee-only)  │    │  (React UI) │    │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘    │
│         │                  │                  │            │
│  ┌──────┴──────────────────┴──────────────────┴──────┐    │
│  │              UTXO Pools (Nonce / Fee)              │    │
│  └───────────────────────┬────────────────────────────┘    │
│              ┌────────────┴────────────┐                   │
│              │    Redis / In-Memory    │                   │
│              └─────────────────────────┘                   │
└─────────────────────────────────────────────────────────────┘
```

The **Gatekeeper** issues 402 challenges and verifies proofs. The **Delegator** adds fee inputs and signs only those — it never holds client keys or broadcasts. The **Client** constructs, signs, and broadcasts the settlement transaction.

Full architecture details, settlement flow diagrams, and key concepts: [Architecture](docs/architecture.md)

## Client Library

The [`@merkleworks/x402-client`](client-js/) package provides a drop-in `fetch()` replacement that transparently handles 402 payment challenges. No wallet or balance tracking required.

```typescript
import { X402Client } from "@merkleworks/x402-client"

const client = new X402Client({
  delegatorUrl: "https://demo.x402.merkleworks.io",
})

const res = await client.fetch("https://demo.x402.merkleworks.io/v1/expensive")
```

See the [client README](client-js/README.md) for install, configuration, protocol profiles, error handling, and advanced usage.

## Documentation

| Document | Description |
|----------|-------------|
| [Architecture](docs/architecture.md) | Components, settlement flow, key concepts, project structure |
| [Configuration](docs/configuration.md) | Environment variables and `.env` setup |
| [API Reference](docs/api-reference.md) | All endpoints, headers, request/response formats |
| [Deployment](docs/deployment.md) | Demo, Docker, production checklist, security |
| [Testing](docs/testing/README.md) | Unit tests, Postman collections, adversarial harness |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and fixes |

### Protocol & Governance

| Document | Description |
|----------|-------------|
| [Protocol](PROTOCOL.md) | Specification hierarchy and protocol overview |
| [Governance](GOVERNANCE.md) | Authority model and contribution policy |
| [Code of Conduct](CODE_OF_CONDUCT.md) | Contributor Covenant v2.1 |
| [Contributing](CONTRIBUTING.md) | Development setup and PR process |
| [Security](SECURITY.md) | Vulnerability reporting policy |
| [Changelog](CHANGELOG.md) | Release history |

## License

Apache License 2.0 — see [LICENSE](LICENSE).
