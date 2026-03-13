# Troubleshooting

## "No UTXOs available (pool exhausted)"

- UTXO pools need seeding with 1-satoshi UTXOs
- In demo mode with mock broadcaster, pools auto-seed on startup
- For production: fund the treasury address, then POST to `/api/v1/treasury/fanout`
- Use `/api/v1/pools/reconcile` to detect and remove zombie UTXOs that are spent on-chain but still listed as available

## "SSE not supported" / Dashboard disconnected

- The logging middleware must implement `http.Flusher`
- Fixed in recent versions — update to the latest code

## Redis connection failed

- Ensure Redis is running: `docker compose up redis -d`
- Check `REDIS_URL` format: `redis://host:port`
- In Docker: the gateway uses `redis://redis:6379` (Docker DNS). Ensure both services are on the same Docker network
- For local development without Redis: set `REDIS_ENABLED=false` (pools use in-memory storage, no persistence across restarts)

## Broadcaster errors / "dial tcp: lookup ... no such host"

- The `woc` and `composite` broadcasters require internet access
- For offline development: set `BROADCASTER=mock`
- For `composite` mode: GorillaPool ARC is primary, WhatsOnChain is fallback. If ARC is down, the circuit breaker opens automatically and routes through WoC
- Check broadcaster health: GET `/api/v1/health/broadcasters`

## Port conflicts

- Default ports: 8402 (gateway), 8403 (delegator), 6379 (Redis)
- Override in `.env`: set `PORT` and `DELEGATOR_PORT`
- Docker Compose maps these automatically from `.env`

## Transaction rejected by mempool

- `409 double_spend`: the nonce UTXO was already spent — likely a retry of an already-settled request
- `402 expired_challenge`: the challenge TTL (default 5 min) has passed — request a new challenge
- `402 insufficient_amount`: the payment output is less than the challenged amount

## Setup issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| `bind: address already in use` | Port 8402 is occupied | Set `PORT=8403` in `.env` or stop the conflicting process |
| `REDIS_URL: connection refused` | Redis not running | Start Redis (`docker compose up redis -d`) or set `REDIS_ENABLED=false` |
| `No UTXOs available` on first request | Pools not yet seeded | Use `make demo` (auto-seeds) or POST to `/api/v1/treasury/fanout` |
| `broadcaster: dial tcp: lookup ...` | No network access with `woc` or `composite` | Use `BROADCASTER=mock` for offline development |
| `go: module requires go >= 1.25.0` | Go version too old | Install Go 1.25+ from https://go.dev/dl/ |
