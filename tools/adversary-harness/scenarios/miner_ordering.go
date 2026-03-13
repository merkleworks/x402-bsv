// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


// Package scenarios implements adversarial test scenarios against the x402 gateway.
package scenarios

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
)

// MinerOrdering simulates competing transactions and miner ordering variance.
//
// Attack model:
//   - Attacker obtains a challenge with a nonce UTXO
//   - Attacker creates multiple "competing" delegation requests using the
//     same nonce (simulating txA, txB, txC with different fee levels)
//   - Gateway's atomic nonce reservation (TryReserve) must ensure only
//     one wins; all others get 409 (double_spend) or 202 (nonce_pending)
//   - After one succeeds, submitting any competing proof must be rejected
//
// Protocol invariants tested:
//   - Replay cache blocks duplicate nonce usage
//   - TryReserve is atomic (only one concurrent reservation wins)
//   - Proof for a different txid than the committed one is rejected
func MinerOrdering(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, clients int, logger *slog.Logger) {
	logger.Info("starting miner ordering variance scenario", "clients", clients)

	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			runMinerOrderingRound(ctx, gw, m, id, logger)
		}(i)
	}
	wg.Wait()
	logger.Info("miner ordering scenario complete")
}

func runMinerOrderingRound(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, id int, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		m.Inc("miner_order_tests")

		// 1. Get a challenge (nonce UTXO allocated)
		ch, err := gw.GetChallenge()
		if err != nil {
			m.Inc("challenge_errors")
			logger.Debug("challenge failed", "worker", id, "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		m.Inc("challenges_requested")

		if ch.NonceUTXO == nil {
			m.Inc("challenge_no_nonce")
			continue
		}

		// 2. Simulate 3 "competing" delegation requests with the same nonce
		//    In reality, only one can win the TryReserve race.
		//    We create fake partial tx hex variations to simulate fee variance.
		nonceTxID := ch.NonceUTXO.TxID
		nonceVout := ch.NonceUTXO.Vout
		challengeHash := computeFakeChallengeHash(ch.Raw)

		type variant struct {
			name string
			fee  string // appended to partial_tx to make it differ
		}
		variants := []variant{
			{name: "txA_original", fee: "01"},
			{name: "txB_high_fee", fee: "ff"},
			{name: "txC_low_fee", fee: "00"},
		}

		// Shuffle to randomize "miner ordering"
		rand.Shuffle(len(variants), func(i, j int) {
			variants[i], variants[j] = variants[j], variants[i]
		})

		var delegMu sync.Mutex
		var winnerName string
		var winnerStatus int
		results := make([]string, len(variants))

		// Fire all 3 concurrently (simulating competing miners)
		var delegWg sync.WaitGroup
		for vi, v := range variants {
			delegWg.Add(1)
			go func(idx int, vt variant) {
				defer delegWg.Done()

				start := time.Now()
				partialHex := "0100000001" + vt.fee // fake partial tx
				status, body, err := gw.Delegate(partialHex, challengeHash, nonceTxID, nonceVout)
				elapsed := time.Since(start)
				m.RecordLatency("delegation", elapsed)

				delegMu.Lock()
				defer delegMu.Unlock()

				if err != nil {
					results[idx] = fmt.Sprintf("%s: error=%v", vt.name, err)
					m.Inc("delegation_errors")
					return
				}

				results[idx] = fmt.Sprintf("%s: status=%d", vt.name, status)

				switch status {
				case 200:
					m.Inc("delegation_accepted")
					if winnerName == "" {
						winnerName = vt.name
						winnerStatus = status
					}
				case 202:
					m.Inc("nonce_pending")
				case 409:
					m.Inc("double_spend_detected")
				case 400:
					m.Inc("delegation_rejected_400")
				default:
					m.Inc("delegation_other_" + fmt.Sprintf("%d", status))
				}

				_ = body // logged at debug level
			}(vi, v)
		}
		delegWg.Wait()

		// Log the round results
		logger.Debug("miner ordering round",
			"worker", id,
			"nonce", nonceTxID[:16]+"...",
			"winner", winnerName,
			"winner_status", winnerStatus,
			"results", results,
		)

		// 3. If a delegation succeeded, try submitting a fake "competing" proof
		//    with a different txid. Gateway should reject it.
		if winnerName != "" {
			fakeTxID := computeFakeTxID(nonceTxID)
			fakeProof := buildFakeProofHeader(fakeTxID, challengeHash, ch)
			status, _, _ := gw.SubmitProof(fakeProof)
			if status == 200 {
				m.Inc("CRITICAL_fake_proof_accepted")
				logger.Error("CRITICAL: fake competing proof accepted!", "worker", id, "fake_txid", fakeTxID)
			} else {
				m.Inc("fake_proof_rejected")
			}
		}

		// Small backoff to avoid pool exhaustion
		time.Sleep(50 * time.Millisecond)
	}
}

// computeFakeChallengeHash creates a deterministic hash from the challenge.
func computeFakeChallengeHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// computeFakeTxID creates a fake "competing" txid by hashing the original.
func computeFakeTxID(original string) string {
	h := sha256.Sum256([]byte("competing:" + original))
	return hex.EncodeToString(h[:])
}

// buildFakeProofHeader constructs a minimal fake proof header for testing.
// The gateway should reject it because the txid won't match any cached entry.
func buildFakeProofHeader(txid, challengeHash string, ch *client.Challenge) string {
	proof := map[string]any{
		"v":                "1",
		"scheme":           "bsv-tx-v1",
		"txid":             txid,
		"rawtx_b64":        "AAAA", // fake
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
	return hex.EncodeToString(data)
}
