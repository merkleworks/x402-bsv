// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package pool

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/redis/go-redis/v9"
)

// Compile-time check that RedisPool implements Pool.
var _ Pool = (*RedisPool)(nil)

// Redis key suffixes — matches the Node.js redis-utxo-manager.js data model.
const (
	keyAvailable = "available" // ZSET: member="txid:vout", score=satoshis
	keySpent     = "spent"     // SET:  member="txid:vout"
	keyDetails   = "details"   // HASH: per-UTXO metadata at {prefix}details:{txid}:{vout}
	keyStats     = "stats"     // HASH: aggregate stats
	keyNextIndex = "nextIndex" // STRING: deterministic round-robin counter
)

// Lua script for atomic spend: ZREM from available + SADD to spent + update details + stats.
// Returns 1 if successful, 0 if the UTXO was not in available.
var spendScript = redis.NewScript(`
local removed = redis.call('ZREM', KEYS[1], ARGV[1])
if removed == 0 then return 0 end
redis.call('SADD', KEYS[2], ARGV[2])
redis.call('HSET', KEYS[3], 'status', 'spent', 'spentAt', ARGV[3])
redis.call('HINCRBY', KEYS[4], 'availableUtxos', -1)
redis.call('HINCRBY', KEYS[4], 'spentUtxos', 1)
redis.call('HSET', KEYS[4], 'lastUpdate', ARGV[3])
return 1
`)

// Lua script for atomic lease: score check + HSET status + lease timestamps.
// Returns 1 if leased successfully, 0 if not available.
var leaseScript = redis.NewScript(`
local status = redis.call('HGET', KEYS[1], 'status')
if status ~= 'available' then return 0 end
redis.call('HSET', KEYS[1], 'status', 'leased', 'leasedAt', ARGV[1], 'expiresAt', ARGV[2])
return 1
`)

// RedisPool implements Pool backed by Redis.
// Matches the Node.js redis-utxo-manager.js data model exactly.
type RedisPool struct {
	rdb      *redis.Client
	prefix   string // key prefix, e.g. "nonce:" or "fee:"
	key      *ec.PrivateKey
	address  *script.Address
	mainnet  bool
	leaseTTL time.Duration
	logger   *slog.Logger
	mu       sync.Mutex // protects lease counter reads for deterministic selection
}

// NewRedisPool creates a Redis-backed UTXO pool.
// prefix determines the key namespace (e.g., "nonce:" or "fee:").
func NewRedisPool(rdb *redis.Client, prefix string, key *ec.PrivateKey, mainnet bool, leaseTTL time.Duration) (*RedisPool, error) {
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}

	return &RedisPool{
		rdb:      rdb,
		prefix:   prefix,
		key:      key,
		address:  addr,
		mainnet:  mainnet,
		leaseTTL: leaseTTL,
		logger:   slog.Default().With("component", "redis-pool", "prefix", prefix),
	}, nil
}

// Address returns the BSV address that owns all UTXOs in this pool.
func (p *RedisPool) Address() string {
	return p.address.AddressString
}

// LockingScriptHex returns the P2PKH locking script hex for the pool address.
func (p *RedisPool) LockingScriptHex() (string, error) {
	s, err := p2pkh.Lock(p.address)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(*s), nil
}

// k builds a full Redis key from prefix + suffix.
func (p *RedisPool) k(suffix string) string {
	return p.prefix + suffix
}

// detailKey builds a Redis key for UTXO details.
func (p *RedisPool) detailKey(txid string, vout uint32) string {
	return p.prefix + keyDetails + ":" + txid + ":" + uitoa(vout)
}

