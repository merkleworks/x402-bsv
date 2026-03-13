# Architecture

## System Overview

```
┌─────────────────────────────────────────────────────────────┐
│                   x402 Settlement Gateway                    │
├─────────────────────────────────────────────────────────────┤
│                                                             │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐    │
│  │  Gatekeeper │    │  Delegator  │    │  Dashboard  │    │
│  │  (402 flow) │    │ (fee-only)  │    │  (React UI) │    │
│  └──────┬──────┘    └──────┬──────┘    └──────┬──────┘    │
│         │                  │                  │            │
│  ┌──────┴──────────────────┴──────────────────┴──────┐    │
│  │                   UTXO Pools                       │    │
│  │       ┌──────────────┐  ┌──────────────┐          │    │
│  │       │  Nonce UTXO  │  │  Fee UTXO   │           │    │
│  │       │    Pool      │  │    Pool     │            │    │
│  │       └──────────────┘  └──────────────┘          │    │
│  └────────────────────────────────────────────────────┘    │
│                           │                                 │
│              ┌────────────┴────────────┐                   │
│              │    Redis / In-Memory    │                   │
│              │   (operational store)   │                   │
│              └─────────────────────────┘                   │
└─────────────────────────────────────────────────────────────┘
```

## Component Roles

**Gatekeeper** (`internal/gatekeeper/`)
- Issues challenges containing a nonce UTXO, price, payee, expiry, and request binding
- Binds challenge to request fields via canonical hashing
- Verifies proofs on retry requests
- Does not sign, construct, or modify transactions

**Delegator** (`internal/delegator/`)
- Validates client-constructed partial transaction structure
- Adds miner-fee inputs from the Fee UTXO Pool
- Signs only its own fee inputs using `SIGHASH_ALL | ANYONECANPAY | FORKID (0xC1)`
- Returns the completed transaction to the client
- Never broadcasts

**Client** (`cmd/client/`)
- Constructs and signs the payment portion of the transaction
- Submits the partial transaction to the delegator for fee completion
- Broadcasts the completed transaction to the network
- Retries the original request with the proof header

**Network**
- Enforces UTXO single-use at the consensus layer
- Provides replay protection — a spent nonce UTXO cannot be spent again
- On-chain finality is the ultimate double-spend arbiter

## Settlement Flow

```
Client                          Gateway                      Network
  │                               │                              │
  │  1. GET /v1/expensive         │                              │
  │  ─────────────────────────►   │                              │
  │                               │                              │
  │  2. 402 + X402-Challenge      │                              │
  │     (nonce UTXO, amount,      │                              │
  │      payee, expiry, binding)  │                              │
  │  ◄─────────────────────────   │                              │
  │                               │                              │
  │  3. Build partial TX          │                              │
  │     (spend nonce UTXO,        │                              │
  │      add payee output,        │                              │
  │      sign with 0xC1)          │                              │
  │                               │                              │
  │  4. POST /delegate/x402       │                              │
  │  ─────────────────────────►   │                              │
  │                               │                              │
  │         5. Validate structure  │                              │
  │            Add fee inputs      │                              │
  │            Sign fee inputs     │                              │
  │                               │                              │
  │  6. Return completed TX       │                              │
  │  ◄─────────────────────────   │                              │
  │                               │                              │
  │  7. Broadcast TX              │                              │
  │  ──────────────────────────────────────────────────────────► │
  │                               │                              │
  │  8. GET /v1/expensive         │                              │
  │     + X402-Proof header       │                              │
  │  ─────────────────────────►   │                              │
  │                               │                              │
  │  9. Verify proof → 200 OK     │                              │
  │     + X402-Receipt            │                              │
  │  ◄─────────────────────────   │                              │
```

The client constructs, signs, and broadcasts the transaction. The delegator adds fee inputs and signs only those. It never holds client keys and never broadcasts.

## Key Concepts

### Nonce UTXO Pool
- Collection of 1-satoshi UTXOs used as challenge identity
- Each challenge references exactly one nonce UTXO that the client must spend
- Single-use is enforced by the network: a spent UTXO cannot be spent again
- Leased to clients with a configurable TTL (default 5 minutes)
- Reclaim loop recovers expired leases every 30 seconds (operational, not correctness-critical)

### Fee UTXO Pool
- Collection of UTXOs consumed by the delegator to pay miner fees
- Delegator adds fee inputs to the client's partial transaction
- Fee inputs are signed with `SIGHASH_ALL | ANYONECANPAY | FORKID (0xC1)`
- This sighash allows the delegator to sign without invalidating the client's existing signatures

### Challenge
Contains:
- Nonce UTXO outpoint (client must spend this)
- Amount (price in satoshis)
- Payee address (settlement destination)
- Expiry (validity period)
- Request binding (canonical hash of method, path, domain, query, headers, body)

### Proof
Contains:
- Complete signed transaction (spending the nonce UTXO)
- Challenge hash reference
- Request binding for verification

### Sighash 0xC1
`SIGHASH_ALL | ANYONECANPAY | FORKID` — the client signs all outputs but only its own input. The delegator can then append fee inputs without breaking the client's signature. The delegator signs its fee inputs the same way.

## Profiles

### Profile A — Open Nonce
The client constructs the entire settlement transaction from the challenge parameters. The client must know how to build a valid BSV transaction with the correct sighash flags and payment output.

### Profile B — Gateway Template
The gateway builds and signs a partial transaction template that already contains the payment output. The client only needs to append funding inputs. See [Profile B Settlement Flow](testing/profile-b-settlement-flow.md) for the 0xC3 sighash mechanics.

## Project Structure

```
x402-bsv/
├── cmd/
│   ├── server/          # Main HTTP server
│   ├── client/          # Test CLI client
│   ├── delegator/       # Standalone fee delegation service
│   ├── keygen/          # Key generation utility
│   └── setup/           # Interactive setup wizard
├── internal/
│   ├── config/          # Environment configuration
│   ├── hdwallet/        # BIP32 HD wallet derivation
│   ├── pool/            # UTXO pool management (Memory/Redis)
│   ├── gatekeeper/      # HTTP 402 middleware and proof verification
│   ├── delegator/       # Fee-input addition and signing (fee-only)
│   ├── feedelegator/    # Fee delegation HTTP API
│   ├── challenge/       # Challenge/proof construction and hashing
│   ├── replay/          # Operational replay cache (in-memory)
│   ├── pricing/         # Dynamic pricing
│   ├── broadcast/       # TX broadcasting (Mock/WoC/ARC composite)
│   ├── treasury/        # Pool funding, fan-out, and sweep
│   └── dashboard/       # React dashboard API
├── dashboard/           # React frontend source
├── tools/
│   └── adversary-harness/  # Adversarial protocol testing
├── postman/             # Postman collection JSON files
├── docs/                # Documentation
├── Dockerfile
├── docker-compose.yml
├── Makefile
└── go.mod
```
