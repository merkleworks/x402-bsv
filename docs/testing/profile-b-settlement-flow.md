# Profile B — Settlement Template Flow

## Why Template Mode Exists

Profile A (Open Nonce) requires the client to construct the entire settlement transaction from scratch.  The client must know the payee address, the exact payment amount, and how to build a valid BSV transaction with the correct sighash flags.  This places significant implementation burden on every client and creates room for protocol errors.

Profile B (Gateway Template) inverts the responsibility.  The gateway builds and signs a partial transaction template that already contains the payment output.  The client only needs to append funding inputs and submit.  This makes integration dramatically simpler because the gateway defines the exact settlement structure — the client cannot accidentally underpay, use the wrong payee address, or pick an incorrect sighash flag for the nonce input.

## How 0xC3 Enables Append-Only Extension

The template's nonce input is signed with `SIGHASH_SINGLE|ANYONECANPAY|FORKID`, which has byte value **0xC3**.  This flag combination is the only choice that satisfies all Profile B requirements:

### SIGHASH_SINGLE (0x03)

Commits only to `output[input_index]`.  Since the nonce is signed at input index 0, the signature locks **only output 0** (the payment).  Outputs at index 1 and above are not covered by this signature, so sponsors can freely append change outputs without invalidating the gateway's signature.

### ANYONECANPAY (0x80)

Commits only to the input being signed (input 0).  Other inputs are not covered, so sponsors can append funding inputs at index 1 and above without invalidating the gateway's signature.

### FORKID (0x40)

Required by BSV consensus (BIP143 replay protection).  All BSV signatures must include the fork ID flag.

### Combined effect: 0x03 | 0x80 | 0x40 = 0xC3

The gateway's signature on input 0 commits to exactly two things:

1. **The input being signed** — the nonce UTXO outpoint and its unlocking script
2. **Output 0** — the payment amount and the payee's locking script

Everything else in the transaction is uncommitted.  Sponsors extend the template by appending inputs and outputs at indices >= 1 without breaking the gateway's pre-existing signature.

### What happens if someone tries to tamper?

| Tamper target | Result |
|---|---|
| Change output 0 amount | Signature verification fails — amount is part of the BIP143 preimage |
| Change output 0 script (payee) | Signature verification fails — locking script is part of the preimage |
| Move nonce to a different input index | Signature verification fails — SIGHASH_SINGLE binds to `output[input_index]` |
| Append inputs at index >= 1 | Signature survives — ANYONECANPAY excludes other inputs |
| Append outputs at index >= 1 | Signature survives — SIGHASH_SINGLE excludes other outputs |

## Why the Nonce Input Must Remain at Index 0

`SIGHASH_SINGLE` binds the signing input to `output[input_index]`.  If the nonce input is at index 0, the signature commits to output 0 (the payment).  If someone moves the nonce input to index 1, the signature would commit to output 1 instead — which may be a change output or may not exist at all.  Either way, the payment output is no longer protected.

The gateway enforces this invariant in two places:

1. **Template generation** (`internal/treasury/template.go`) — the nonce UTXO is always placed at input 0 when building the template.

2. **Delegator validation** (`internal/delegator/delegator.go`) — `verifyNonceAtIndex0()` checks that the nonce outpoint matches input 0 of the submitted transaction.  If the nonce is found at any other index, delegation is rejected.

This is a structural invariant, not just a convention.  Moving the nonce to another position is not a protocol violation that could be fixed later — it fundamentally changes which output the gateway's signature protects.

## Sighash Policy Summary

| Input | Profile A | Profile B (Template Mode) |
|---|---|---|
| Input 0 (nonce) | 0xC1 or 0x41 | **0xC3 only** |
| Input 1+ (client/sponsor) | 0xC1 or 0x41 | 0xC1 or 0x41 |
| Fee input (delegator) | 0xC1 or 0x41 | 0xC1 or 0x41 |

- **0xC3** = `SIGHASH_SINGLE|ANYONECANPAY|FORKID` — locks one output, allows appending
- **0xC1** = `SIGHASH_ALL|ANYONECANPAY|FORKID` — locks all outputs, allows appending inputs only
- **0x41** = `SIGHASH_ALL|FORKID` — locks all outputs and all inputs

Profile B restricts the nonce input to 0xC3 because 0xC1 commits to **all** outputs.  If the nonce were signed with 0xC1, sponsors could not append change outputs — this breaks the template extensibility model that is the entire point of Profile B.

## The Complete Flow

```
Client                Gateway               Delegator            Network
  |                       |                      |                    |
  | GET /protected        |                      |                    |
  |──────────────────────>|                      |                    |
  |                       |                      |                    |
  |  402 + challenge      |                      |                    |
  |  (template_tx, nonce) |                      |                    |
  |<──────────────────────|                      |                    |
  |                       |                      |                    |
  | [extend template:     |                      |                    |
  |  + funding inputs     |                      |                    |
  |  + change outputs]    |                      |                    |
  |                       |                      |                    |
  | POST partial_tx ──────────────────────────── >|                   |
  |                       |                      | + fee input        |
  |                       |                      | sign (0xC1/0x41)   |
  |                       |<─────────────────────| completed_tx       |
  |                       |                      |                    |
  | broadcast ────────────────────────────────────────────────────── >|
  |                       |                      |                    |
  | GET /protected        |                      |                    |
  | + X402-Proof header   |                      |                    |
  |──────────────────────>|                      |                    |
  |                       | verify proof         |                    |
  |                       | check nonce spent    |                    |
  |                       | verify payment output|                    |
  |                       |                      |                    |
  |  200 OK + content     |                      |                    |
  |<──────────────────────|                      |                    |
```

Each challenge uses a unique nonce UTXO.  Bitcoin consensus guarantees that an outpoint can only be spent once, providing cryptographic replay protection without any server-side session state.
