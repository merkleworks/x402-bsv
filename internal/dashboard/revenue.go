// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package dashboard

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// RevenueStats holds persistent settlement revenue data.
type RevenueStats struct {
	Payments     int64  `json:"payments"`
	TotalSats    int64  `json:"totalSats"`
	LastTxID     string `json:"lastTxid,omitempty"`
	UnsweptCount int    `json:"unsweptCount"` // UTXOs available to sweep
	UnsweptSats  int64  `json:"unsweptSats"`  // total sats available to sweep
}

// RevenueUTXO represents an unswept settlement output.
type RevenueUTXO struct {
	TxID     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	Satoshis uint64 `json:"satoshis"`
	Script   string `json:"script"`
}

// RevenueTracker records settlement revenue persistently in Redis.
// Falls back to in-memory counters when Redis is unavailable.
//
// In addition to aggregate counters (payments, totalSats), the tracker
// maintains a set of "unswept" UTXOs — the payee outputs from each
// settlement transaction. The sweep-revenue handler reads these UTXOs
// directly, eliminating the dependency on WhatsOnChain for UTXO discovery.
type RevenueTracker struct {
	rdb    *redis.Client
	prefix string // e.g. "revenue:"
	logger *slog.Logger

	// Atomic in-memory counters (session fallback + fast reads)
	memPayments  atomic.Int64
	memTotalSats atomic.Int64

	// In-memory unswept UTXO tracking (Redis-backed, loaded at startup)
	mu      sync.RWMutex
	unswept map[string]RevenueUTXO // key: "txid:vout"
}

// NewRevenueTracker creates a new revenue tracker.
// rdb may be nil (falls back to in-memory only).
func NewRevenueTracker(rdb *redis.Client, logger *slog.Logger) *RevenueTracker {
	rt := &RevenueTracker{
		rdb:     rdb,
		prefix:  "revenue:",
		logger:  logger,
		unswept: make(map[string]RevenueUTXO),
	}

	// Load existing totals from Redis into memory for fast reads
	if rdb != nil {
		ctx := context.Background()
		if payments, err := rdb.HGet(ctx, rt.prefix+"totals", "payments").Int64(); err == nil {
			rt.memPayments.Store(payments)
		}
		if totalSats, err := rdb.HGet(ctx, rt.prefix+"totals", "total_sats").Int64(); err == nil {
			rt.memTotalSats.Store(totalSats)
		}

		// Load unswept UTXOs into memory
		if all, err := rdb.HGetAll(ctx, rt.prefix+"unswept").Result(); err == nil {
			for field, value := range all {
				if utxo, ok := parseUnsweptEntry(field, value); ok {
					rt.unswept[field] = utxo
				}
			}
		}

		rt.logger.Info("revenue tracker initialized",
			"payments", rt.memPayments.Load(),
			"total_sats", rt.memTotalSats.Load(),
			"unswept_utxos", len(rt.unswept),
		)
	}

	return rt
}

// RecordSettlement records a successful payment settlement.
// Called by the gatekeeper middleware on 200 OK (payment accepted).
//
// amountSats is the challenge price (for the revenue counter).
// txid, vout, satoshis, scriptHex describe the payee output UTXO
// so it can be swept to treasury later without querying WoC.
func (rt *RevenueTracker) RecordSettlement(amountSats int64, txid string, vout uint32, satoshis uint64, scriptHex string) {
	// Always update in-memory counters (fast, lock-free)
	rt.memPayments.Add(1)
	rt.memTotalSats.Add(amountSats)

	// Track UTXO in memory for sweep
	key := fmt.Sprintf("%s:%d", txid, vout)
	utxo := RevenueUTXO{
		TxID:     txid,
		Vout:     vout,
		Satoshis: satoshis,
		Script:   scriptHex,
	}
	rt.mu.Lock()
	rt.unswept[key] = utxo
	rt.mu.Unlock()

	// Persist to Redis
	if rt.rdb != nil {
		ctx := context.Background()
		pipe := rt.rdb.Pipeline()
		pipe.HIncrBy(ctx, rt.prefix+"totals", "payments", 1)
		pipe.HIncrBy(ctx, rt.prefix+"totals", "total_sats", amountSats)
		pipe.HSet(ctx, rt.prefix+"totals", "last_txid", txid)
		pipe.HSet(ctx, rt.prefix+"totals", "last_update", time.Now().Unix())

		// Store unswept UTXO: field="txid:vout", value="satoshis:scriptHex"
		value := fmt.Sprintf("%d:%s", satoshis, scriptHex)
		pipe.HSet(ctx, rt.prefix+"unswept", key, value)

		if _, err := pipe.Exec(ctx); err != nil {
			rt.logger.Error("failed to persist settlement to Redis", "error", err)
		}

		// Also record in time-series list for history (keep last 1000)
		entry := strconv.FormatInt(time.Now().Unix(), 10) + ":" + strconv.FormatInt(amountSats, 10) + ":" + txid
		pipe2 := rt.rdb.Pipeline()
		pipe2.LPush(ctx, rt.prefix+"history", entry)
		pipe2.LTrim(ctx, rt.prefix+"history", 0, 999)
		pipe2.Exec(ctx)
	}
}

