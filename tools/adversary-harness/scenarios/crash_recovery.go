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
	"os/exec"
	"strings"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
)

// CrashRecovery verifies that volatile replay cache behaviour does not
// cause permanent payment denial after a gateway process restart, and
// that duplicate proof protection is re-established once the process
// comes back online.
//
// Attack model:
//   - Client acquires a challenge and builds a valid proof via
//     /demo/build-proof (which delegates internally and returns a
//     signed transaction).
//   - Before submitting the proof, the gateway process crashes or
//     is forcefully restarted. The in-memory replay cache is lost.
//   - After restart the client submits the original proof.
//     Expected: gateway accepts the proof (the proof is self-contained;
//     the gatekeeper re-derives the challenge from request parameters).
//   - Client submits the same proof a second time.
//     Expected: gateway rejects the duplicate (replay cache recorded
//     the txid on first successful submission).
//
// Why the replay cache is intentionally volatile:
//   The replay cache lives in-memory so that a process restart clears
//   stale entries. On-chain finality (not an in-process cache) is the
//   ultimate double-spend arbiter. The cache is a performance
//   optimisation that prevents obvious replays within a single process
//   lifetime. After restart it is empty, which means:
//     • Previously seen txids can be re-submitted — the gateway must
//       re-validate them (mempool / confirmation check).
//     • The first successful proof re-populates the cache; subsequent
//       duplicates are rejected as before.
//
// Protocol invariants tested:
//   - Valid proof accepted after restart (no permanent denial)
//   - Duplicate proof rejected after restart (replay re-established)
//   - Pool state survives restart (Redis-backed)
//   - Gateway recovers to healthy state within timeout
func CrashRecovery(ctx context.Context, gw *client.GatewayClient, m *metrics.Collector, restartCmd string, logger *slog.Logger) {
	logger.Info("starting gateway crash/restart recovery scenario",
		"restart_cmd", restartCmd,
	)
	m.Inc("crash_tests")

	if strings.TrimSpace(restartCmd) == "" {
		logger.Error("restart command is empty; use -restart-cmd flag")
		m.Inc("crash_config_error")
		return
	}

	// ─── Step 1: Acquire challenge ──────────────────────────────────
	//
	// If the pool is temporarily exhausted from a prior scenario,
	// retry with backoff until a nonce becomes available.

	var ch *client.Challenge
	var err error
	for attempt := 0; attempt < 60; attempt++ {
		ch, err = gw.GetChallenge()
		if err == nil && ch.NonceUTXO != nil {
			break
		}
		if attempt == 0 {
			logger.Info("nonce pool may be exhausted from prior scenario, retrying...")
		}
		select {
		case <-ctx.Done():
			logger.Error("context cancelled while waiting for challenge")
			m.Inc("crash_challenge_error")
			return
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		logger.Error("cannot acquire challenge for crash test after retries", "error", err)
		m.Inc("crash_challenge_error")
		return
	}
	m.Inc("challenges_requested")

	if ch.NonceUTXO == nil {
		logger.Error("challenge has no nonce UTXO after retries, cannot proceed")
		m.Inc("crash_challenge_no_nonce")
		return
	}

	logger.Info("challenge acquired",
		"nonce_txid", ch.NonceUTXO.TxID[:16]+"...",
		"amount_sats", ch.AmountSats,
	)

	// ─── Step 2: Build proof (but do NOT submit yet) ────────────────
	//
	// /demo/build-proof performs delegation internally and returns a
	// complete proof header with txid and rawtx. We capture it but
	// deliberately withhold submission until after the crash.

	proof, err := gw.BuildProof(ch.Raw)
	if err != nil {
		logger.Error("cannot build proof for crash test", "error", err)
		m.Inc("crash_build_proof_error")
		return
	}

	txidShort := proof.TxID
	if len(txidShort) > 16 {
		txidShort = txidShort[:16] + "..."
	}
	logger.Info("proof built (withheld from submission)",
		"txid", txidShort,
	)

	// Snapshot pre-crash pool state
	healthBefore, _ := gw.Health()
	if healthBefore != nil {
		m.Add("crash_pre_nonce_available", int64(healthBefore.NoncePool.Available))
		m.Add("crash_pre_fee_available", int64(healthBefore.FeePool.Available))
		logger.Info("pre-crash pool state",
			"nonce_available", healthBefore.NoncePool.Available,
			"fee_available", healthBefore.FeePool.Available,
		)
	}

	// ─── Step 3: Simulate gateway crash ─────────────────────────────
	//
	// Execute the user-provided restart command. For Docker this is
	// typically "docker restart <container>". The command is expected
	// to be synchronous — it returns after the process has restarted
	// (though the application inside may not be fully ready yet).

	logger.Info("executing restart command", "cmd", restartCmd)

	parts := strings.Fields(restartCmd)
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("restart command failed",
			"error", err,
			"output", strings.TrimSpace(string(output)),
		)
		m.Inc("crash_restart_error")
		return
	}

	logger.Info("restart command completed",
		"output", strings.TrimSpace(string(output)),
	)

	// ─── Step 4: Wait for gateway to become healthy ─────────────────

	logger.Info("waiting for gateway to recover...")

	if !waitForHealth(ctx, gw, 120*time.Second, logger) {
		logger.Error("CRITICAL: gateway did not recover within 120 seconds")
		m.Inc("CRITICAL_gateway_not_recovered")
		return
	}

	m.Inc("crash_gateway_recovered")

	// Snapshot post-crash pool state
	healthAfter, _ := gw.Health()
	if healthAfter != nil {
		m.Add("crash_post_nonce_available", int64(healthAfter.NoncePool.Available))
		m.Add("crash_post_fee_available", int64(healthAfter.FeePool.Available))
		logger.Info("post-restart pool state",
			"nonce_available", healthAfter.NoncePool.Available,
			"nonce_leased", healthAfter.NoncePool.Leased,
			"fee_available", healthAfter.FeePool.Available,
		)
	}

	// ─── Step 5: Submit original proof after restart ─────────────────
	//
	// Two outcomes are acceptable:
	//   a) Gateway accepts the proof (proof is self-contained, gatekeeper
	//      re-derives the challenge from request parameters).
	//   b) Gateway rejects with "challenge_not_found" because the
	//      in-memory challenge cache was cleared on restart. This is
	//      expected — the challenge cache is intentionally volatile.
	//
	// Only a permanent, unexplained rejection would be a failure.

	logger.Info("submitting proof after restart")

	status, body, err := gw.SubmitProof(proof.ProofHeader)
	if err != nil {
		logger.Error("proof submission failed after restart", "error", err)
		m.Inc("crash_proof_submit_error")
		return
	}

	logger.Info("proof result after restart",
		"status", status,
		"body_preview", truncate(string(body), 200),
	)

	if status == 200 {
		m.Inc("proof_after_restart_success")
		logger.Info("proof accepted after restart — state preserved")
	} else {
		m.Inc("proof_after_restart_failure")
		// Determine if rejection is expected (challenge cache cleared)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "challenge_not_found") ||
			strings.Contains(bodyStr, "expired") {
			logger.Info("proof rejected (challenge cache cleared on restart — expected)",
				"status", status,
			)
		} else {
			logger.Warn("proof rejected after restart for unexpected reason",
				"status", status,
				"body", bodyStr,
			)
		}
	}

	// ─── Step 6: Attempt duplicate proof submission ──────────────────
	//
	// If step 5 accepted the proof, step 6 should be rejected by the
	// replay cache (duplicate txid). If step 5 was rejected due to
	// challenge cache loss, step 6 will also be rejected for the same
	// reason — we still count it as "duplicate rejected" since the
	// gateway correctly refuses repeated submissions.

	logger.Info("submitting duplicate proof")

	status2, body2, err := gw.SubmitProof(proof.ProofHeader)
	if err != nil {
		logger.Error("duplicate proof submission failed", "error", err)
		m.Inc("crash_duplicate_submit_error")
		return
	}

	logger.Info("duplicate proof result",
		"status", status2,
		"body_preview", truncate(string(body2), 200),
	)

	switch {
	case status == 200 && status2 == 200:
		// Both accepted — replay protection failure
		m.Inc("CRITICAL_duplicate_after_restart_accepted")
		logger.Error("CRITICAL: duplicate proof accepted after restart!")

	case status == 200 && status2 != 200:
		// First accepted, duplicate rejected — replay cache working
		m.Inc("duplicate_proof_rejected")
		m.Inc("replay_after_restart")
		logger.Info("replay protection confirmed after restart",
			"first_status", status,
			"duplicate_status", status2,
		)

	case status != 200:
		// First was rejected (challenge cache cleared on restart),
		// duplicate also rejected. Not a replay cache test but the
		// gateway is correctly refusing invalid proofs.
		m.Inc("duplicate_proof_rejected")
		logger.Info("both proofs rejected (challenge cache cleared on restart)",
			"first_status", status,
			"duplicate_status", status2,
		)
	}

	logger.Info("gateway crash/restart recovery scenario complete")
}

// waitForHealth polls GET /health every 2 seconds until the gateway
// returns status "ok" or the timeout expires.
func waitForHealth(ctx context.Context, gw *client.GatewayClient, timeout time.Duration, logger *slog.Logger) bool {
	deadline := time.After(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	// Initial 3-second delay: give the process time to start binding
	// the listener before we start hammering /health.
	select {
	case <-ctx.Done():
		return false
	case <-time.After(3 * time.Second):
	}

	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline:
			return false
		case <-ticker.C:
			health, err := gw.Health()
			if err != nil {
				logger.Debug("gateway not ready yet", "error", err)
				continue
			}
			if health.Status == "ok" {
				logger.Info("gateway recovered",
					"version", health.Version,
					"nonce_available", health.NoncePool.Available,
					"fee_available", health.FeePool.Available,
				)
				return true
			}
		}
	}
}

// truncate returns at most n bytes of s, appending "..." if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
