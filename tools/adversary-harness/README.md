# x402 Adversary Harness

Standalone adversarial test tool that simulates hostile network and miner behaviour against a running x402 gateway. It validates the robustness of the replay cache, nonce reservation, delegation endpoint, mempool gating, fee pool behaviour, lease TTL reclaim, and crash recovery — without modifying any gateway code.

## Scenarios

### 1. Miner Ordering Variance (`-adversary=mining`)

Simulates competing transactions with different fee levels racing to spend the same nonce UTXO.

**Attack model:**
- Acquire a challenge (nonce UTXO allocated)
- Fire 3 concurrent delegation requests with the same nonce (txA, txB, txC)
- Only one should win the atomic `TryReserve` race
- Submit a fake "competing" proof with a different txid

**Protocol invariants validated:**
- `TryReserve` is atomic — only one concurrent reservation wins
- Losers get 409 (double_spend) or 202 (nonce_pending)
- Proof with a non-committed txid is rejected
- Replay cache blocks duplicate nonce usage

**Metrics:** `miner_order_tests`, `double_spend_detected`, `nonce_pending`, `fake_proof_rejected`

### 2. Mempool Suppression (`-adversary=mempool`)

Simulates mempool visibility failures where transaction broadcast status doesn't match reality.

**Attack model:**
- **Case A (false negative):** Submit proof with plausible but non-existent txid — gateway's mempool check should fail
- **Case B (false positive):** Submit completely fabricated proof with wrong challenge hash, method, path — binding check should fail
- **Case C (delayed propagation):** Submit proof immediately, wait 0.5-2.5s, retry — tests retry semantics

**Protocol invariants validated:**
- Gateway rejects proofs with unknown txids
- Challenge binding (domain, method, path, body hash) is enforced
- Invalid rawtx deserialization is caught
- Expired challenges are rejected after TTL

**Metrics:** `mempool_false_negative_tests`, `false_negative_rejected`, `false_positive_rejected`, `delayed_propagation_tests`

### 3. Delayed Broadcast (`-adversary=broadcast`)

Simulates scenarios where the delegator returns a signed transaction but broadcast is delayed or never happens.

**Attack model:**
- **Case A (immediate):** Delegate + submit proof immediately
- **Case B (delayed):** Delegate + wait 5-30s + submit proof (may hit challenge TTL)
- **Case C (never):** Delegate but never submit proof (orphaned transaction)

**Protocol invariants validated:**
- Challenges expire after 5-minute TTL
- Expired proofs are correctly rejected (402 vs 200)
- Orphaned delegations don't permanently leak pool resources
- Pool reclaim loop recovers leaked leases

**Metrics:** `broadcast_immediate_success`, `broadcast_delayed_expired`, `orphaned_tx`, `proof_retry_success`

### 4. Fee Delegation Abuse (`-adversary=abuse`)

Hammers the delegation and challenge endpoints with 4 attack phases.

**Phase 1 — Challenge flood:** N concurrent workers acquire challenges as fast as possible, draining the nonce pool.

**Phase 2 — Concurrent same-nonce (RACE-01):** Get one challenge, fire 50 concurrent delegation requests with the same nonce. The RACE-01 fix (`TryReserve/Commit/Release`) must ensure exactly one wins.

**Phase 3 — Duplicate delegation replay:** Delegate once, then try the same nonce again. Second attempt must be rejected (409 or 202).

**Phase 4 — Mid-flight cancellation:** Start delegation, cancel the HTTP request mid-flight. The `defer Release()` unwind must free the reserved nonce.

**Protocol invariants validated:**
- Pool exhaustion returns proper error (503 or 402 with no nonces)
- RACE-01 atomic reservation prevents concurrent bypass
- Replay cache blocks duplicate nonces after commit
- Cancelled requests release reserved nonces via defer

**Metrics:** `nonce_pending`, `replay_cache_conflicts`, `nonce_pool_exhausted`, `delegation_cancelled`

