// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package broadcast

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// CompositeBroadcaster chains a primary broadcaster (GorillaPool ARC) with
// a fallback (WhatsOnChain). It attempts the primary first; on transport-level
// failures it falls back to the secondary. Application-level rejections (bad tx,
// double-spend, fee too low) are NOT retried on fallback — the same rejection
// would occur.
//
// CompositeBroadcaster also implements MempoolChecker. For status checks,
// if the primary returns "not found/not tracked" (which ARC does for txs it
// hasn't processed), the fallback is consulted as a supplement.
type CompositeBroadcaster struct {
	primary  transaction.Broadcaster
	fallback transaction.Broadcaster
	health   *HealthTracker
	logger   *slog.Logger

	// skipPrimary is latched to 1 after a fee-policy rejection from the primary.
	// Since the fee rate is constant at runtime, a fee rejection will recur on
	// every broadcast — there's no point adding latency by retrying ARC each time.
	// Status checks (CheckMempool) are NOT affected by this flag.
	skipPrimary atomic.Int32
}

// NewCompositeBroadcaster creates a composite broadcaster.
// primary is typically GorillaPool ARC, fallback is typically WoC.
// health may be nil (health tracking disabled).
func NewCompositeBroadcaster(
	primary transaction.Broadcaster,
	fallback transaction.Broadcaster,
	health *HealthTracker,
) *CompositeBroadcaster {
	return &CompositeBroadcaster{
		primary:  primary,
		fallback: fallback,
		health:   health,
		logger:   slog.Default().With("component", "composite-broadcaster"),
	}
}

// Broadcast tries the primary broadcaster first. On transport-level failure,
// falls back to the secondary. Application-level failures are returned as-is.
func (c *CompositeBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	// Circuit breaker: if primary was latched off (e.g. persistent fee-policy
	// rejection), skip it entirely to avoid adding latency to every broadcast.
	if c.skipPrimary.Load() != 0 {
		c.recordStat(statPrimaryFailed, nil) // count as skipped
		return c.broadcastFallback(tx)
	}

	// Try primary
	success, failure := c.primary.Broadcast(tx)
	if failure == nil {
		// Primary succeeded
		c.recordHealth("gorilla", "broadcast", true, "")
		c.recordStat(statPrimarySuccess, failure)
		c.logger.Debug("broadcast via primary (GorillaPool)", "txid", success.Txid)
		return success, nil
	}

	// Primary failed — classify the error
	if !shouldFallback(failure) {
		// Application error (bad tx, double-spend, etc.) — do NOT fallback
		c.recordHealth("gorilla", "broadcast", true, "") // service reachable, tx rejected
		c.logger.Info("primary rejected tx (application error, no fallback)",
			"code", failure.Code,
			"desc", failure.Description,
		)
		return nil, failure
	}

	// Latch the circuit breaker on fee-policy rejections. The fee rate is
	// constant at runtime, so ARC will reject every subsequent broadcast
	// with the same error — skip it to avoid unnecessary latency.
	if isFeePolicyReject(failure) {
		if c.skipPrimary.CompareAndSwap(0, 1) {
			c.logger.Warn("ARC fee-policy rejection — latching circuit breaker, routing all broadcasts to WoC",
				"code", failure.Code,
			)
		}
	}

	// Transport or policy error — try fallback
	c.recordHealth("gorilla", "broadcast", false, failure.Code+": "+failure.Description)
	c.recordStat(statPrimaryFailed, failure)

	return c.broadcastFallback(tx)
}

// broadcastFallback sends the tx via the fallback broadcaster (WoC).
func (c *CompositeBroadcaster) broadcastFallback(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	success, failure := c.fallback.Broadcast(tx)
	if failure == nil {
		c.recordHealth("woc", "broadcast", true, "")
		c.recordStat(statFallbackSuccess, nil)
		c.logger.Debug("broadcast via fallback (WoC)", "txid", success.Txid)
		return success, nil
	}

	c.recordHealth("woc", "broadcast", false, failure.Code+": "+failure.Description)
	c.recordStat(statFallbackFailed, nil)
	c.logger.Error("fallback also failed",
		"code", failure.Code,
		"desc", failure.Description,
	)
	return nil, failure
}

