# x402 Gateway — Postman Testing Guide

## Overview

The Postman collection (`postman/x402-gateway.postman_collection.json`) provides a complete test suite for the x402 payment gateway. It covers all endpoints, the full payment flow, replay protection, and error cases.

## x402 Payment Flow

The x402 protocol uses HTTP 402 Payment Required to gate access to resources. The flow is:

```
Client                         Gateway (Gatekeeper)            Delegator
  |                                |                              |
  |--- GET /v1/expensive --------->|                              |
  |<-- 402 + X402-Challenge -------|                              |
  |                                |                              |
  |  [decode challenge, build partial tx, sign with 0xC1]         |
  |                                |                              |
  |--- POST /delegate/x402 ------>|------- Accept(req) --------->|
  |<-- {txid, rawtx_hex} ---------|<------ {txid, rawtx} --------|
  |                                |                              |
  |  [build proof from txid + rawtx]                              |
  |                                |                              |
  |--- GET /v1/expensive --------->|                              |
  |    X402-Proof: <base64>        |                              |
  |<-- 200 + protected data -------|                              |
  |                                |                              |
  |--- GET /v1/expensive --------->|                              |
  |    X402-Proof: <same proof>    |                              |
  |<-- 409 double_spend -----------|  [replay cache rejects]      |
```

In **demo mode** (`BROADCASTER=mock`), the `POST /demo/build-proof` endpoint handles steps 2-4 server-side, so you can test the full flow without BSV client-side crypto.

## Prerequisites

### 1. Start the Gateway

**Docker (recommended):**
```bash
cd x402-gateway
docker compose up -d --build
```

**Local (demo mode):**
```bash
set -a && source .env && set +a
BROADCASTER=mock go run ./cmd/server
```

**Local (live mode, requires funded wallets):**
```bash
set -a && source .env && set +a
go run ./cmd/server
```

### 2. Import the Collection

1. Open Postman
2. Click **Import** (top-left)
3. Drag and drop `postman/x402-gateway.postman_collection.json`
4. The collection "x402 Payment Gateway" appears in your sidebar

### 3. Verify Base URL

The collection variable `baseUrl` defaults to `http://localhost:8402`. Change it if your server runs on a different port:

- Click the collection name in the sidebar
- Go to the **Variables** tab
- Update `baseUrl` if needed

## Running the Full Demo Flow

The **"Full Demo Flow (Run in Sequence)"** folder executes the complete payment cycle:

| Step | Request | Expected |
|------|---------|----------|
| 1 | `GET /health` | 200 — server is up |
| 2 | `GET /v1/expensive` | 402 — challenge captured |
| 3 | `POST /demo/build-proof` | 200 — proof built |
| 4 | `GET /v1/expensive` + `X402-Proof` | 200 — payment accepted |
| 5 | `GET /v1/expensive` + same proof | 409 — replay blocked |
| 6 | `GET /health` | 200 — pool consumption verified |

### Using Collection Runner

1. Click the **"Full Demo Flow"** folder
2. Click **Run** (top-right)
3. Ensure requests are in order (1-6)
4. Click **Run x402 Payment Gateway**
5. All 6 requests should pass (green)

### Manual Step-by-Step

You can also run each request individually:

1. Send **Step 1** — verify 200
2. Send **Step 2** — verify 402, check Console for challenge details
3. Send **Step 3** — verify 200, check Console for TxID
4. Send **Step 4** — verify 200, you've accessed paid content
5. Send **Step 5** — verify 409 (same proof rejected)
6. Send **Step 6** — check Console for pool consumption

## Expected Responses

### 402 Payment Required (Challenge)

```
HTTP/1.1 402 Payment Required
Cache-Control: no-store
X402-Accept: bsv-tx-v1
X402-Challenge: eyJ2IjoiMSIsInNjaGVtZSI6ImJzdi10eC12MSIs...

{"code":"payment_required","message":"Payment required. See X402-Challenge header.","status":402}
```

The `X402-Challenge` header is a base64-encoded JSON object containing:
- `v`: protocol version ("1")
- `scheme`: payment scheme ("bsv-tx-v1")
- `amount_sats`: price in satoshis (100)
- `payee_locking_script_hex`: payee's P2PKH locking script
- `expires_at`: Unix timestamp
- `nonce_utxo`: `{txid, vout, satoshis, locking_script_hex}` for replay protection
- `domain`, `method`, `path`, `query`: request binding
- `req_headers_sha256`, `req_body_sha256`: request content hashes

### 200 OK (Payment Accepted)

```json
{
    "data": "This response cost 100 satoshis via x402",
    "timestamp": 1772635000,
    "path": "/v1/expensive"
}
```

### 409 Conflict (Replay Detected)

```json
{
    "code": "double_spend",
    "message": "nonce already used in transaction <txid>",
    "status": 409
}
```

### 202 Accepted (Nonce Pending — RACE-01 Fix)

When concurrent requests attempt to use the same nonce, the atomic reservation returns:

```json
{
    "code": "nonce_pending",
    "message": "nonce is being processed by another request; retry shortly",
    "status": 202
}
```

This is the RACE-01 fix in action — the `TryReserve/Commit/Release` state machine prevents race conditions.

## Replay Protection Test

The collection includes a dedicated replay test (Step 5 in the Full Demo Flow):