### 5. Nonce Lease TTL Reclaim (`-adversary=ttl`)

Verifies that nonce leases are correctly reclaimed after lease TTL expiration and that a malicious client cannot permanently exhaust the nonce pool.

**Attack model:**
- Adversary rapidly acquires challenges to lease all nonce UTXOs. Each `GET /v1/expensive` that returns 402 causes the gateway to lease one nonce from the pool.
- Adversary deliberately does NOT complete delegation or payment — every nonce stays in LEASED state indefinitely.
- Once all nonces are leased the pool is exhausted: no new challenges can include a nonce, blocking all payments.
- After the lease TTL expires (default 300s / 5 minutes), the reclaim loop (runs every 30s) detects expired leases and returns them to AVAILABLE.

**Test phases:**
1. Snapshot initial pool state via `GET /health`
2. Rapid challenge acquisition from 10 concurrent workers until `nonce_available = 0`
3. Wait for `lease_ttl + 40s` (TTL + reclaim interval + margin)
4. Poll `GET /health` every 2 seconds watching for recovery
5. Verify pool returns to >= 80% of initial capacity
6. Confirm new challenges can be acquired after reclaim

**Protocol invariants validated:**
- Pool reports exhaustion correctly (`available = 0`)
- Reclaim loop recovers all expired leases after TTL
- Pool returns to near-initial capacity after recovery
- New challenges can be acquired after reclaim completes
- A malicious client cannot permanently deny service

**Metrics:** `lease_reclaim_tests`, `nonce_pool_exhausted`, `nonce_recovered`, `reclaim_success`, `reclaim_duration_ms`

**Failure condition:** `CRITICAL_reclaim_failed` — nonce pool remains permanently exhausted after TTL + buffer.

### 6. Gateway Crash / Restart Recovery (`-adversary=crash`)

Verifies that volatile replay cache behaviour does not cause permanent payment denial after a gateway process restart, and that duplicate proof protection is re-established once the process comes back online.

**Attack model:**
1. Client acquires a challenge and builds a valid proof via `/demo/build-proof`
2. Before submitting, the gateway process is killed and restarted (simulating a crash)
3. Client submits the original proof after restart — should be accepted
4. Client submits the same proof again — should be rejected (duplicate)

**Why the replay cache is intentionally volatile:**

The replay cache lives in-memory so that a process restart clears stale entries. On-chain finality (not an in-process cache) is the ultimate double-spend arbiter. The cache is a performance optimisation that prevents obvious replays within a single process lifetime. After restart:
- Previously seen txids can be re-submitted — the gateway re-validates them via mempool/confirmation checks
- The first successful proof re-populates the cache; subsequent duplicates are rejected as before
- Pool state survives the restart because it is backed by Redis

**Protocol invariants validated:**
- Valid proof accepted after restart (no permanent denial of service)
- Duplicate proof rejected after restart (replay protection re-established)
- Pool state survives restart (Redis-backed persistence)
- Gateway recovers to healthy state within 120 seconds

**Metrics:** `crash_tests`, `proof_after_restart_success`, `proof_after_restart_failure`, `duplicate_proof_rejected`, `replay_after_restart`

**Failure conditions:**
- `CRITICAL_gateway_not_recovered` — gateway did not return to healthy state after restart
- `CRITICAL_duplicate_after_restart_accepted` — duplicate proof accepted (replay protection failure)

## How to Run

### Prerequisites

1. Gateway running at `http://localhost:8402`
2. For best results, run in **demo mode** (`BROADCASTER=mock`) so pools are auto-seeded
3. For the crash scenario, the gateway must be running in Docker (or provide a custom restart command)

### Start gateway in demo mode

```bash
# Docker
docker compose run -d --service-ports -e BROADCASTER=mock --name x402-test x402-gateway

# Local
BROADCASTER=mock go run ./cmd/server
```

