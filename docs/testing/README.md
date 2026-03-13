# Testing

## Unit Tests

Run the Go test suite:

```bash
make test
```

Tests cover: challenge construction and canonical hashing, proof verification, fee delegation, UTXO pool management (memory and Redis), replay cache, HD wallet derivation, and treasury operations.

## Postman Collections

Import the Postman collections from the `postman/` directory at the repository root:

| Collection | Profile | File |
|------------|---------|------|
| Gateway test suite | Profile A (Open Nonce) | `postman/x402-gateway.postman_collection.json` |
| Template flow | Profile B (Gateway Template) | `postman/x402-profile-b.postman_collection.json` |

### Profile A Testing

The gateway collection covers all endpoints and the full payment cycle including replay protection. See the [Profile A Postman guide](postman-profile-a.md) for step-by-step instructions.

### Profile B Testing

Profile B uses pre-signed transaction templates with `SIGHASH_SINGLE|ANYONECANPAY|FORKID (0xC3)`. See the [Profile B settlement flow](profile-b-settlement-flow.md) for the sighash mechanics and testing guide.

## Adversarial Testing

The `tools/adversary-harness/` directory contains a black-box testing tool that validates protocol invariants under adversarial conditions:

| Scenario | What it tests |
|----------|---------------|
| Miner ordering | Concurrent delegation race conditions |
| Mempool suppression | False negatives/positives in mempool checks |
| Delayed broadcast | TTL enforcement under slow broadcast |
| Fee abuse | Concurrent fee pool hammering |
| Nonce lease TTL reclaim | Resource cleanup after lease expiry |
| Gateway crash recovery | State persistence and recovery |

Run the harness against a running gateway:

```bash
go run ./tools/adversary-harness -gateway http://localhost:8402
```

## Linting

```bash
make lint
```

Runs `go vet` across all packages.
