# Configuration

All configuration is via environment variables, loaded from a `.env` file. Generate one with `make setup` or `go run ./cmd/keygen`.

The annotated template is in [`.env.example`](../.env.example) at the repository root.

## Key Management

Choose one:

| Variable | Description |
|----------|-------------|
| `XPRIV` | BIP32 extended private key (recommended). Derives separate keys for nonce, fee, and treasury pools. Generate via `make setup`. |
| `BSV_PRIVATE_KEY` | Single WIF key (legacy). Same key used for all pools. |

## Network

| Variable | Default | Description |
|----------|---------|-------------|
| `BSV_NETWORK` | `testnet` | `testnet` or `mainnet` |
| `BROADCASTER` | *(required)* | `mock` (demo/offline), `woc` (WhatsOnChain only), or `composite` (GorillaPool ARC primary + WoC fallback) |
| `ARC_URL` | `https://arc.gorillapool.io/v1` | GorillaPool ARC endpoint (composite mode) |
| `ARC_API_KEY` | *(empty)* | Optional ARC API key |
| `WOC_API_URL` | *(auto-derived)* | WhatsOnChain API base URL. Override if WoC is unreachable from your server. |

## Payments

| Variable | Default | Description |
|----------|---------|-------------|
| `PAYEE_ADDRESS` | *(nonce pool address)* | Where settlement payments are sent |
| `FEE_RATE` | *(required)* | Fee rate in sat/byte (BSV standard: `0.001`) |
| `DAILY_FEE_BUDGET` | `0` | Daily fee spending limit in satoshis. `0` = unlimited. |
| `FEE_UTXO_SATS` | `100` | Fee pool UTXO denomination (1-1000 sats) |

## Server

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8402` | HTTP port |
| `NONCE_POOL_SIZE` | `100` | Initial nonce pool size |
| `NONCE_LEASE_TTL` | `300` | Nonce lease timeout in seconds |

## Storage

| Variable | Default | Description |
|----------|---------|-------------|
| `REDIS_ENABLED` | `false` | Enable Redis for persistent pool storage |
| `REDIS_URL` | `redis://localhost:6379` | Redis connection URL |

When Redis is disabled, pools are in-memory and reset on restart. Redis is an operational aid for persistence — not a correctness dependency.

## Pool Auto-Refill

| Variable | Default | Description |
|----------|---------|-------------|
| `POOL_REPLENISH_THRESHOLD` | `500` | Trigger refill when available UTXOs drop below this count |
| `POOL_OPTIMAL_SIZE` | `5000` | Target pool size after refill |

## Profile B (Template Mode)

| Variable | Default | Description |
|----------|---------|-------------|
| `TEMPLATE_MODE` | `false` | Enable Profile B (Gateway Template) |
| `TEMPLATE_PRICE_SATS` | `10` | Payment amount locked in templates |

## Delegator

| Variable | Default | Description |
|----------|---------|-------------|
| `DELEGATOR_PORT` | `8403` | Delegator HTTP port (standalone mode) |
| `DELEGATOR_EMBEDDED` | `false` | Run delegator within the gateway process |
| `DELEGATOR_URL` | `http://localhost:8403` | Delegator URL (when running separately) |
