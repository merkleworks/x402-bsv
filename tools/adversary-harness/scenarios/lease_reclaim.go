// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package scenarios

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
)

// LeaseReclaim verifies that nonce leases are correctly reclaimed after
// lease TTL expiration and that a malicious client cannot permanently
// exhaust the nonce pool.
//
// Attack model:
//   - Adversary rapidly acquires challenges to lease all nonce UTXOs.
//     Each GET /v1/expensive that returns 402 causes the gateway to
//     lease one nonce UTXO from the pool.
//   - Adversary deliberately does NOT complete delegation or payment,
//     so every nonce stays in LEASED state indefinitely.
//   - Once all nonces are leased the pool is exhausted: no new
//     challenges can include a nonce, blocking all payments.
//   - After the lease TTL expires, the reclaim loop (runs every 30s)
//     should detect expired leases and return them to AVAILABLE.
//
// Protocol invariants tested:
//   - Pool reports exhaustion correctly (available = 0)
//   - Reclaim loop recovers all expired leases after TTL
//   - Pool returns to near-initial capacity after recovery
//   - New challenges can be acquired after reclaim completes
//   - A malicious client cannot permanently deny service
func LeaseReclaim(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, leaseTTL time.Duration, logger *slog.Logger) {
	logger.Info("starting nonce lease TTL reclaim scenario", "lease_ttl", leaseTTL)
	m.Inc("lease_reclaim_tests")

	// ─── Phase 0: Snapshot initial pool state ───────────────────────

	healthBefore, err := gw.Health()
	if err != nil {
		logger.Error("cannot query initial health", "error", err)
		m.Inc("lease_reclaim_errors")
		return
	}

	initialAvailable := healthBefore.NoncePool.Available
	initialTotal := healthBefore.NoncePool.Total
	m.Add("nonce_initial_available", int64(initialAvailable))
	m.Add("nonce_initial_total", int64(initialTotal))

	logger.Info("initial pool state",
		"nonce_available", initialAvailable,
		"nonce_leased", healthBefore.NoncePool.Leased,
		"nonce_spent", healthBefore.NoncePool.Spent,
		"nonce_total", initialTotal,
	)

	if initialAvailable == 0 {
		// Pool may be exhausted from a prior scenario (e.g. abuse).
		// If there are leased nonces, wait for reclaim before proceeding.
		if healthBefore.NoncePool.Leased > 0 {
			logger.Info("nonce pool exhausted from prior scenario, waiting for reclaim first",
				"leased", healthBefore.NoncePool.Leased,
			)
			if !waitForPoolRecovery(ctx, gw, 1, leaseTTL+60*time.Second, logger) {
				logger.Error("pool did not recover from prior exhaustion")
				m.Inc("lease_reclaim_skipped")
				return
			}
			// Re-read initial state after recovery
			healthBefore, err = gw.Health()
			if err != nil {
				logger.Error("cannot re-query health after recovery", "error", err)
				m.Inc("lease_reclaim_errors")
				return
			}
			initialAvailable = healthBefore.NoncePool.Available
			initialTotal = healthBefore.NoncePool.Total
			m.Add("nonce_initial_available", int64(initialAvailable))
			m.Add("nonce_initial_total", int64(initialTotal))
			logger.Info("pool recovered, starting TTL test",
				"nonce_available", initialAvailable,
			)
		} else {
			logger.Warn("nonce pool empty with zero leased, cannot run lease reclaim test")
			m.Inc("lease_reclaim_skipped")
			return
		}
	}

	// ─── Phase 1: Exhaust nonce pool ────────────────────────────────
	//
	// Rapidly acquire challenges from multiple goroutines. Each 402
	// response leases one nonce UTXO. We intentionally do NOT follow
	// up with delegation, leaving every nonce in LEASED state.

	logger.Info("phase 1: exhausting nonce pool via rapid challenge acquisition")

	exhaustCtx, exhaustCancel := context.WithTimeout(ctx, 60*time.Second)
	defer exhaustCancel()

	workers := 10
	var wg sync.WaitGroup
	var acquired atomic.Int64
	var exhaustionReached atomic.Bool

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			consecutiveErrors := 0

			for {
				select {
				case <-exhaustCtx.Done():
					return
				default:
				}

				if exhaustionReached.Load() {
					return
				}

				ch, err := gw.GetChallenge()
				if err != nil {
					consecutiveErrors++
					m.Inc("challenge_errors")
					if consecutiveErrors >= 20 {
						// Sustained errors indicate pool exhaustion
						exhaustionReached.Store(true)
						return
					}
					time.Sleep(20 * time.Millisecond)
					continue
				}
				consecutiveErrors = 0
				acquired.Add(1)
				m.Inc("challenges_requested")

				if ch.NonceUTXO == nil {
					m.Inc("challenge_no_nonce")
				}

				// Periodically check pool state
				count := acquired.Load()
				if count%25 == 0 {
					health, herr := gw.Health()
					if herr == nil && health.NoncePool.Available == 0 {
						logger.Info("nonce pool exhausted during flood",
							"challenges_acquired", count,
							"leased", health.NoncePool.Leased,
						)
						exhaustionReached.Store(true)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
	exhaustCancel()

	// Confirm exhaustion
	healthExhausted, err := gw.Health()
	if err != nil {
		logger.Error("cannot query health after exhaustion", "error", err)
		m.Inc("lease_reclaim_errors")
		return
	}

	logger.Info("post-exhaustion pool state",
		"challenges_acquired", acquired.Load(),
		"available", healthExhausted.NoncePool.Available,
		"leased", healthExhausted.NoncePool.Leased,
		"spent", healthExhausted.NoncePool.Spent,
	)

	if healthExhausted.NoncePool.Available == 0 {
		m.Inc("nonce_pool_exhausted")
		logger.Info("confirmed: nonce pool fully exhausted")
	} else {
		logger.Warn("nonce pool not fully exhausted",
			"remaining", healthExhausted.NoncePool.Available,
		)
		// Continue anyway, partial exhaustion is still a valid test
	}

	// ─── Phase 2: Wait for lease TTL + reclaim buffer ───────────────
	//
	// The reclaim loop runs every 30 seconds and reclaims leases
	// whose expiresAt timestamp is in the past. We need to wait at
	// least leaseTTL for the leases to expire, plus up to 30s for
	// the next reclaim tick, plus a small margin.

	buffer := 40 * time.Second // 30s reclaim interval + 10s margin
	waitDuration := leaseTTL + buffer

	logger.Info("phase 2: waiting for lease TTL expiry + reclaim",
		"lease_ttl", leaseTTL,
		"buffer", buffer,
		"total_wait", waitDuration,
	)

	reclaimStart := time.Now()
	pollTicker := time.NewTicker(2 * time.Second)
	defer pollTicker.Stop()

	deadline := time.After(waitDuration)
	recovered := false
	var reclaimDuration time.Duration

	// Recovery threshold: at least 80% of initial available nonces
	// must return to available. Some may legitimately be spent.
	threshold := int(float64(initialAvailable) * 0.80)
	if threshold < 1 {
		threshold = 1
	}

	for !recovered {
		select {
		case <-ctx.Done():
			logger.Warn("context cancelled while waiting for reclaim")
			return

		case <-deadline:
			logger.Error("deadline reached without full reclaim")
			goto checkFinal

		case <-pollTicker.C:
			health, herr := gw.Health()
			if herr != nil {
				logger.Debug("health check failed during poll", "error", herr)
				continue
			}

			elapsed := time.Since(reclaimStart).Truncate(time.Second)
			logger.Debug("reclaim poll",
				"elapsed", elapsed,
				"available", health.NoncePool.Available,
				"leased", health.NoncePool.Leased,
				"spent", health.NoncePool.Spent,
				"threshold", threshold,
			)

			if health.NoncePool.Available >= threshold {
				reclaimDuration = time.Since(reclaimStart)
				recovered = true
				m.Inc("nonce_recovered")
				m.Add("reclaim_duration_ms", reclaimDuration.Milliseconds())
				logger.Info("nonce pool recovered",
					"available", health.NoncePool.Available,
					"threshold", threshold,
					"reclaim_duration", reclaimDuration.Truncate(time.Second),
				)
			}
		}
	}

checkFinal:
	// ─── Phase 3: Final verification ────────────────────────────────

	healthFinal, err := gw.Health()
	if err != nil {
		logger.Error("cannot query final health", "error", err)
		m.Inc("lease_reclaim_errors")
		return
	}

	logger.Info("final pool state",
		"available", healthFinal.NoncePool.Available,
		"leased", healthFinal.NoncePool.Leased,
		"spent", healthFinal.NoncePool.Spent,
		"total", healthFinal.NoncePool.Total,
	)

	m.Add("nonce_final_available", int64(healthFinal.NoncePool.Available))
	m.Add("nonce_final_leased", int64(healthFinal.NoncePool.Leased))

	if recovered {
		m.Inc("reclaim_success")

		// Verify new challenges can be acquired after reclaim
		ch, err := gw.GetChallenge()
		if err != nil {
			logger.Error("cannot acquire challenge after reclaim", "error", err)
			m.Inc("CRITICAL_post_reclaim_challenge_failed")
		} else if ch.NonceUTXO != nil {
			m.Inc("post_reclaim_challenge_success")
			logger.Info("confirmed: new challenges available after reclaim",
				"nonce_txid", ch.NonceUTXO.TxID[:16]+"...",
			)
		} else {
			m.Inc("post_reclaim_challenge_no_nonce")
			logger.Warn("post-reclaim challenge has no nonce UTXO")
		}
	} else {
		m.Inc("CRITICAL_reclaim_failed")
		logger.Error("CRITICAL: nonce pool did NOT recover after TTL",
			"initial_available", initialAvailable,
			"final_available", healthFinal.NoncePool.Available,
			"final_leased", healthFinal.NoncePool.Leased,
		)
	}

	logger.Info("nonce lease TTL reclaim scenario complete")
}

// waitForPoolRecovery polls /health until nonce_available >= minAvailable
// or the timeout expires. Used when a prior scenario has exhausted the pool.
func waitForPoolRecovery(ctx context.Context, gw *client.GatewayClient, minAvailable int, timeout time.Duration, logger *slog.Logger) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-ticker.C:
			health, err := gw.Health()
			if err != nil {
				continue
			}
			logger.Debug("waiting for pool recovery",
				"available", health.NoncePool.Available,
				"leased", health.NoncePool.Leased,
			)
			if health.NoncePool.Available >= minAvailable {
				return true
			}
		}
	}
}