### Run the original stress-test scenarios

```bash
go run ./tools/adversary-harness -adversary=mining -clients=20 -duration=30s
go run ./tools/adversary-harness -adversary=mempool -clients=10 -duration=30s
go run ./tools/adversary-harness -adversary=broadcast -clients=5 -duration=60s
go run ./tools/adversary-harness -adversary=abuse -clients=50 -duration=30s
```

### Run the TTL reclaim scenario

```bash
# With default 300s lease TTL (takes ~6 minutes)
go run ./tools/adversary-harness -adversary=ttl

# With a short TTL for faster testing (set LEASE_TTL=30 on the gateway too)
go run ./tools/adversary-harness -adversary=ttl -lease-ttl=30s
```

### Run the crash recovery scenario

```bash
# Docker (default restart command)
go run ./tools/adversary-harness -adversary=crash

# Custom container name
go run ./tools/adversary-harness -adversary=crash \
  -restart-cmd="docker restart x402-test"

# Local process (requires manual restart script)
go run ./tools/adversary-harness -adversary=crash \
  -restart-cmd="/path/to/restart-gateway.sh"
```

### Run all scenarios

```bash
# All 6 scenarios (takes 6+ minutes due to TTL wait + gateway restart)
go run ./tools/adversary-harness -adversary=all -clients=10 -duration=60s

# All with short lease TTL for faster iteration
go run ./tools/adversary-harness -adversary=all -clients=10 -duration=60s -lease-ttl=30s
```

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-adversary` | `all` | Scenario: `mining`, `mempool`, `broadcast`, `abuse`, `ttl`, `crash`, `all` |
| `-clients` | `10` | Concurrent workers per timed scenario |
| `-duration` | `30s` | Total duration for timed scenarios (split equally across them) |
| `-url` | `http://localhost:8402` | Gateway base URL |
| `-verbose` | `false` | Enable debug logging |
| `-lease-ttl` | `300s` | Expected nonce lease TTL (must match gateway `LEASE_TTL` env var) |
| `-restart-cmd` | `docker restart x402-gateway-x402-gateway-1` | Command to restart gateway for crash scenario |

**Note:** The `-duration` flag only governs the four timed scenarios (mining, mempool, broadcast, abuse). The TTL scenario runs for `lease-ttl + ~2 minutes` regardless. The crash scenario is a single-pass test that completes in ~30-120 seconds.

## Expected Behaviour

### PASS

```
RESULT: PASS (all protocol invariants held)
```

All adversarial attacks were correctly rejected. No fake proofs were accepted, no duplicate delegations succeeded, no resource leaks occurred, nonces were reclaimed after TTL, and the gateway recovered correctly after restart.

### FAIL

```
⚠️  CRITICAL FAILURE: CRITICAL_fake_proof_accepted = 3
RESULT: FAIL (1 critical vulnerabilities detected)
```

Any metric prefixed with `CRITICAL_` indicates a protocol invariant violation:

| Critical Metric | Meaning |
|----------------|---------|
| `CRITICAL_fake_proof_accepted` | Gateway accepted a proof with a fabricated txid |
| `CRITICAL_false_negative_accepted` | Mempool gating bypassed with non-existent tx |
| `CRITICAL_false_positive_accepted` | Fabricated proof with wrong binding accepted |
| `CRITICAL_duplicate_accepted` | Same nonce used twice for delegation |
| `CRITICAL_delayed_accepted_*` | Fake proof accepted on immediate or retry |
| `CRITICAL_reclaim_failed` | Nonce pool did not recover after lease TTL expiry |
| `CRITICAL_post_reclaim_challenge_failed` | Cannot acquire challenges after reclaim |
| `CRITICAL_gateway_not_recovered` | Gateway did not recover after restart |
| `CRITICAL_duplicate_after_restart_accepted` | Duplicate proof accepted after restart |

## What Each Attack Validates

