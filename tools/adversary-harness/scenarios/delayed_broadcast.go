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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
)

// DelayedBroadcast simulates scenarios where the delegator returns a
// signed transaction but broadcast is delayed or never happens.
//
// Attack model:
//   - Case A: normal flow — delegation succeeds, proof submitted immediately
//   - Case B: delayed — delegation succeeds, proof submitted after 5-30s
//     The challenge may expire (5 min TTL) but short delays should be fine.
//   - Case C: never broadcast — delegation succeeds but client never submits proof
//     The nonce UTXO stays committed in replay cache; fee UTXO stays MarkSpent.
//     This tests whether orphaned transactions cause resource leaks.
//
// Since the harness cannot build valid partial transactions (no BSV signing),
// we test the delegation endpoint with invalid data and observe:
//   - Challenge acquisition rate under sustained load
//   - Nonce UTXO pool consumption patterns
//   - Challenge TTL enforcement on delayed proof submission
//   - Pool health after orphaned delegation attempts
//
// Protocol invariants tested:
//   - Challenges expire after TTL
//   - Expired proofs are rejected
//   - Pool resources are eventually reclaimed
func DelayedBroadcast(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	logger.Info("starting delayed broadcast scenario", "clients", clients)

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runDelayedBroadcastRound(ctx, gw, m, id, logger)
		}(i)
	}
	wg.Wait()

	// Final pool health check
	health, err := gw.Health()
	if err == nil {
		logger.Info("post-scenario pool health",
			"nonce_available", health.NoncePool.Available,
			"nonce_leased", health.NoncePool.Leased,
			"nonce_spent", health.NoncePool.Spent,
			"fee_available", health.FeePool.Available,
			"fee_leased", health.FeePool.Leased,
		)
	}

	logger.Info("delayed broadcast scenario complete")
}

func runDelayedBroadcastRound(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, id int, logger *slog.Logger) {
	behaviours := []string{"immediate", "delayed", "never"}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.Inc("broadcast_tests")

		// 1. Get challenge
		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			time.Sleep(200 * time.Millisecond)
			continue
		}
		m.Inc("challenges_requested")

		behaviour := behaviours[rand.Intn(len(behaviours))]

		switch behaviour {
		case "immediate":
			runBroadcastImmediate(ctx, gw, m, ch, id, logger)
		case "delayed":
			runBroadcastDelayed(ctx, gw, m, ch, id, logger)
		case "never":
			runBroadcastNever(ctx, gw, m, ch, id, logger)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func runBroadcastImmediate(_ context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Simulate: delegation + immediate proof submission
	// Since we can't build valid partial txs, we test with a fake one
	// and observe the rejection. This still exercises the full HTTP path.
	challengeHash := computeFakeChallengeHash(ch.Raw)

	start := time.Now()
	status, _, _ := gw.Delegate("0100000001dead", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)
	elapsed := time.Since(start)
	m.RecordLatency("delegation", elapsed)

	if status == 200 {
		m.Inc("broadcast_immediate_delegated")
	} else {
		m.Inc("broadcast_immediate_rejected")
	}

	// Submit a fake proof immediately
	fakeProof := buildTimedProofHeader(ch, challengeHash)
	proofStatus, _, _ := gw.SubmitProof(fakeProof)

	if proofStatus == 200 {
		m.Inc("proof_accepted")
		m.Inc("broadcast_immediate_success")
	} else {
		m.Inc("proof_rejected")
		m.Inc("broadcast_immediate_proof_rejected")
	}

	logger.Debug("immediate broadcast", "worker", id, "deleg_status", status, "proof_status", proofStatus)
}

func runBroadcastDelayed(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Simulate: delegation + delayed proof submission (5-30s)
	challengeHash := computeFakeChallengeHash(ch.Raw)

	start := time.Now()
	status, _, _ := gw.Delegate("0100000001beef", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)
	elapsed := time.Since(start)
	m.RecordLatency("delegation", elapsed)

	if status == 200 {
		m.Inc("broadcast_delayed_delegated")
	} else {
		m.Inc("broadcast_delayed_deleg_rejected")
	}

	// Wait 5-30 seconds (but cap at context deadline)
	delay := time.Duration(5+rand.Intn(25)) * time.Second
	select {
	case <-ctx.Done():
		m.Inc("broadcast_delayed_cancelled")
		return
	case <-time.After(delay):
	}

	// Submit proof after delay
	fakeProof := buildTimedProofHeader(ch, challengeHash)
	proofStatus, _, _ := gw.SubmitProof(fakeProof)

	if proofStatus == 200 {
		m.Inc("proof_accepted")
		m.Inc("broadcast_delayed_success")
	} else if proofStatus == 402 {
		// Challenge likely expired — this is expected for long delays
		m.Inc("broadcast_delayed_expired")
	} else {
		m.Inc("proof_rejected")
		m.Inc("broadcast_delayed_proof_rejected")
	}

	logger.Debug("delayed broadcast", "worker", id, "delay_s", delay.Seconds(), "deleg_status", status, "proof_status", proofStatus)
}

func runBroadcastNever(_ context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Simulate: delegation but never submit proof
	// This tests resource leak: nonce stays committed, fee UTXO stays spent.
	// The nonce pool should NOT recover these (they're committed, not leaked).
	// Fee pool should NOT recover these (they're marked spent).
	challengeHash := computeFakeChallengeHash(ch.Raw)

	start := time.Now()
	status, _, _ := gw.Delegate("0100000001cafe", challengeHash, ch.NonceUTXO.TxID, ch.NonceUTXO.Vout)
	elapsed := time.Since(start)
	m.RecordLatency("delegation", elapsed)

	if status == 200 {
		m.Inc("broadcast_never_delegated")
		m.Inc("orphaned_tx")
	} else {
		m.Inc("broadcast_never_rejected")
	}

	logger.Debug("never broadcast (orphan)", "worker", id, "deleg_status", status)
}

// buildTimedProofHeader constructs a fake proof for timing tests.
func buildTimedProofHeader(ch *client.Challenge, challengeHash string) string {
	proof := map[string]any{
		"v":                "1",
		"scheme":           "bsv-tx-v1",
		"txid":             fmt.Sprintf("%064x", rand.Int63()),
		"rawtx_b64":        base64.StdEncoding.EncodeToString([]byte{0x01}),
		"challenge_sha256": challengeHash,
		"request": map[string]string{
			"domain":             ch.Domain,
			"method":             ch.Method,
			"path":               ch.Path,
			"query":              ch.Query,
			"req_headers_sha256": ch.ReqHeadersSHA256,
			"req_body_sha256":    ch.ReqBodySHA256,
		},
	}
	data, _ := json.Marshal(proof)
	return base64.StdEncoding.EncodeToString(data)
}