// Lease returns a single available UTXO using deterministic round-robin selection.
func (p *RedisPool) Lease() (*UTXO, error) {
	ctx := context.Background()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Get pool size
	count, err := p.rdb.ZCard(ctx, p.k(keyAvailable)).Result()
	if err != nil {
		return nil, fmt.Errorf("zcard: %w", err)
	}
	if count == 0 {
		return nil, fmt.Errorf("no UTXOs available (pool exhausted)")
	}

	// Deterministic round-robin: INCR counter, mod by pool size
	idx, err := p.rdb.Incr(ctx, p.k(keyNextIndex)).Result()
	if err != nil {
		return nil, fmt.Errorf("incr index: %w", err)
	}
	offset := idx % count

	// Get the member at this offset
	members, err := p.rdb.ZRange(ctx, p.k(keyAvailable), offset, offset).Result()
	if err != nil || len(members) == 0 {
		// Fallback: try offset 0
		members, err = p.rdb.ZRange(ctx, p.k(keyAvailable), 0, 0).Result()
		if err != nil || len(members) == 0 {
			return nil, fmt.Errorf("no UTXOs available after fallback")
		}
	}

	outpoint := members[0]
	parts := strings.SplitN(outpoint, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid outpoint format: %s", outpoint)
	}
	txid := parts[0]
	vout, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse vout: %w", err)
	}

	// Atomic lease via Lua script
	now := time.Now()
	expiresAt := now.Add(p.leaseTTL)
	detKey := p.detailKey(txid, uint32(vout))

	result, err := leaseScript.Run(ctx, p.rdb, []string{detKey},
		now.Unix(), expiresAt.Unix(),
	).Int64()
	if err != nil {
		return nil, fmt.Errorf("lease script: %w", err)
	}
	if result == 0 {
		// UTXO wasn't available (race condition) — try scanning for any available
		return p.leaseLinearScan(ctx, now, expiresAt)
	}

	// Remove from available ZSET (leased UTXOs are tracked in details only)
	p.rdb.ZRem(ctx, p.k(keyAvailable), outpoint)

	// Load full UTXO details
	utxo, err := p.loadUTXO(ctx, txid, uint32(vout))
	if err != nil {
		return nil, err
	}

	// Fallback safeguard: reject synthetic UTXOs that should not be in this pool.
	// With mode-namespaced pools, this should never trigger — defense-in-depth only.
	if utxo.Synthetic {
		p.logger.Warn("fallback: rejecting synthetic UTXO in pool",
			"txid", txid, "vout", vout, "prefix", p.prefix)
		// Already removed from available ZSET above; fall through to linear scan
		return p.leaseLinearScan(ctx, now, expiresAt)
	}

	utxo.Status = StatusLeased
	utxo.LeasedAt = now
	utxo.ExpiresAt = expiresAt

	return utxo, nil
}

// maxSyntheticSkips bounds how many synthetic UTXOs leaseLinearScan will
// evict before giving up. Prevents runaway iteration if a pool is heavily
// contaminated. The integrity check (pool.CheckIntegrity) should be used
// to clean up such pools in bulk.
const maxSyntheticSkips = 10

// leaseLinearScan is a fallback when round-robin hits a non-available UTXO.
func (p *RedisPool) leaseLinearScan(ctx context.Context, now, expiresAt time.Time) (*UTXO, error) {
	members, err := p.rdb.ZRange(ctx, p.k(keyAvailable), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange scan: %w", err)
	}

	syntheticSkips := 0

	for _, outpoint := range members {
		parts := strings.SplitN(outpoint, ":", 2)
		if len(parts) != 2 {
			continue
		}
		txid := parts[0]
		vout, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			continue
		}

		detKey := p.detailKey(txid, uint32(vout))
		result, err := leaseScript.Run(ctx, p.rdb, []string{detKey},
			now.Unix(), expiresAt.Unix(),
		).Int64()
		if err != nil || result == 0 {
			continue
		}

		p.rdb.ZRem(ctx, p.k(keyAvailable), outpoint)

		utxo, err := p.loadUTXO(ctx, txid, uint32(vout))
		if err != nil {
			return nil, err
		}

		// Fallback safeguard: skip synthetic UTXOs with bounded iteration
		if utxo.Synthetic {
			p.logger.Warn("fallback: skipping synthetic UTXO in linear scan",
				"txid", txid, "vout", vout, "prefix", p.prefix)
			syntheticSkips++
			if syntheticSkips >= maxSyntheticSkips {
				return nil, fmt.Errorf("too many synthetic UTXOs in pool %s — run integrity check", p.prefix)
			}
			continue
		}

		utxo.Status = StatusLeased
		utxo.LeasedAt = now
		utxo.ExpiresAt = expiresAt
		return utxo, nil
	}

	return nil, fmt.Errorf("no UTXOs available (pool exhausted)")
}

