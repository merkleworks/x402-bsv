# @merkleworks/x402-client

TypeScript client library for the **x402-BSV** payment protocol. Provides a drop-in `fetch()` replacement that transparently handles HTTP 402 Payment Required challenges using BSV transactions.

## Install

```bash
npm install @merkleworks/x402-client
```

Requires Node.js >= 18. Zero runtime dependencies.

## Quick Start

```typescript
import { X402Client } from "@merkleworks/x402-client"

const client = new X402Client({
  delegatorUrl: "https://demo.x402.merkleworks.io",
})

// Works like fetch() — payments happen automatically
const res = await client.fetch("https://demo.x402.merkleworks.io/v1/expensive")
const data = await res.json()
```

The client is **stateless** and requires **no wallet**. The delegator service handles key management and fee funding.

## How It Works

```
Client                    Gateway                   Delegator          WoC
  |                         |                          |                |
  |-- GET /resource ------->|                          |                |
  |<-- 402 + Challenge -----|                          |                |
  |                         |                          |                |
  |-- Build partial tx      |                          |                |
  |-- POST /delegate/x402 --+------------------------->|                |
  |<-- Completed tx ---------+-------------------------+                |
  |                         |                          |                |
  |-- POST /tx/raw ----------+-------------------------+--------------->|
  |<-- txid -----------------+-------------------------+----------------|
  |                         |                          |                |
  |-- GET /resource ------->|                          |                |
  |   + X402-Proof header   |                          |                |
  |<-- 200 + content -------|                          |                |
```

1. Send the original HTTP request
2. On 402, parse the `X402-Challenge` header
3. Build a partial transaction (nonce input + payee output)
4. Submit to the delegator for fee input completion and signing
5. Broadcast the completed transaction via WhatsOnChain
6. Retry the original request with the `X402-Proof` header

## Configuration

```typescript
interface X402ClientConfig {
  /** Base URL of the delegator service. */
  delegatorUrl: string

  /** Path on the delegator for tx completion. @default "/delegate/x402" */
  delegatorPath?: string

  /** Base URL for WhatsOnChain broadcast API. @default WOC_MAINNET */
  broadcastUrl?: string

  /** Extra headers sent with every proxied request. */
  defaultHeaders?: Record<string, string>

  /** Override the global fetch (useful for testing). */
  fetch?: typeof globalThis.fetch
}
```

### Examples

```typescript
// Mainnet (default)
const client = new X402Client({
  delegatorUrl: "https://your-gateway.example.com",
})

// Testnet
import { WOC_TESTNET } from "@merkleworks/x402-client"

const client = new X402Client({
  delegatorUrl: "https://testnet-gateway.example.com",
  broadcastUrl: WOC_TESTNET,
})

// With default headers
const client = new X402Client({
  delegatorUrl: "https://gateway.example.com",
  defaultHeaders: {
    "X402-Client": "my-app/1.0",
  },
})
```

## Protocol Profiles

The client supports both x402 protocol profiles:

- **Profile A** (Client-Built): The client constructs the full unsigned transaction with nonce input and payee output. All inputs are signed with `0xC1` (SIGHASH_ALL|ANYONECANPAY|FORKID).

- **Profile B** (Gateway Template): The gateway provides a pre-signed template transaction in the challenge. The nonce input is signed with `0xC3` (SIGHASH_SINGLE|ANYONECANPAY|FORKID). The client passes the template directly to the delegator.

Profile selection is automatic based on the challenge contents.

## Error Handling

```typescript
import {
  X402ChallengeError,
  DelegatorError,
  BroadcastError,
} from "@merkleworks/x402-client"

try {
  const res = await client.fetch(url)
} catch (err) {
  if (err instanceof X402ChallengeError) {
    // Invalid or expired challenge
  } else if (err instanceof DelegatorError) {
    // Delegator rejected the transaction
    console.log(err.code)   // e.g. "invalid_partial_tx"
    console.log(err.status) // e.g. 400
  } else if (err instanceof BroadcastError) {
    // Transaction broadcast failed
    console.log(err.code) // HTTP status from WoC
  }
}
```

## Advanced Usage

Individual protocol components are exported for custom integrations:

```typescript
import {
  parseChallenge,
  buildPartialTransaction,
  isTemplateMode,
  buildProofHeader,
  hashHeaders,
  hashBody,
  HttpDelegator,
  WoCBroadcaster,
} from "@merkleworks/x402-client"

// Parse a challenge header manually
const { challenge, hash } = parseChallenge(headerValue)

// Build a partial transaction
const partialHex = buildPartialTransaction(challenge)

// Use a custom delegator or broadcaster
const delegator = new HttpDelegator("https://gateway.example.com")
const broadcaster = new WoCBroadcaster()
```

## Testing

```bash
# Unit tests
npm test

# Live integration test (costs ~100 sats per run)
npx tsx test/live.test.ts
```

## License

Apache-2.0