// BroadcastCtx is the context-aware version. Same primary→fallback logic
// with circuit breaker support.
func (c *CompositeBroadcaster) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	// Circuit breaker: if primary was latched off, skip directly to fallback.
	if c.skipPrimary.Load() != 0 {
		c.recordStat(statPrimaryFailed, nil)
		return c.broadcastFallbackCtx(ctx, tx)
	}

	// Try primary (context-aware if supported)
	success, failure := broadcastWithCtx(c.primary, ctx, tx)
	if failure == nil {
		c.recordHealth("gorilla", "broadcast", true, "")
		c.recordStat(statPrimarySuccess, failure)
		c.logger.Debug("broadcast via primary (GorillaPool, ctx)", "txid", success.Txid)
		return success, nil
	}

	// Primary failed — classify the error
	if !shouldFallback(failure) {
		c.recordHealth("gorilla", "broadcast", true, "")
		c.logger.Info("primary rejected tx (application error, no fallback, ctx)",
			"code", failure.Code,
			"desc", failure.Description,
		)
		return nil, failure
	}

	// Latch circuit breaker on fee-policy rejections
	if isFeePolicyReject(failure) {
		if c.skipPrimary.CompareAndSwap(0, 1) {
			c.logger.Warn("ARC fee-policy rejection (ctx) — latching circuit breaker, routing all broadcasts to WoC",
				"code", failure.Code,
			)
		}
	}

	c.recordHealth("gorilla", "broadcast", false, failure.Code+": "+failure.Description)
	c.recordStat(statPrimaryFailed, failure)

	return c.broadcastFallbackCtx(ctx, tx)
}

// broadcastFallbackCtx sends the tx via the fallback broadcaster with context.
func (c *CompositeBroadcaster) broadcastFallbackCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	success, failure := broadcastWithCtx(c.fallback, ctx, tx)
	if failure == nil {
		c.recordHealth("woc", "broadcast", true, "")
		c.recordStat(statFallbackSuccess, nil)
		c.logger.Debug("broadcast via fallback (WoC, ctx)", "txid", success.Txid)
		return success, nil
	}

	c.recordHealth("woc", "broadcast", false, failure.Code+": "+failure.Description)
	c.recordStat(statFallbackFailed, nil)
	c.logger.Error("fallback also failed (ctx)",
		"code", failure.Code,
		"desc", failure.Description,
	)
	return nil, failure
}

// broadcastWithCtx tries BroadcastCtx if the broadcaster supports it,
// otherwise falls back to Broadcast.
func broadcastWithCtx(b transaction.Broadcaster, ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	type ctxBroadcaster interface {
		BroadcastCtx(context.Context, *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure)
	}
	if cb, ok := b.(ctxBroadcaster); ok {
		return cb.BroadcastCtx(ctx, tx)
	}
	return b.Broadcast(tx)
}

// CheckMempool implements MempoolChecker. Tries the primary (ARC) first.
// If primary returns "not found" (visible=false, doubleSpend=false, err=nil)
// — which ARC does for txs it hasn't processed — the fallback (WoC) is
// consulted as a supplement.
//
// If primary returns a definitive answer (visible=true, or doubleSpend=true,
// or err!=nil), that answer is returned immediately without fallback.
func (c *CompositeBroadcaster) CheckMempool(txid string) (bool, bool, error) {
	// Try primary mempool check
	if checker, ok := c.primary.(MempoolChecker); ok {
		visible, doubleSpend, err := checker.CheckMempool(txid)

		if err != nil {
			// Primary error — could be transport; try fallback
			c.recordHealth("gorilla", "status", false, err.Error())
			c.logger.Warn("primary status check error, trying fallback",
				"txid", txid, "error", err,
			)
		} else if visible || doubleSpend {
			// Primary gave a definitive answer
			c.recordHealth("gorilla", "status", true, "")
			return visible, doubleSpend, nil
		} else {
			// Primary says "not found" — might just mean ARC doesn't track it.
			// Try fallback for supplemental visibility.
			c.recordHealth("gorilla", "status", true, "")
		}
	}

	// Fallback mempool check
	if checker, ok := c.fallback.(MempoolChecker); ok {
		visible, doubleSpend, err := checker.CheckMempool(txid)
		if err != nil {
			c.recordHealth("woc", "status", false, err.Error())
			return false, false, err
		}
		c.recordHealth("woc", "status", true, "")
		return visible, doubleSpend, nil
	}

	// Neither supports MempoolChecker — assume visible (safe fallback)
	return true, false, nil
}