// LeaseN leases exactly n UTXOs.
func (p *RedisPool) LeaseN(n int) ([]*UTXO, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be positive, got %d", n)
	}

	ctx := context.Background()
	p.mu.Lock()
	defer p.mu.Unlock()

	// Check availability
	count, err := p.rdb.ZCard(ctx, p.k(keyAvailable)).Result()
	if err != nil {
		return nil, fmt.Errorf("zcard: %w", err)
	}
	if count < int64(n) {
		return nil, fmt.Errorf("need %d UTXOs, only %d available", n, count)
	}

	// Get the first n members
	members, err := p.rdb.ZRange(ctx, p.k(keyAvailable), 0, int64(n-1)).Result()
	if err != nil {
		return nil, fmt.Errorf("zrange: %w", err)
	}
	if len(members) < n {
		return nil, fmt.Errorf("need %d UTXOs, got %d from zrange", n, len(members))
	}

	now := time.Now()
	expiresAt := now.Add(p.leaseTTL)
	leased := make([]*UTXO, 0, n)

	for _, outpoint := range members[:n] {
		parts := strings.SplitN(outpoint, ":", 2)
		if len(parts) != 2 {
			continue
		}
		txid := parts[0]
		vout, err := strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			continue
		}

		detKey := p.detailKey(txid, uint32(vout))
		result, err := leaseScript.Run(ctx, p.rdb, []string{detKey},
			now.Unix(), expiresAt.Unix(),
		).Int64()
		if err != nil || result == 0 {
			continue // Skip if already claimed
		}

		p.rdb.ZRem(ctx, p.k(keyAvailable), outpoint)

		utxo, err := p.loadUTXO(ctx, txid, uint32(vout))
		if err != nil {
			return nil, err
		}
		utxo.Status = StatusLeased
		utxo.LeasedAt = now
		utxo.ExpiresAt = expiresAt
		leased = append(leased, utxo)
	}

	if len(leased) < n {
		// Return leased ones back to available (rollback)
		for _, u := range leased {
			p.returnToAvailable(ctx, u)
		}
		return nil, fmt.Errorf("could only lease %d of %d requested UTXOs", len(leased), n)
	}

	return leased, nil
}

// returnToAvailable returns a leased UTXO back to available state.
func (p *RedisPool) returnToAvailable(ctx context.Context, u *UTXO) {
	outpoint := u.Outpoint()
	p.rdb.ZAdd(ctx, p.k(keyAvailable), redis.Z{Score: float64(u.Satoshis), Member: outpoint})
	detKey := p.detailKey(u.TxID, u.Vout)
	p.rdb.HSet(ctx, detKey, "status", "available")
	p.rdb.HDel(ctx, detKey, "leasedAt", "expiresAt")
}

// Lookup returns the UTXO for a given outpoint, or nil if not found.
func (p *RedisPool) Lookup(txid string, vout uint32) *UTXO {
	ctx := context.Background()
	utxo, err := p.loadUTXO(ctx, txid, vout)
	if err != nil {
		return nil
	}
	return utxo
}

// loadUTXO loads full UTXO metadata from Redis.
func (p *RedisPool) loadUTXO(ctx context.Context, txid string, vout uint32) (*UTXO, error) {
	detKey := p.detailKey(txid, vout)
	data, err := p.rdb.HGetAll(ctx, detKey).Result()
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("UTXO not found: %s:%d", txid, vout)
	}

	satoshis, _ := strconv.ParseUint(data["satoshis"], 10, 64)
	status := Status(data["status"])

	utxo := &UTXO{
		TxID:     txid,
		Vout:     vout,
		Script:   data["script"],
		Satoshis: satoshis,
		Status:   status,
	}

	if leasedAtStr, ok := data["leasedAt"]; ok && leasedAtStr != "" {
		ts, _ := strconv.ParseInt(leasedAtStr, 10, 64)
		utxo.LeasedAt = time.Unix(ts, 0)
	}
	if expiresAtStr, ok := data["expiresAt"]; ok && expiresAtStr != "" {
		ts, _ := strconv.ParseInt(expiresAtStr, 10, 64)
		utxo.ExpiresAt = time.Unix(ts, 0)
	}

	// Mode-segregation metadata
	if data["synthetic"] == "true" {
		utxo.Synthetic = true
	}
	utxo.OriginMode = data["origin_mode"]

	// Profile B template metadata
	if tmpl, ok := data["rawtx_template"]; ok {
		utxo.RawTxTemplate = tmpl
	}
	if priceStr, ok := data["template_price_sats"]; ok && priceStr != "" {
		utxo.TemplatePriceSats, _ = strconv.ParseUint(priceStr, 10, 64)
	}
	if cls, ok := data["endpoint_class"]; ok {
		utxo.EndpointClass = cls
	}
	if verStr, ok := data["template_version"]; ok && verStr != "" {
		v, _ := strconv.ParseUint(verStr, 10, 32)
		utxo.TemplateVersion = uint32(v)
	}

	return utxo, nil
}

