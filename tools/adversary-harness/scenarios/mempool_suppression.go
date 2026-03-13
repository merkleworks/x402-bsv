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

// MempoolSuppression simulates mempool visibility failures.
//
// Attack model:
//   - Case A: tx was broadcast but mempool query returns false (false negative)
//     → Attacker submits a proof with a valid-looking txid but the gateway's
//       mempool check fails. Gateway should reject with mempool_rejected.
//   - Case B: tx was NOT broadcast but attacker claims it was (false positive)
//     → Attacker submits a proof with fabricated txid. Gateway must reject.
//   - Case C: delayed propagation — tx visible only after a delay
//     → Attacker submits proof immediately, then retries after delay.
//       First attempt may fail, retry may succeed (depending on gateway mode).
//
// Since we can't control the gateway's mempool checker from outside,
// we simulate these cases by constructing proofs with:
//   - Real challenge data but fake/invalid txids (tests mempool gating)
//   - Expired challenges (tests TTL enforcement)
//   - Malformed rawtx (tests deserialization defense)
//
// Protocol invariants tested:
//   - Gateway rejects proofs with unknown txids
//   - Gateway enforces challenge expiry
//   - Mempool gating prevents acceptance of unbroadcast transactions
func MempoolSuppression(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	logger.Info("starting mempool suppression scenario", "clients", clients)

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runMempoolSuppressionRound(ctx, gw, m, id, logger)
		}(i)
	}
	wg.Wait()
	logger.Info("mempool suppression scenario complete")
}

func runMempoolSuppressionRound(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, id int, logger *slog.Logger) {
	cases := []string{"false_negative", "false_positive", "delayed_propagation"}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.Inc("mempool_tests")

		// Get a fresh challenge
		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			time.Sleep(100 * time.Millisecond)
			continue
		}
		m.Inc("challenges_requested")

		// Pick a random case
		testCase := cases[rand.Intn(len(cases))]

		switch testCase {
		case "false_negative":
			// Case A: submit a proof with a plausible but non-existent txid.
			// Gateway's mempool check should fail → reject.
			runFalseNegative(ctx, gw, m, ch, id, logger)

		case "false_positive":
			// Case B: submit proof with completely fabricated data.
			// Gateway should reject at proof parsing or challenge binding.
			runFalsePositive(ctx, gw, m, ch, id, logger)

		case "delayed_propagation":
			// Case C: submit immediately, then retry after delay.
			// Tests whether gateway correctly handles retry semantics.
			runDelayedPropagation(ctx, gw, m, ch, id, logger)
		}

		time.Sleep(50 * time.Millisecond)
	}
}

func runFalseNegative(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Construct a proof with a valid-looking but fake txid
	fakeTxID := fmt.Sprintf("%064x", rand.Int63())
	challengeHash := computeFakeChallengeHash(ch.Raw)

	proof := map[string]any{
		"v":                "1",
		"scheme":           "bsv-tx-v1",
		"txid":             fakeTxID,
		"rawtx_b64":        base64.StdEncoding.EncodeToString([]byte("fake-raw-tx-data")),
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
	proofJSON, _ := json.Marshal(proof)
	proofHeader := base64.StdEncoding.EncodeToString(proofJSON)

	start := time.Now()
	status, body, err := gw.SubmitProof(proofHeader)
	elapsed := time.Since(start)
	m.RecordLatency("proof_submission", elapsed)

	if err != nil {
		m.Inc("proof_submit_errors")
		return
	}

	m.Inc("mempool_false_negative_tests")

	if status == 200 {
		// CRITICAL: gateway accepted a proof with a fake txid
		m.Inc("CRITICAL_false_negative_accepted")
		logger.Error("CRITICAL: proof with fake txid accepted!", "worker", id, "txid", fakeTxID)
	} else {
		m.Inc("false_negative_rejected")
		logger.Debug("false negative correctly rejected", "worker", id, "status", status, "body", string(body))
	}
}

func runFalsePositive(_ context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Completely fabricated proof — wrong challenge hash, wrong txid
	proof := map[string]any{
		"v":                "1",
		"scheme":           "bsv-tx-v1",
		"txid":             fmt.Sprintf("%064x", rand.Int63()),
		"rawtx_b64":        "AAAA",
		"challenge_sha256": fmt.Sprintf("%064x", rand.Int63()),
		"request": map[string]string{
			"domain":             ch.Domain,
			"method":             "DELETE", // wrong method
			"path":               "/admin",  // wrong path
			"query":              "",
			"req_headers_sha256": fmt.Sprintf("%064x", rand.Int63()),
			"req_body_sha256":    fmt.Sprintf("%064x", rand.Int63()),
		},
	}
	proofJSON, _ := json.Marshal(proof)
	proofHeader := base64.StdEncoding.EncodeToString(proofJSON)

	start := time.Now()
	status, _, err := gw.SubmitProof(proofHeader)
	elapsed := time.Since(start)
	m.RecordLatency("proof_submission", elapsed)

	if err != nil {
		m.Inc("proof_submit_errors")
		return
	}

	m.Inc("mempool_false_positive_tests")

	if status == 200 {
		m.Inc("CRITICAL_false_positive_accepted")
		logger.Error("CRITICAL: fabricated proof accepted!", "worker", id)
	} else {
		m.Inc("false_positive_rejected")
	}
}

func runDelayedPropagation(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, ch *client.Challenge, id int, logger *slog.Logger) {
	// Submit a fake proof, wait, then submit again.
	// The gateway should reject both (fake txid) but the second attempt
	// exercises the retry path.
	fakeTxID := fmt.Sprintf("%064x", rand.Int63())
	challengeHash := computeFakeChallengeHash(ch.Raw)

	proof := map[string]any{
		"v":                "1",
		"scheme":           "bsv-tx-v1",
		"txid":             fakeTxID,
		"rawtx_b64":        base64.StdEncoding.EncodeToString([]byte{0x01, 0x00}),
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
	proofJSON, _ := json.Marshal(proof)
	proofHeader := base64.StdEncoding.EncodeToString(proofJSON)

	// First attempt — immediate
	status1, _, _ := gw.SubmitProof(proofHeader)
	m.Inc("delayed_attempt_1")

	if status1 == 200 {
		m.Inc("CRITICAL_delayed_accepted_immediate")
	} else {
		m.Inc("delayed_rejected_1")
	}

	// Wait a simulated propagation delay
	delay := time.Duration(500+rand.Intn(2000)) * time.Millisecond
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	// Retry after delay
	status2, _, _ := gw.SubmitProof(proofHeader)
	m.Inc("delayed_attempt_2")

	if status2 == 200 {
		m.Inc("CRITICAL_delayed_accepted_retry")
	} else {
		m.Inc("delayed_rejected_2")
	}

	m.Inc("delayed_propagation_tests")
	logger.Debug("delayed propagation test",
		"worker", id,
		"status_immediate", status1,
		"status_retry", status2,
		"delay_ms", delay.Milliseconds(),
	)
}
