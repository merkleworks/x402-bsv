# Deployment

## Demo Mode (Local Development)

```bash
make demo
```

Starts with in-memory pools, mock broadcaster, and auto-seeded UTXOs. No external dependencies required.

## Docker Compose (Recommended)

```bash
# Generate keys and .env
make setup

# Start gateway + delegator + Redis
docker compose up -d --build
```

Services started:

| Service | Port | Description |
|---------|------|-------------|
| Redis | 6379 | Operational store for UTXO pool indexing |
| x402-gateway | 8402 | Main server with 402 middleware and dashboard |
| x402-delegator | 8403 | Fee delegation service |

Environment variables are loaded from `.env`. Redis is configured with health checks and the gateway waits for Redis readiness before starting.

## Manual Docker Build

```bash
docker build -t x402-gateway .
docker build -f Dockerfile.delegator -t x402-delegator .
```

## Manual Build (No Docker)

```bash
# Build all binaries (includes dashboard)
make build

# Start the server
make run
```

## Production Checklist

| Item | Detail |
|------|--------|
| **Key generation** | Run `make setup` to generate an HD wallet (xpriv). Store securely. |
| **Network** | Set `BSV_NETWORK=mainnet` and `BROADCASTER=composite` (GorillaPool ARC + WhatsOnChain fallback) |
| **Redis** | Set `REDIS_ENABLED=true`. Required for pool persistence across restarts. |
| **Pool seeding** | Fund the treasury address, then POST to `/api/v1/treasury/fanout` for nonce and fee pools |
| **Fee budget** | Set `DAILY_FEE_BUDGET` to limit runaway fee spending under load |
| **TLS** | Terminate TLS at a reverse proxy (nginx, Caddy, ALB). The gateway serves plain HTTP. |
| **Monitoring** | Dashboard at `/`, SSE event stream at `/api/v1/events/stream`, health at `/health` |
| **Backups** | Back up `.env` (contains xpriv). Redis data is operational — pools can be re-seeded from treasury. |

## Security Considerations

### Replay Protection
- Nonce UTXOs are single-use at the network consensus layer
- A spent nonce UTXO cannot be included in another valid transaction
- The in-memory replay cache provides an operational fast-path but is not a correctness dependency

### Request Binding
- Proofs are bound to the specific request that triggered the challenge
- Binding includes: method, path, domain, query, headers, body
- Prevents proof reuse across different endpoints or request shapes

### Key Management
- HD wallet recommended for production (separate derivation paths for nonce, fee, and treasury)
- Delegator holds only the fee key — never the client's keys
- Never expose private keys in logs or error messages

### Fee Budget
- Set `DAILY_FEE_BUDGET` in satoshis to cap daily fee spending
- Prevents runaway costs under high load

## Make Targets

| Target | Description |
|--------|-------------|
| `make build` | Build all binaries (includes dashboard) |
| `make test` | Run Go tests |
| `make lint` | Run `go vet` |
| `make run` | Start server |
| `make demo` | Start in demo mode (auto-seeds pools) |
| `make client` | Run test client |
| `make setup` | Interactive setup wizard |
| `make deploy` | Docker Compose deployment |
| `make dashboard-dev` | Dashboard dev server (hot reload) |
| `make dashboard-build` | Build dashboard for production |
| `make clean` | Clean build artifacts |