// Health returns the health tracker (may be nil).
func (c *CompositeBroadcaster) Health() *HealthTracker {
	return c.health
}

// SkipPrimary returns true if the circuit breaker has been latched
// (primary is being skipped for broadcasts due to fee-policy rejection).
func (c *CompositeBroadcaster) SkipPrimary() bool {
	return c.skipPrimary.Load() != 0
}

// recordHealth records a success or failure in the health tracker.
func (c *CompositeBroadcaster) recordHealth(service, role string, success bool, errMsg string) {
	if c.health == nil {
		return
	}
	if success {
		c.health.RecordSuccess(service, role)
	} else {
		c.health.RecordFailure(service, role, errMsg)
	}
}

// Stat event types for recordStat.
const (
	statPrimarySuccess  = "primary_success"
	statPrimaryFailed   = "primary_failed"
	statFallbackSuccess = "fallback_success"
	statFallbackFailed  = "fallback_failed"
)

// recordStat increments broadcast statistics in the health tracker.
// failure is used to detect fee-policy rejections for the dedicated counter.
func (c *CompositeBroadcaster) recordStat(event string, failure *transaction.BroadcastFailure) {
	if c.health == nil {
		return
	}
	switch event {
	case statPrimarySuccess:
		c.health.RecordPrimarySuccess()
	case statPrimaryFailed:
		c.health.RecordPrimaryFailed()
		if failure != nil && isFeePolicyReject(failure) {
			c.health.RecordFeePolicyReject()
		}
	case statFallbackSuccess:
		c.health.RecordFallbackSuccess()
	case statFallbackFailed:
		c.health.RecordFallbackFailed()
	}
}

// isFeePolicyReject returns true if the failure is a fee-policy rejection.
func isFeePolicyReject(f *transaction.BroadcastFailure) bool {
	if f == nil {
		return false
	}
	if f.Code == "461" || f.Code == "465" {
		return true
	}
	desc := strings.ToLower(f.Description)
	return strings.Contains(desc, "fee") && (strings.Contains(desc, "too low") || strings.Contains(desc, "insufficient"))
}

// ---------------------------------------------------------------------------
// shouldFallback — error classification for fallback decisions
// ---------------------------------------------------------------------------

// shouldFallback returns true if the broadcast failure warrants retrying
// on a different broadcaster service. This includes:
//
//  1. Transport errors — network failures, timeouts, 5xx, rate limits.
//     The tx is fine; the service is unavailable.
//
//  2. Miner policy differences — fee-too-low rejections (ARC 461/465).
//     Fee policies are miner-specific: ARC may enforce a higher minimum
//     than WoC, so a tx rejected by ARC may succeed via WoC. These are
//     NOT universal application errors like double-spend or bad scripts.
//
// Returns false for truly universal application errors (bad tx structure,
// double-spend, invalid script) where any miner would reject.
func shouldFallback(f *transaction.BroadcastFailure) bool {
	if f == nil {
		return false
	}

	code := f.Code
	desc := strings.ToLower(f.Description)

	// Explicit transport error codes
	switch code {
	case "NETWORK_ERROR", "TIMEOUT":
		return true
	}

	// HTTP 5xx server errors
	if strings.HasPrefix(code, "5") || strings.HasPrefix(code, "HTTP_5") {
		return true
	}

	// HTTP 429 rate limited
	if code == "429" || code == "HTTP_429" {
		return true
	}

	// ARC fee-policy rejections — miner-specific, worth trying fallback.
	// ARC uses 461 (fee too low) and 465 (fee too low after data carrier).
	// These are NOT universal: WoC's fee policy may accept the same tx.
	if code == "461" || code == "465" {
		return true
	}

	// Description-based heuristics for fee rejections
	if strings.Contains(desc, "fee") && (strings.Contains(desc, "too low") || strings.Contains(desc, "insufficient")) {
		return true
	}

	// Description-based heuristics for network-level failures
	transportPatterns := []string{
		"connection refused",
		"no such host",
		"dial tcp",
		"tls handshake",
		"certificate",
		"context deadline",
		"context canceled",
		"i/o timeout",
		"eof",
	}
	for _, pattern := range transportPatterns {
		if strings.Contains(desc, pattern) {
			return true
		}
	}

	// Everything else is a universal application error (409 double-spend,
	// invalid script, malformed tx, etc.) — no point retrying on fallback.
	return false
}
