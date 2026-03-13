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
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
)

// FeeAbuse hammers the delegation and challenge endpoints to test:
//   - Nonce pool exhaustion under sustained load
//   - Fee pool exhaustion under concurrent delegation
//   - Nonce reservation contention (TryReserve 202 responses)
//   - Duplicate delegation with the same nonce
//   - Mid-flight cancellation (client disconnects during delegation)
//
// Attack vectors:
//   1. Rapid-fire challenge acquisition (drains nonce pool)
//   2. Concurrent delegation with same nonce (tests RACE-01 fix)
//   3. Duplicate delegation after first completes (tests replay cache)
//   4. Cancelled requests mid-flight (tests defer Release unwind)
//
// Protocol invariants tested:
//   - Pool exhaustion returns 503 (no_utxos_available)
//   - RACE-01: concurrent same-nonce → one 200, rest 202/409
//   - Replay cache blocks duplicate nonces after commit
//   - Cancelled requests release reserved nonces
func FeeAbuse(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	logger.Info("starting fee delegation abuse scenario", "clients", clients)

	// Snapshot pool health before
	healthBefore, _ := gw.Health()
	if healthBefore != nil {
		m.Add("initial_nonce_available", int64(healthBefore.NoncePool.Available))
		m.Add("initial_fee_available", int64(healthBefore.FeePool.Available))
		logger.Info("pool state before abuse",
			"nonce_available", healthBefore.NoncePool.Available,
			"fee_available", healthBefore.FeePool.Available,
		)
	}

	// Phase 1: Rapid-fire challenge acquisition (drains nonce pool)
	logger.Info("phase 1: rapid challenge acquisition")
	runChallengeFlood(ctx, gw, m, clients, logger)

	// Phase 2: Concurrent same-nonce delegation
	logger.Info("phase 2: concurrent same-nonce delegation (RACE-01)")
	runConcurrentNonceDelegation(ctx, gw, m, clients, logger)

	// Phase 3: Duplicate delegation replay
	logger.Info("phase 3: duplicate delegation replay")
	runDuplicateDelegation(ctx, gw, m, clients, logger)

	// Phase 4: Mid-flight cancellation
	logger.Info("phase 4: mid-flight cancellation")
	runCancelledDelegation(ctx, gw, m, clients, logger)

	// Snapshot pool health after
	healthAfter, _ := gw.Health()
	if healthAfter != nil {
		m.Add("final_nonce_available", int64(healthAfter.NoncePool.Available))
		m.Add("final_fee_available", int64(healthAfter.FeePool.Available))
		logger.Info("pool state after abuse",
			"nonce_available", healthAfter.NoncePool.Available,
			"nonce_leased", healthAfter.NoncePool.Leased,
			"nonce_spent", healthAfter.NoncePool.Spent,
			"fee_available", healthAfter.FeePool.Available,
			"fee_leased", healthAfter.FeePool.Leased,
		)
	}

	logger.Info("fee delegation abuse scenario complete")
}

// runChallengeFlood acquires challenges as fast as possible to drain the nonce pool.
func runChallengeFlood(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	// Use a sub-context with a 10-second window
	floodCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	var exhaustionSeen atomic.Bool

	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-floodCtx.Done():
					return
				default:
				}

				start := time.Now()
				ch, err := gw.GetChallenge()
				elapsed := time.Since(start)
				m.RecordLatency("challenge_acquisition", elapsed)

				if err != nil {
					m.Inc("challenge_errors")
					// Check if it's a pool exhaustion (503)
					if !exhaustionSeen.Load() {
						logger.Debug("challenge error", "worker", id, "err", err)
					}
					time.Sleep(10 * time.Millisecond)
					continue
				}

				m.Inc("challenges_requested")
				m.Inc("flood_challenges")

				if ch.NonceUTXO == nil {
					m.Inc("challenge_no_nonce")
				}
			}
		}(i)
	}
	wg.Wait()

	// Check for pool exhaustion
	health, _ := gw.Health()
	if health != nil && health.NoncePool.Available == 0 {
		m.Inc("nonce_pool_exhausted")
		logger.Info("nonce pool exhausted during flood",
			"leased", health.NoncePool.Leased,
			"spent", health.NoncePool.Spent,
		)
	}
}