// MarkSpent atomically marks a UTXO as spent.
// Handles three cases:
//  1. UTXO is in the available ZSET → Lua script removes it and marks spent
//  2. UTXO is leased (removed from available) → script returns 0, fallback marks spent
//  3. UTXO is already spent → fallback is idempotent (SADD to spent set, HSET status)
//
// The fallback is critical: without it, leased UTXOs skip the spent state entirely.
// The reclaim loop sees status="leased" (not "spent") and returns the UTXO to
// available, creating a zombie that causes txn-mempool-conflict on reuse.
func (p *RedisPool) MarkSpent(txid string, vout uint32) {
	ctx := context.Background()
	outpoint := txid + ":" + uitoa(vout)
	now := time.Now().Unix()

	detKey := p.detailKey(txid, vout)
	result, err := spendScript.Run(ctx, p.rdb, []string{
		p.k(keyAvailable), // KEYS[1]
		p.k(keySpent),     // KEYS[2]
		detKey,            // KEYS[3]
		p.k(keyStats),     // KEYS[4]
	},
		outpoint, // ARGV[1] - ZSET member
		outpoint, // ARGV[2] - SET member
		now,      // ARGV[3] - timestamp
		0,        // ARGV[4] - satoshis (unused in current script)
	).Int64()

	if err != nil || result == 0 {
		// UTXO was not in the available ZSET — either it's currently leased
		// (removed from available during Lease()) or already spent.
		// Mark as spent directly so the reclaim loop won't resurrect it.
		p.rdb.HSet(ctx, detKey, "status", "spent", "spentAt", now)
		p.rdb.SAdd(ctx, p.k(keySpent), outpoint)
	}
}

// AddExisting adds pre-existing UTXOs to the Redis pool.
func (p *RedisPool) AddExisting(utxos []UTXO) {
	ctx := context.Background()
	pipe := p.rdb.Pipeline()

	for i := range utxos {
		u := &utxos[i]
		u.Status = StatusAvailable
		outpoint := u.Outpoint()
		detKey := p.detailKey(u.TxID, u.Vout)

		// Add to available ZSET (score = satoshis)
		pipe.ZAdd(ctx, p.k(keyAvailable), redis.Z{
			Score:  float64(u.Satoshis),
			Member: outpoint,
		})

		// Store UTXO details
		pipe.HSet(ctx, detKey,
			"txid", u.TxID,
			"vout", u.Vout,
			"script", u.Script,
			"satoshis", u.Satoshis,
			"status", string(StatusAvailable),
		)

		// Mode-segregation metadata (synthetic provenance tracking)
		if u.Synthetic {
			pipe.HSet(ctx, detKey,
				"synthetic", "true",
				"origin_mode", u.OriginMode,
			)
		}

		// Profile B template metadata (stored alongside nonce identity)
		if u.RawTxTemplate != "" {
			pipe.HSet(ctx, detKey,
				"rawtx_template", u.RawTxTemplate,
				"template_price_sats", u.TemplatePriceSats,
				"endpoint_class", u.EndpointClass,
				"template_version", u.TemplateVersion,
			)
		}
	}

	// Update stats
	pipe.HIncrBy(ctx, p.k(keyStats), "availableUtxos", int64(len(utxos)))
	pipe.HIncrBy(ctx, p.k(keyStats), "totalAdded", int64(len(utxos)))
	pipe.HSet(ctx, p.k(keyStats), "lastUpdate", time.Now().Unix())

	_, err := pipe.Exec(ctx)
	if err != nil {
		p.logger.Error("failed to add UTXOs to Redis", "error", err, "count", len(utxos))
	}
}