1. **Step 4** submits a valid proof → 200 OK. The gatekeeper records the nonce outpoint in the replay cache via `Record()`.
2. **Step 5** resubmits the exact same proof → 409 Conflict. The gatekeeper's `Check()` finds the nonce outpoint already recorded with a committed `spendTxID`.

This verifies:
- The replay cache correctly stores nonce→txid mappings
- Duplicate proofs are rejected at the gatekeeper layer
- The nonce UTXO cannot be reused (Bitcoin-enforced single-use)

## Concurrency Testing

To test the RACE-01 atomic nonce reservation:

### Using Postman (Manual)

1. Run **Step 2** to get a challenge
2. Open two Postman tabs with **Step 3** (Build Proof)
3. Click Send on both simultaneously
4. One should return 200, the other should return 500 (delegation failed) or 202 (nonce_pending)

### Using curl (Parallel)

```bash
# Get a challenge
CHALLENGE=$(curl -s -D - http://localhost:8402/v1/expensive 2>&1 | grep X402-Challenge | cut -d' ' -f2 | tr -d '\r')

# Fire two concurrent proof-build requests
curl -s -X POST http://localhost:8402/demo/build-proof \
  -H "Content-Type: application/json" \
  -d "{\"challenge\": \"$CHALLENGE\"}" &

curl -s -X POST http://localhost:8402/demo/build-proof \
  -H "Content-Type: application/json" \
  -d "{\"challenge\": \"$CHALLENGE\"}" &

wait
```

Only one request will succeed. The other will fail because:
- `TryReserve()` atomically claims the nonce under an exclusive lock
- The second request sees the nonce as pending → returns 202 or delegation error

### Using Newman + Parallel Iterations

```bash
npm install -g newman
newman run postman/x402-gateway.postman_collection.json \
  --folder "Full Demo Flow (Run in Sequence)" \
  --iteration-count 5
```

Each iteration gets a fresh challenge and runs the full cycle.

## Endpoint Reference

### Health & Monitoring

| Method | Path | Description |
|--------|------|-------------|
| GET | `/health` | Server health, version, all pool stats |
| GET | `/api/health` | Node.js-compatible health (uptime, timestamp) |
| GET | `/api/utxo/stats` | Fee pool stats with Redis status |
| GET | `/api/utxo/health` | Pool health (healthy/degraded) |

### x402 Protocol

| Method | Path | Description |
|--------|------|-------------|
| GET/POST | `/v1/expensive` | Protected resource (402 challenge / 200 with proof) |
| POST | `/delegate/x402` | Fee delegation (client partial tx → completed tx) |

### Fee Delegator (Node.js-Compatible)

| Method | Path | Description |
|--------|------|-------------|
| POST | `/api/v1/tx` | Fee delegation with JSON tx structure |

### Dashboard API

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/config` | Get server configuration |
| PUT | `/api/v1/config` | Update runtime configuration |
| GET | `/api/v1/stats/summary` | 1-hour aggregate stats |
| GET | `/api/v1/stats/timeseries` | 1-minute bucket timeseries |
| GET | `/api/v1/treasury/info` | Treasury address and pool info |
| GET | `/api/v1/treasury/utxos` | Treasury UTXO list |
| POST | `/api/v1/treasury/fanout` | Split funding UTXO into pool UTXOs |
| GET | `/api/v1/treasury/history` | Fan-out operation history |

### Events

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/events/stream` | SSE event stream |
| GET | `/demo/events` | SSE stream (backward-compatible) |

### Demo Mode (BROADCASTER=mock only)

| Method | Path | Description |
|--------|------|-------------|
| GET | `/demo/info` | Demo mode info (version, pools, price) |
| POST | `/demo/build-proof` | Server-side proof builder |

## Troubleshooting

### "Could not get response" / Connection refused
- Is the server running? Check `docker ps` or your terminal.
- Is the port correct? Default is 8402. Check `baseUrl` collection variable.

### Step 3 returns 500 "delegation failed"
- The server must be in **demo mode** (`BROADCASTER=mock`) for `/demo/build-proof` to work.
- Check pool availability: `GET /health` — are nonce, fee, and payment pools non-zero?
- In live mode, pools must be funded with real BSV UTXOs via treasury fan-out.

### Step 4 returns 402 instead of 200
- The proof may have expired (challenges have a 5-minute TTL).
- Run steps 2-4 within 5 minutes.
- Check that `proofHeader` collection variable is populated (check Variables tab).

### Step 5 returns 402 instead of 409
- The replay cache entry may have expired (10-minute TTL).
- The proof header may not match (Postman variable issue).
- Run steps 2-5 in quick succession.

### Redis connection errors
- For Docker: Redis starts automatically via `docker compose`.
- For local: set `REDIS_ENABLED=false` in `.env` for in-memory mode, or start Redis locally on port 6379.

### "no UTXOs available" (503)
- All pool UTXOs have been consumed. In demo mode, restart the server to re-seed.
- In live mode, use the treasury fan-out endpoint to replenish pools.

### Challenge decode errors in test scripts
- The `X402-Challenge` header is standard base64 (not base64url in practice). The test scripts handle both formats.
- If you see decode errors, check that `challengeEncoded` is populated by running Step 2 first.
