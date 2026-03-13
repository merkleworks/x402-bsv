# x402 Protocol

## Summary

The x402 protocol defines a stateless settlement-gated HTTP authorization model. Request execution is conditioned on verifiable economic settlement using BSV transactions.

The protocol uses the HTTP 402 status code to signal that payment is required, then verifies on-chain settlement before granting access.

## Canonical Specification

The normative protocol specification is maintained at:

**https://github.com/ruidasilva/merkleworks-x402-spec**

If any behavior in this implementation conflicts with the specification, the specification prevails.

## Specification Tiers

The protocol is governed by a tiered authority model:

| Tier | Scope |
|------|-------|
| **0** | Frozen protocol invariants — foundational principles that do not change |
| **1** | Wire-level protocol: HTTP headers, challenge/proof format, status codes |
| **2** | Reference implementation architecture: component roles, signing rules, pool management |

Higher tiers always prevail. See the [specification repository](https://github.com/ruidasilva/merkleworks-x402-spec) for details.

## Protocol Flow

1. Client sends an HTTP request to a protected endpoint
2. Gateway responds `402 Payment Required` with an `X402-Challenge` header containing a nonce UTXO, price, payee, expiry, and request binding
3. Client constructs a BSV transaction spending the nonce UTXO, paying the required amount to the payee
4. Client submits the partial transaction to the delegator for fee completion
5. Delegator adds fee inputs, signs only its own inputs (`SIGHASH_ALL | ANYONECANPAY | FORKID`)
6. Client broadcasts the completed transaction to the BSV network
7. Client retries the original request with an `X402-Proof` header containing the signed transaction
8. Gateway verifies the proof (structure, binding, amount, payee, mempool acceptance) and returns `200 OK` with an `X402-Receipt`

## Key Properties

- **Stateless** — correctness depends on UTXO single-use at the network layer, not on server-side state
- **Replay-safe** — each challenge is bound to a unique nonce UTXO; once spent, it cannot be reused
- **Request-bound** — proofs are cryptographically bound to the specific request (method, path, domain, query, headers, body)
- **Fee-delegated** — the gateway sponsors miner fees so clients do not need dust UTXOs for fees
- **Infrastructure-neutral** — the protocol can be implemented by any HTTP server with access to the BSV network

## Profiles

| Profile | Description |
|---------|-------------|
| **A (Open Nonce)** | Client receives a nonce UTXO and builds the transaction from scratch |
| **B (Gateway Template)** | Gateway provides a pre-signed transaction template; client completes and signs |

Profile selection is controlled by the `TEMPLATE_MODE` configuration flag.
