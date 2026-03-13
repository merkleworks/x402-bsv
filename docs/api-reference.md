# API Reference

## Endpoint Summary

| Method | Path | Description |
|--------|------|-------------|
| GET | `/v1/expensive` | Example protected endpoint (402-gated) |
| GET | `/health` | Server health and pool statistics |
| POST | `/delegate/x402` | Fee delegation (binary TX) |
| POST | `/api/v1/tx` | Fee delegation (JSON TX) |
| GET | `/api/v1/config` | Get server configuration |
| PUT | `/api/v1/config` | Update server configuration |
| GET | `/api/v1/stats/summary` | Aggregate statistics |
| GET | `/api/v1/stats/timeseries` | Time-series data |
| GET | `/api/v1/revenue` | Revenue statistics |
| GET | `/api/v1/treasury/info` | Treasury and pool info |
| GET | `/api/v1/treasury/utxos` | Treasury UTXOs |
| POST | `/api/v1/treasury/fanout` | Fan-out UTXOs to a pool |
| POST | `/api/v1/treasury/sweep` | Sweep UTXOs to treasury |
| POST | `/api/v1/treasury/sweep-revenue` | Sweep settlement revenue |
| POST | `/api/v1/broadcast` | Broadcast a raw transaction |
| GET | `/api/v1/health/broadcasters` | Broadcaster health (composite mode) |
| POST | `/api/v1/pools/reconcile` | Detect and remove zombie UTXOs |
| GET | `/api/v1/events/stream` | SSE event stream |

## X402 Protocol Headers

| Header | Direction | Description |
|--------|-----------|-------------|
| `X402-Challenge` | Response (402) | Base64url-encoded Challenge JSON |
| `X402-Accept` | Response (402) | Payment scheme: `bsv-tx-v1` |
| `X402-Proof` | Request (retry) | Client payment proof JSON |
| `X402-Receipt` | Response (200) | Payment receipt hash |
| `X402-Receipt-Time` | Response (200) | ISO 8601 timestamp |
| `X402-Status` | Response | `accepted`, `pending`, `rejected`, or `error` |

## 402 Challenge-Proof Flow

### Step 1 — Client sends request without proof

```
GET /v1/expensive HTTP/1.1
```

### Step 2 — Gateway responds 402 with challenge

```http
HTTP/1.1 402 Payment Required
X402-Challenge: <base64url-encoded JSON>
X402-Accept: bsv-tx-v1

{"status":402,"code":"payment_required","message":"Payment required. See X402-Challenge header."}
```

The decoded `X402-Challenge` contains:

```json
{
  "v": "1",
  "scheme": "bsv-tx-v1",
  "amount_sats": 100,
  "payee_locking_script_hex": "76a914...88ac",
  "expires_at": 1710360000,
  "domain": "localhost:8402",
  "method": "GET",
  "path": "/v1/expensive",
  "query": "",
  "req_headers_sha256": "e3b0c442...",
  "req_body_sha256": "e3b0c442...",
  "nonce_utxo": {
    "txid": "abcd1234...",
    "vout": 0,
    "satoshis": 1,
    "locking_script_hex": "76a914...88ac"
  },
  "template": null,
  "require_mempool_accept": true,
  "confirmations_required": 0
}
```

### Step 3 — Client builds TX, delegates fees, broadcasts, retries with proof

```http
GET /v1/expensive HTTP/1.1
X402-Proof: {"v":"1","scheme":"bsv-tx-v1","txid":"...","rawtx_b64":"...","challenge_sha256":"...","request":{...}}
```

### Step 4 — Gateway verifies and responds

```http
HTTP/1.1 200 OK
X402-Receipt: <hex-hash>
X402-Receipt-Time: 2026-03-13T10:00:00Z
X402-Status: accepted
```

## Error Codes

| Status | Code | Meaning |
|--------|------|---------|
| 400 | `invalid_proof` | Malformed proof or missing fields |
| 400 | `challenge_not_found` | Challenge hash not in cache |
| 402 | `expired_challenge` | Challenge TTL exceeded |
| 402 | `insufficient_amount` | Payment below required amount |
| 403 | `invalid_binding` | Request fields don't match challenge |
| 403 | `invalid_payee` | Payment output doesn't match payee |
| 409 | `double_spend` | Nonce UTXO already spent |
| 503 | `no_utxos_available` | Nonce pool exhausted |

## Fee Delegation

### POST /delegate/x402 (Binary)

Send a raw partial transaction. The delegator adds fee inputs, signs them, and returns the completed transaction.

### POST /api/v1/tx (JSON)

Request:
```json
{
  "txJson": {
    "inputs": [
      {"txid": "abcd...", "vout": 0, "satoshis": 1, "scriptSig": ""}
    ],
    "outputs": [
      {"satoshis": 100, "script": "76a914...88ac"}
    ]
  }
}
```

Response (200):
```json
{
  "success": true,
  "txid": "abcd...",
  "rawtx": "0100000001...",
  "fee": 1,
  "mode": "raw_transaction_returned"
}
```

## Dashboard & Operations

### GET /api/v1/config

Returns full server configuration including network, broadcaster, pool settings, key mode, profile, and addresses.

### PUT /api/v1/config

Update runtime configuration. All fields optional:
```json
{
  "feeRate": 1.0,
  "poolReplenishThreshold": 200,
  "poolOptimalSize": 2000,
  "broadcaster": "composite"
}
```

### GET /api/v1/stats/summary

Returns aggregate statistics: total requests, payments, challenges, errors, average duration, fee totals, uptime, and pool stats.

### GET /api/v1/revenue

Returns revenue statistics: payment count, total sats, last txid, unswept count and amount.

### POST /api/v1/treasury/fanout

Split a funding UTXO into pool-sized UTXOs:
```json
{
  "pool": "nonce",
  "count": 100,
  "fundingTxid": "abcd...",
  "fundingVout": 0,
  "fundingScript": "76a914...88ac",
  "fundingSatoshis": 500000,
  "signingKey": "treasury"
}
```

### POST /api/v1/treasury/sweep

Consolidate UTXOs back to the treasury address.

### POST /api/v1/pools/reconcile

Detect and remove zombie UTXOs that are spent on-chain but still listed as available in the pool.

### GET /health

Returns server status, version, network, profile, and pool statistics.

### GET /api/v1/events/stream

Server-Sent Events stream for real-time operational monitoring. Events include challenges issued, proofs verified, settlements recorded, and pool state changes.