// ListUnsweptUTXOs returns all settlement UTXOs that haven't been swept
// to treasury yet. Returns a copy safe for concurrent use.
func (rt *RevenueTracker) ListUnsweptUTXOs() []RevenueUTXO {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	utxos := make([]RevenueUTXO, 0, len(rt.unswept))
	for _, u := range rt.unswept {
		utxos = append(utxos, u)
	}
	return utxos
}

// MarkSwept removes the given outpoints from the unswept set after a
// successful sweep to treasury. Outpoints should be "txid:vout" strings.
func (rt *RevenueTracker) MarkSwept(outpoints []string) {
	rt.mu.Lock()
	for _, op := range outpoints {
		delete(rt.unswept, op)
	}
	rt.mu.Unlock()

	// Remove from Redis
	if rt.rdb != nil && len(outpoints) > 0 {
		ctx := context.Background()
		if err := rt.rdb.HDel(ctx, rt.prefix+"unswept", outpoints...).Err(); err != nil {
			rt.logger.Error("failed to remove swept UTXOs from Redis", "error", err)
		}
	}
}

// Stats returns current revenue statistics.
func (rt *RevenueTracker) Stats() RevenueStats {
	stats := RevenueStats{
		Payments:  rt.memPayments.Load(),
		TotalSats: rt.memTotalSats.Load(),
	}

	// Unswept UTXO stats
	rt.mu.RLock()
	stats.UnsweptCount = len(rt.unswept)
	for _, u := range rt.unswept {
		stats.UnsweptSats += int64(u.Satoshis)
	}
	rt.mu.RUnlock()

	// Get last txid from Redis if available
	if rt.rdb != nil {
		ctx := context.Background()
		if lastTxID, err := rt.rdb.HGet(ctx, rt.prefix+"totals", "last_txid").Result(); err == nil {
			stats.LastTxID = lastTxID
		}
	}

	return stats
}

// parseUnsweptEntry parses a Redis hash entry for an unswept UTXO.
//
//	field format: "<64-char-txid>:<vout>"
//	value format: "<satoshis>:<scriptHex>"
func parseUnsweptEntry(field, value string) (RevenueUTXO, bool) {
	// field must be at least 64 (txid) + 1 (:) + 1 (vout digit) = 66 chars
	if len(field) < 66 || field[64] != ':' {
		return RevenueUTXO{}, false
	}
	txid := field[:64]
	vout, err := strconv.ParseUint(field[65:], 10, 32)
	if err != nil {
		return RevenueUTXO{}, false
	}

	// value: "satoshis:scriptHex"
	colonIdx := strings.Index(value, ":")
	if colonIdx < 1 {
		return RevenueUTXO{}, false
	}
	satoshis, err := strconv.ParseUint(value[:colonIdx], 10, 64)
	if err != nil {
		return RevenueUTXO{}, false
	}
	scriptHex := value[colonIdx+1:]

	return RevenueUTXO{
		TxID:     txid,
		Vout:     uint32(vout),
		Satoshis: satoshis,
		Script:   scriptHex,
	}, true
}