// ListAvailable returns all available UTXOs with their full metadata.
// Used at startup for operations like Profile B template generation
// that need to iterate over existing pool contents.
func (p *RedisPool) ListAvailable() ([]UTXO, error) {
	ctx := context.Background()
	members, err := p.rdb.ZRangeByScore(ctx, p.k(keyAvailable), &redis.ZRangeBy{
		Min: "-inf",
		Max: "+inf",
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to list available UTXOs: %w", err)
	}

	utxos := make([]UTXO, 0, len(members))
	for _, outpoint := range members {
		parts := strings.SplitN(outpoint, ":", 2)
		if len(parts) != 2 {
			continue
		}
		vout64, _ := strconv.ParseUint(parts[1], 10, 32)
		utxo, err := p.loadUTXO(ctx, parts[0], uint32(vout64))
		if err != nil {
			p.logger.Warn("skipping UTXO during list", "outpoint", outpoint, "error", err)
			continue
		}
		utxos = append(utxos, *utxo)
	}
	return utxos, nil
}

// UpdateTemplates writes template metadata to existing UTXOs in Redis
// without modifying their availability status or pool stats. Used at startup
// when Profile B template generation is applied to pre-existing nonce UTXOs.
func (p *RedisPool) UpdateTemplates(utxos []UTXO) error {
	ctx := context.Background()
	pipe := p.rdb.Pipeline()

	for i := range utxos {
		u := &utxos[i]
		if u.RawTxTemplate == "" {
			continue
		}
		detKey := p.detailKey(u.TxID, u.Vout)
		pipe.HSet(ctx, detKey,
			"rawtx_template", u.RawTxTemplate,
			"template_price_sats", u.TemplatePriceSats,
			"template_version", u.TemplateVersion,
		)
	}

	_, err := pipe.Exec(ctx)
	return err
}

// Available returns the count of available UTXOs.
func (p *RedisPool) Available() int {
	ctx := context.Background()
	count, err := p.rdb.ZCard(ctx, p.k(keyAvailable)).Result()
	if err != nil {
		p.logger.Error("failed to get available count", "error", err)
		return 0
	}
	return int(count)
}

// Stats returns pool statistics including UTXO denomination.
func (p *RedisPool) Stats() PoolStats {
	ctx := context.Background()

	available, _ := p.rdb.ZCard(ctx, p.k(keyAvailable)).Result()
	spent, _ := p.rdb.SCard(ctx, p.k(keySpent)).Result()

	// Leased = total added - available - spent
	statsData, _ := p.rdb.HGetAll(ctx, p.k(keyStats)).Result()
	totalAdded, _ := strconv.ParseInt(statsData["totalAdded"], 10, 64)

	leased := totalAdded - available - spent
	if leased < 0 {
		leased = 0
	}

	// Get UTXO denomination from the first available UTXO.
	// All UTXOs in a pool have the same denomination (from fan-out).
	var utxoValue uint64
	members, err := p.rdb.ZRangeByScore(ctx, p.k(keyAvailable), &redis.ZRangeBy{
		Min: "-inf", Max: "+inf", Count: 1,
	}).Result()
	if err == nil && len(members) > 0 {
		outpoint := members[0]
		detKey := p.k("details:" + outpoint)
		if satsStr, err := p.rdb.HGet(ctx, detKey, "satoshis").Result(); err == nil {
			val, _ := strconv.ParseUint(satsStr, 10, 64)
			utxoValue = val
		}
	}

	// Quarantined UTXOs (from integrity check)
	quarantined, _ := p.rdb.SCard(ctx, p.k("quarantined")).Result()

	return PoolStats{
		Total:       int(totalAdded),
		Available:   int(available),
		Leased:      int(leased),
		Spent:       int(spent),
		Quarantined: int(quarantined),
		UTXOValue:   utxoValue,
	}
}

// IsAvailable returns true if the outpoint is currently in the available ZSET.
// Uses ZSCORE for a lightweight O(1) membership check without loading full metadata.
func (p *RedisPool) IsAvailable(txid string, vout uint32) bool {
	ctx := context.Background()
	outpoint := txid + ":" + uitoa(vout)
	err := p.rdb.ZScore(ctx, p.k(keyAvailable), outpoint).Err()
	return err == nil // nil = member exists, redis.Nil = not found
}

// ListLeased returns all UTXOs currently in the leased state.
// Used by pool reconciliation to check leased UTXOs against on-chain state.
func (p *RedisPool) ListLeased() ([]UTXO, error) {
	ctx := context.Background()
	pattern := p.prefix + keyDetails + ":*"
	iter := p.rdb.Scan(ctx, 0, pattern, 100).Iterator()

	var utxos []UTXO
	for iter.Next(ctx) {
		detKey := iter.Val()
		data, err := p.rdb.HGetAll(ctx, detKey).Result()
		if err != nil || len(data) == 0 {
			continue
		}
		if data["status"] != "leased" {
			continue
		}

		txid := data["txid"]
		vout, _ := strconv.ParseUint(data["vout"], 10, 32)
		satoshis, _ := strconv.ParseUint(data["satoshis"], 10, 64)

		u := UTXO{
			TxID:     txid,
			Vout:     uint32(vout),
			Script:   data["script"],
			Satoshis: satoshis,
			Status:   StatusLeased,
		}

		if leasedAtStr, ok := data["leasedAt"]; ok && leasedAtStr != "" {
			ts, _ := strconv.ParseInt(leasedAtStr, 10, 64)
			u.LeasedAt = time.Unix(ts, 0)
		}
		if expiresAtStr, ok := data["expiresAt"]; ok && expiresAtStr != "" {
			ts, _ := strconv.ParseInt(expiresAtStr, 10, 64)
			u.ExpiresAt = time.Unix(ts, 0)
		}

		utxos = append(utxos, u)
	}

	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan leased UTXOs: %w", err)
	}
	return utxos, nil
}