| Attack | Validates |
|--------|-----------|
| Competing delegations (same nonce) | `TryReserve` atomicity (RACE-01 fix) |
| Fake txid in proof | Mempool gating, challenge cache binding |
| Wrong method/path in proof | Request binding verification |
| Expired challenge submission | TTL enforcement |
| Nonce pool flood | Graceful degradation under exhaustion |
| Duplicate delegation | Replay cache `Check()` + `Record()` |
| Mid-flight cancellation | `defer Release()` unwind path |
| Orphaned transaction | Pool reclaim loop recovery |
| Nonce lease exhaustion + TTL wait | Reclaim loop expires leases after TTL |
| Gateway crash + proof after restart | Volatile replay cache resilience |
| Duplicate proof after restart | Replay re-establishment post-crash |

## Metrics Output

### Timed scenarios (example at 30s)

```
╔══════════════════════════════════════════════╗
║  ADVERSARY HARNESS STATUS  (30s elapsed)
╠══════════════════════════════════════════════╣
║  challenges_requested                    100  ║
║  delegation_rejected_400                 300  ║
║  double_spend_detected                    50  ║
║  miner_order_tests                       150  ║
║  nonce_pending                            25  ║
╠══════════════════════════════════════════════╣
║  avg_delegation                          1ms  ║
╚══════════════════════════════════════════════╝
```

### TTL reclaim scenario (example)

```
╔══════════════════════════════════════════════╗
║  ADVERSARY HARNESS STATUS  (342s elapsed)
╠══════════════════════════════════════════════╣
║  challenges_requested                    100  ║
║  lease_reclaim_tests                       1  ║
║  nonce_initial_available                 100  ║
║  nonce_final_available                    98  ║
║  nonce_pool_exhausted                      1  ║
║  nonce_recovered                           1  ║
║  post_reclaim_challenge_success            1  ║
║  reclaim_duration_ms                  312450  ║
║  reclaim_success                           1  ║
╚══════════════════════════════════════════════╝
```

### Crash recovery scenario (example)

```
╔══════════════════════════════════════════════╗
║  ADVERSARY HARNESS STATUS  (45s elapsed)
╠══════════════════════════════════════════════╣
║  challenges_requested                      1  ║
║  crash_gateway_recovered                   1  ║
║  crash_tests                               1  ║
║  duplicate_proof_rejected                  1  ║
║  proof_after_restart_success               1  ║
║  replay_after_restart                      1  ║
╚══════════════════════════════════════════════╝
```

## Architecture

```
tools/adversary-harness/
├── main.go              # CLI entrypoint, scenario orchestration
├── client/
│   └── gateway_client.go  # HTTP client for all gateway endpoints
├── metrics/
│   └── metrics.go         # Thread-safe atomic counters + reporter
├── scenarios/
│   ├── miner_ordering.go      # Scenario 1: competing transactions
│   ├── mempool_suppression.go # Scenario 2: visibility failures
│   ├── delayed_broadcast.go   # Scenario 3: timing attacks
│   ├── fee_abuse.go           # Scenario 4: concurrent hammering
│   ├── lease_reclaim.go       # Scenario 5: nonce lease TTL reclaim
│   └── crash_recovery.go     # Scenario 6: gateway crash/restart
└── README.md
```

The harness is a **black-box** testing tool — it only uses the gateway's public HTTP API. No gateway code is modified, no Redis is accessed directly, and all adversarial behaviour is simulated client-side.

### Scenario Timing Model

The six scenarios fall into two categories:

| Category | Scenarios | Duration control |
|----------|-----------|-----------------|
| **Timed** | mining, mempool, broadcast, abuse | `-duration` flag (split equally) |
| **Self-paced** | ttl, crash | Internal timing (TTL wait / restart cycle) |

When running `-adversary=all`, timed scenarios execute first with the duration budget, then TTL and crash run sequentially. The crash scenario always runs last because it restarts the gateway process.