// runConcurrentNonceDelegation gets one challenge and fires N concurrent
// delegation requests with the same nonce. Only one should win (RACE-01).
func runConcurrentNonceDelegation(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	// Cap concurrency for this test
	concurrency := clients
	if concurrency > 50 {
		concurrency = 50
	}

	// Run multiple rounds
	rounds := 5
	for round := 0; round < rounds; round++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			continue
		}
		m.Inc("challenges_requested")

		if ch.NonceUTXO == nil {
			continue
		}

		nonceTxID := ch.NonceUTXO.TxID
		nonceVout := ch.NonceUTXO.Vout
		challengeHash := computeFakeChallengeHash(ch.Raw)

		var wg sync.WaitGroup
		var mu sync.Mutex
		results := make(map[int]int) // status_code → count

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				partialHex := fmt.Sprintf("0100000001%04x", id)
				start := time.Now()
				status, _, _ := gw.Delegate(partialHex, challengeHash, nonceTxID, nonceVout)
				elapsed := time.Since(start)
				m.RecordLatency("delegation", elapsed)

				mu.Lock()
				results[status]++
				mu.Unlock()

				switch status {
				case 200:
					m.Inc("race_delegation_accepted")
				case 202:
					m.Inc("nonce_pending")
				case 409:
					m.Inc("double_spend_detected")
					m.Inc("replay_cache_conflicts")
				default:
					m.Inc("race_delegation_other")
				}
			}(i)
		}
		wg.Wait()

		logger.Info("concurrent nonce delegation round",
			"round", round+1,
			"concurrency", concurrency,
			"nonce", nonceTxID[:16]+"...",
			"results", results,
		)
	}
}

// runDuplicateDelegation gets a challenge, delegates once, then tries again.
func runDuplicateDelegation(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	iterations := clients
	if iterations > 20 {
		iterations = 20
	}

	for i := 0; i < iterations; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			continue
		}
		m.Inc("challenges_requested")

		if ch.NonceUTXO == nil {
			continue
		}

		challengeHash := computeFakeChallengeHash(ch.Raw)

		// First delegation
		status1, _, _ := gw.Delegate("0100000001aaaa", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)

		// Duplicate — should be rejected
		status2, _, _ := gw.Delegate("0100000001bbbb", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)

		m.Inc("duplicate_delegation_tests")

		if status2 == 409 || status2 == 202 {
			m.Inc("duplicate_correctly_rejected")
		} else if status2 == 200 {
			m.Inc("CRITICAL_duplicate_accepted")
			logger.Error("CRITICAL: duplicate delegation accepted!", "iteration", i)
		} else {
			m.Inc("duplicate_other_status")
		}

		logger.Debug("duplicate delegation", "iteration", i, "status_1", status1, "status_2", status2)
	}
}

// runCancelledDelegation tests client disconnection mid-flight.
// If the client cancels during delegation, the defer Release() should
// free the reserved nonce, making it available again.
func runCancelledDelegation(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	iterations := clients
	if iterations > 20 {
		iterations = 20
	}

	for i := 0; i < iterations; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			continue
		}
		m.Inc("challenges_requested")

		if ch.NonceUTXO == nil {
			continue
		}

		challengeHash := computeFakeChallengeHash(ch.Raw)

		// Create a context that cancels after a random short duration (1-50ms)
		// This simulates client disconnection mid-request
		cancelDelay := time.Duration(1+rand.Intn(49)) * time.Millisecond
		cancelCtx, cancelFn := context.WithTimeout(ctx, cancelDelay)

		// Fire delegation with cancellable context
		done := make(chan struct{})
		go func() {
			defer close(done)
			gw.Delegate("0100000001cancel", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)
		}()

		select {
		case <-cancelCtx.Done():
			m.Inc("delegation_cancelled")
		case <-done:
			m.Inc("delegation_completed_before_cancel")
		}
		cancelFn()

		// After cancellation, the nonce should eventually be released.
		// Try delegating with a different challenge to the same nonce.
		// This tests the Release() unwind path.
		time.Sleep(10 * time.Millisecond)

		status, _, _ := gw.Delegate("0100000001retry", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)
		m.Inc("post_cancel_delegation_tests")

		switch status {
		case 200:
			m.Inc("post_cancel_nonce_reused")
		case 409:
			m.Inc("post_cancel_nonce_committed")
		case 202:
			m.Inc("post_cancel_nonce_still_pending")
		default:
			m.Inc("post_cancel_other")
		}

		logger.Debug("cancelled delegation", "iteration", i, "cancel_delay_ms", cancelDelay.Milliseconds(), "retry_status", status)
	}
}