// ReleaseLease returns a leased UTXO back to available state.
// Only acts if the UTXO is currently in leased status (safety check).
// Used by pool reconciliation for stale leases that never confirmed.
func (p *RedisPool) ReleaseLease(txid string, vout uint32) {
	ctx := context.Background()
	detKey := p.detailKey(txid, vout)

	data, err := p.rdb.HGetAll(ctx, detKey).Result()
	if err != nil || data["status"] != "leased" {
		return
	}

	satoshis, _ := strconv.ParseUint(data["satoshis"], 10, 64)
	outpoint := txid + ":" + uitoa(vout)

	pipe := p.rdb.Pipeline()
	pipe.ZAdd(ctx, p.k(keyAvailable), redis.Z{Score: float64(satoshis), Member: outpoint})
	pipe.HSet(ctx, detKey, "status", "available")
	pipe.HDel(ctx, detKey, "leasedAt", "expiresAt")
	pipe.Exec(ctx)
}

// StartReclaimLoop starts a background goroutine that reclaims expired leases.
// For Redis pools, we scan details hashes for leased UTXOs past their expiry.
func (p *RedisPool) StartReclaimLoop(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.reclaimExpired()
			case <-stop:
				return
			}
		}
	}()
}

// reclaimExpired scans for leased UTXOs that have passed their expiry and returns them to available.
func (p *RedisPool) reclaimExpired() {
	ctx := context.Background()
	now := time.Now().Unix()
	reclaimed := 0

	// Scan all detail keys for this prefix
	pattern := p.prefix + keyDetails + ":*"
	iter := p.rdb.Scan(ctx, 0, pattern, 100).Iterator()

	for iter.Next(ctx) {
		detKey := iter.Val()
		data, err := p.rdb.HGetAll(ctx, detKey).Result()
		if err != nil {
			continue
		}

		if data["status"] != "leased" {
			continue
		}

		expiresAt, err := strconv.ParseInt(data["expiresAt"], 10, 64)
		if err != nil || now < expiresAt {
			continue
		}

		// Expired lease — return to available
		txid := data["txid"]
		vout, _ := strconv.ParseUint(data["vout"], 10, 32)
		satoshis, _ := strconv.ParseUint(data["satoshis"], 10, 64)

		outpoint := txid + ":" + uitoa(uint32(vout))

		pipe := p.rdb.Pipeline()
		pipe.ZAdd(ctx, p.k(keyAvailable), redis.Z{Score: float64(satoshis), Member: outpoint})
		pipe.HSet(ctx, detKey, "status", "available")
		pipe.HDel(ctx, detKey, "leasedAt", "expiresAt")
		_, err = pipe.Exec(ctx)
		if err == nil {
			reclaimed++
		}
	}

	if reclaimed > 0 {
		p.logger.Info("reclaimed expired leases", "count", reclaimed)
	}
}
