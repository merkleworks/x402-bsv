// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package treasury

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/redis/go-redis/v9"
)

// Compile-time check that TreasuryWatcher implements FundingSource.
var _ FundingSource = (*TreasuryWatcher)(nil)

const (
	redisKeyUTXOs    = "treasury:utxos"    // HASH: field="txid:vout", value=JSON
	redisKeyLastPoll = "treasury:lastPoll" // STRING: ISO 8601
	redisKeyMempool  = "treasury:mempool"  // HASH: field="txid:vout", value=JSON
	redisKeySpent    = "treasury:spent"    // SET: members are "txid:vout"
	redisKeyLeased   = "treasury:leased"   // HASH: field="txid:vout", value=JSON lease record
)

// TreasuryLease represents an active lease on a treasury outpoint.
type TreasuryLease struct {
	TxID       string    `json:"txid"`
	Vout       uint32    `json:"vout"`
	Satoshis   uint64    `json:"satoshis"`
	LeasedAt   time.Time `json:"leased_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LeaseOwner string    `json:"lease_owner"` // "fanout", "sweep", "refill"
}

// FundingUTXOStatus indicates whether a treasury UTXO is confirmed or in the mempool.
type FundingUTXOStatus string

const (
	UTXOStatusConfirmed FundingUTXOStatus = "confirmed"
	UTXOStatusMempool   FundingUTXOStatus = "mempool"
)

// FundingUTXOWithStatus wraps a FundingUTXO with its confirmation status.
type FundingUTXOWithStatus struct {
	FundingUTXO
	Status FundingUTXOStatus `json:"status"`
}

// wocUnspent is the JSON response item from WoC's unspent endpoint.
type wocUnspent struct {
	Height int    `json:"height"`
	TxPos  int    `json:"tx_pos"`
	TxHash string `json:"tx_hash"`
	Value  int64  `json:"value"`
}

// TreasuryWatcher polls WhatsOnChain for unspent UTXOs at the treasury address
// and stores them in Redis (persistent) with an in-memory read cache.
// Falls back to in-memory only when Redis is not available.
//
// Additionally tracks locally-broadcast change outputs as "mempool" UTXOs,
// maintains lease discipline to prevent double-spend, and tracks spent outpoints.
type TreasuryWatcher struct {
	mu       sync.RWMutex
	utxos    []FundingUTXO            // confirmed UTXOs from WoC polling
	mempool  []FundingUTXO            // locally-tracked mempool UTXOs (change outputs)
	spent    map[string]bool          // "txid:vout" → consumed outpoints
	leased   map[string]TreasuryLease // "txid:vout" → active leases
	lastPoll time.Time
	lastErr  error
	leaseTTL time.Duration

	httpClient *http.Client
	baseURL    string // e.g. "https://api.whatsonchain.com/v1/bsv/main"
	address    string // treasury P2PKH address
	scriptHex  string // hex locking script (derived once)
	interval   time.Duration
	rdb        *redis.Client // nil if Redis not enabled
	logger     *slog.Logger

	// Rate-limit backoff: consecutive poll errors increase the wait before the
	// next attempt. Reset to 0 on any successful poll.
	consecutiveErrors int
}

// NewTreasuryWatcher creates a watcher for the given treasury address.
// baseURL is the WoC-compatible API base URL (e.g. "https://api.whatsonchain.com/v1/bsv/main").
// rdb may be nil (falls back to in-memory only).
func NewTreasuryWatcher(
	mainnet bool,
	address string,
	treasuryKey *ec.PrivateKey,
	interval time.Duration,
	rdb *redis.Client,
	baseURL string,
) (*TreasuryWatcher, error) {
	// Derive locking script hex from the treasury key
	addr, err := script.NewAddressFromPublicKey(treasuryKey.PubKey(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}
	lockScript, err := p2pkh.Lock(addr)
	if err != nil {
		return nil, fmt.Errorf("derive locking script: %w", err)
	}
	scriptHex := fmt.Sprintf("%x", *lockScript)

	return &TreasuryWatcher{
		utxos:      []FundingUTXO{},
		mempool:    []FundingUTXO{},
		spent:      make(map[string]bool),
		leased:     make(map[string]TreasuryLease),
		leaseTTL:   2 * time.Minute,
		httpClient: &http.Client{Timeout: 15 * time.Second},
		baseURL:    baseURL,
		address:    address,
		scriptHex:  scriptHex,
		interval:   interval,
		rdb:        rdb,
		logger:     slog.Default().With("component", "treasury-watcher"),
	}, nil
}

// Start hydrates from Redis (if available), polls WoC immediately,
// then polls every interval. Stops when stop is closed.
func (tw *TreasuryWatcher) Start(stop <-chan struct{}) {
	tw.logger.Info("starting treasury watcher",
		"address", tw.address,
		"interval", tw.interval,
	)

	// Hydrate from Redis on startup
	if tw.rdb != nil {
		if err := tw.hydrateFromRedis(); err != nil {
			tw.logger.Warn("failed to hydrate from Redis", "error", err)
		} else {
			tw.mu.RLock()
			count := len(tw.utxos)
			tw.mu.RUnlock()
			if count > 0 {
				tw.logger.Info("hydrated from Redis", "utxo_count", count)
			}
		}

		// Hydrate mempool, spent, and leased state
		if err := tw.hydrateMempool(); err != nil {
			tw.logger.Warn("failed to hydrate mempool state from Redis", "error", err)
		}
	}

	// Poll immediately
	if err := tw.poll(); err != nil {
		tw.logger.Warn("initial poll failed", "error", err)
	}

	go func() {
		ticker := time.NewTicker(tw.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := tw.poll(); err != nil {
					tw.consecutiveErrors++
					// Exponential backoff: skip polls when rate-limited (2^n intervals, max ~5 min)
					backoffMultiplier := 1 << min(tw.consecutiveErrors, 5)
					tw.logger.Warn("poll failed, backing off",
						"error", err,
						"consecutive_errors", tw.consecutiveErrors,
						"next_poll_delay", time.Duration(backoffMultiplier)*tw.interval,
					)
					// Sleep additional time for backoff (ticker continues ticking)
					select {
					case <-time.After(time.Duration(backoffMultiplier-1) * tw.interval):
					case <-stop:
						tw.logger.Info("treasury watcher stopped")
						return
					}
				} else {
					if tw.consecutiveErrors > 0 {
						tw.logger.Info("poll recovered after backoff",
							"previous_errors", tw.consecutiveErrors)
					}
					tw.consecutiveErrors = 0
				}
			case <-stop:
				tw.logger.Info("treasury watcher stopped")
				return
			}
		}
	}()
}

// poll queries WoC for unspent UTXOs, updates Redis and in-memory cache,
// then reconciles mempool/spent/leased state.
func (tw *TreasuryWatcher) poll() error {
	url := tw.baseURL + "/address/" + tw.address + "/unspent"

	resp, err := tw.httpClient.Get(url)
	if err != nil {
		tw.mu.Lock()
		tw.lastErr = fmt.Errorf("WoC request failed: %w", err)
		tw.mu.Unlock()
		return tw.lastErr
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		tw.mu.Lock()
		tw.lastErr = fmt.Errorf("read response body: %w", err)
		tw.mu.Unlock()
		return tw.lastErr
	}

	// WoC returns 404 for addresses with no history, treat as empty
	if resp.StatusCode == http.StatusNotFound {
		tw.mu.Lock()
		tw.utxos = []FundingUTXO{}
		tw.lastPoll = time.Now()
		tw.lastErr = nil
		mempoolSnap := snapshotMempool(tw.mempool)
		spentSnap := snapshotSpent(tw.spent)
		leasedSnap := snapshotLeases(tw.leased)
		tw.mu.Unlock()

		tw.persistToRedis([]FundingUTXO{})
		tw.persistMempool(mempoolSnap)
		tw.persistSpent(spentSnap)
		tw.persistLeased(leasedSnap)
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		tw.mu.Lock()
		tw.lastErr = fmt.Errorf("WoC returned HTTP %d: %s", resp.StatusCode, string(body))
		tw.mu.Unlock()
		return tw.lastErr
	}

	var items []wocUnspent
	if err := json.Unmarshal(body, &items); err != nil {
		tw.mu.Lock()
		tw.lastErr = fmt.Errorf("parse WoC response: %w", err)
		tw.mu.Unlock()
		return tw.lastErr
	}

	// Convert to FundingUTXO slice
	utxos := make([]FundingUTXO, 0, len(items))
	for _, item := range items {
		if item.Value <= 0 {
			continue
		}
		utxos = append(utxos, FundingUTXO{
			TxID:     item.TxHash,
			Vout:     uint32(item.TxPos),
			Script:   tw.scriptHex,
			Satoshis: uint64(item.Value),
		})
	}

	// Sort by value descending (largest first for GetFunding efficiency)
	sort.Slice(utxos, func(i, j int) bool {
		return utxos[i].Satoshis > utxos[j].Satoshis
	})

	// Update in-memory cache + reconcile mempool/spent/leased state
	tw.mu.Lock()
	tw.utxos = utxos
	tw.lastPoll = time.Now()
	tw.lastErr = nil

	// --- Reconciliation ---
	// INVARIANT: When a mempool UTXO becomes confirmed, the outpoint key (txid:vout)
	// remains identical. Therefore existing leases remain valid, existing spent flags
	// remain valid, and no lease migration is required.

	// Build confirmed outpoint set
	confirmedSet := make(map[string]bool, len(utxos))
	for _, u := range utxos {
		confirmedSet[fmt.Sprintf("%s:%d", u.TxID, u.Vout)] = true
	}

	// Promote mempool UTXOs that now appear in confirmed (remove from mempool)
	surviving := tw.mempool[:0]
	for _, m := range tw.mempool {
		key := fmt.Sprintf("%s:%d", m.TxID, m.Vout)
		if !confirmedSet[key] {
			surviving = append(surviving, m)
		}
	}
	tw.mempool = surviving

	// Clean spent outpoints no longer in confirmed (spend confirmed on-chain)
	for op := range tw.spent {
		if !confirmedSet[op] {
			delete(tw.spent, op)
		}
	}

	// Reclaim expired leases
	now := time.Now()
	for key, lease := range tw.leased {
		if now.After(lease.ExpiresAt) {
			tw.logger.Warn("reclaiming expired treasury lease",
				"outpoint", key, "owner", lease.LeaseOwner,
				"leased_at", lease.LeasedAt)
			delete(tw.leased, key)
		}
	}

	// Snapshot state for persistence outside the lock
	mempoolSnap := snapshotMempool(tw.mempool)
	spentSnap := snapshotSpent(tw.spent)
	leasedSnap := snapshotLeases(tw.leased)
	tw.mu.Unlock()

	// Persist to Redis (outside lock)
	tw.persistToRedis(utxos)
	tw.persistMempool(mempoolSnap)
	tw.persistSpent(spentSnap)
	tw.persistLeased(leasedSnap)

	if len(utxos) > 0 {
		tw.logger.Info("poll complete", "utxo_count", len(utxos), "mempool_count", len(mempoolSnap))
	}

	return nil
}

// ── Snapshot Helpers ──────────────────────────────────────────────────────────

func snapshotMempool(src []FundingUTXO) []FundingUTXO {
	out := make([]FundingUTXO, len(src))
	copy(out, src)
	return out
}

func snapshotSpent(src map[string]bool) map[string]bool {
	out := make(map[string]bool, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func snapshotLeases(src map[string]TreasuryLease) map[string]TreasuryLease {
	out := make(map[string]TreasuryLease, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// ── Persistence (called OUTSIDE the mutex, accepts snapshots) ─────────────────

// persistToRedis writes confirmed UTXOs to Redis atomically (delete old + set new).
func (tw *TreasuryWatcher) persistToRedis(utxos []FundingUTXO) {
	if tw.rdb == nil {
		return
	}

	ctx := context.Background()
	pipe := tw.rdb.Pipeline()

	pipe.Del(ctx, redisKeyUTXOs)
	for _, u := range utxos {
		field := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		data, err := json.Marshal(u)
		if err != nil {
			tw.logger.Error("marshal UTXO for Redis", "error", err)
			continue
		}
		pipe.HSet(ctx, redisKeyUTXOs, field, string(data))
	}
	pipe.Set(ctx, redisKeyLastPoll, time.Now().Format(time.RFC3339), 0)

	if _, err := pipe.Exec(ctx); err != nil {
		tw.logger.Error("persist confirmed UTXOs to Redis", "error", err)
	}
}

// persistMempool writes mempool UTXOs to Redis atomically.
func (tw *TreasuryWatcher) persistMempool(snapshot []FundingUTXO) {
	if tw.rdb == nil {
		return
	}

	ctx := context.Background()
	pipe := tw.rdb.Pipeline()
	pipe.Del(ctx, redisKeyMempool)
	for _, u := range snapshot {
		field := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		data, err := json.Marshal(u)
		if err != nil {
			tw.logger.Error("marshal mempool UTXO for Redis", "error", err)
			continue
		}
		pipe.HSet(ctx, redisKeyMempool, field, string(data))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		tw.logger.Error("persist mempool to Redis", "error", err)
	}
}

// persistSpent writes spent outpoints to Redis atomically.
func (tw *TreasuryWatcher) persistSpent(snapshot map[string]bool) {
	if tw.rdb == nil {
		return
	}

	ctx := context.Background()
	pipe := tw.rdb.Pipeline()
	pipe.Del(ctx, redisKeySpent)
	for op := range snapshot {
		pipe.SAdd(ctx, redisKeySpent, op)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		tw.logger.Error("persist spent to Redis", "error", err)
	}
}

// persistLeased writes active leases to Redis atomically.
func (tw *TreasuryWatcher) persistLeased(snapshot map[string]TreasuryLease) {
	if tw.rdb == nil {
		return
	}

	ctx := context.Background()
	pipe := tw.rdb.Pipeline()
	pipe.Del(ctx, redisKeyLeased)
	for key, lease := range snapshot {
		data, err := json.Marshal(lease)
		if err != nil {
			tw.logger.Error("marshal lease for Redis", "error", err)
			continue
		}
		pipe.HSet(ctx, redisKeyLeased, key, string(data))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		tw.logger.Error("persist leases to Redis", "error", err)
	}
}

// ── Hydration ─────────────────────────────────────────────────────────────────

// hydrateFromRedis loads confirmed UTXOs from Redis into the in-memory cache.
func (tw *TreasuryWatcher) hydrateFromRedis() error {
	if tw.rdb == nil {
		return nil
	}

	ctx := context.Background()

	result, err := tw.rdb.HGetAll(ctx, redisKeyUTXOs).Result()
	if err != nil {
		return fmt.Errorf("HGETALL %s: %w", redisKeyUTXOs, err)
	}

	utxos := make([]FundingUTXO, 0, len(result))
	for _, val := range result {
		var u FundingUTXO
		if err := json.Unmarshal([]byte(val), &u); err != nil {
			tw.logger.Warn("skip malformed Redis entry", "error", err)
			continue
		}
		utxos = append(utxos, u)
	}

	sort.Slice(utxos, func(i, j int) bool {
		return utxos[i].Satoshis > utxos[j].Satoshis
	})

	lastPollStr, err := tw.rdb.Get(ctx, redisKeyLastPoll).Result()
	if err == nil {
		if t, err := time.Parse(time.RFC3339, lastPollStr); err == nil {
			tw.mu.Lock()
			tw.lastPoll = t
			tw.mu.Unlock()
		}
	}

	tw.mu.Lock()
	tw.utxos = utxos
	tw.mu.Unlock()

	return nil
}

// hydrateMempool loads mempool, spent, and leased state from Redis on startup.
// Expired leases are discarded immediately.
func (tw *TreasuryWatcher) hydrateMempool() error {
	if tw.rdb == nil {
		return nil
	}

	ctx := context.Background()

	// Hydrate mempool HASH
	mResult, err := tw.rdb.HGetAll(ctx, redisKeyMempool).Result()
	if err != nil {
		return fmt.Errorf("HGETALL %s: %w", redisKeyMempool, err)
	}
	mempool := make([]FundingUTXO, 0, len(mResult))
	for _, val := range mResult {
		var u FundingUTXO
		if err := json.Unmarshal([]byte(val), &u); err != nil {
			tw.logger.Warn("skip malformed mempool entry", "error", err)
			continue
		}
		mempool = append(mempool, u)
	}

	// Hydrate spent SET
	spentMembers, err := tw.rdb.SMembers(ctx, redisKeySpent).Result()
	if err != nil {
		return fmt.Errorf("SMEMBERS %s: %w", redisKeySpent, err)
	}
	spent := make(map[string]bool, len(spentMembers))
	for _, op := range spentMembers {
		spent[op] = true
	}

	// Hydrate leased HASH + reclaim expired leases immediately
	lResult, err := tw.rdb.HGetAll(ctx, redisKeyLeased).Result()
	if err != nil {
		return fmt.Errorf("HGETALL %s: %w", redisKeyLeased, err)
	}
	leased := make(map[string]TreasuryLease, len(lResult))
	now := time.Now()
	for key, val := range lResult {
		var lease TreasuryLease
		if err := json.Unmarshal([]byte(val), &lease); err != nil {
			tw.logger.Warn("skip malformed lease entry", "key", key, "error", err)
			continue
		}
		if now.After(lease.ExpiresAt) {
			tw.logger.Warn("reclaiming expired lease on startup", "outpoint", key)
			continue // don't restore expired leases
		}
		leased[key] = lease
	}

	tw.mu.Lock()
	tw.mempool = mempool
	tw.spent = spent
	tw.leased = leased
	tw.mu.Unlock()

	if len(mempool) > 0 || len(spent) > 0 || len(leased) > 0 {
		tw.logger.Info("hydrated mempool state from Redis",
			"mempool", len(mempool), "spent", len(spent), "leased", len(leased))
	}

	return nil
}

// ── Lease Lifecycle ───────────────────────────────────────────────────────────

// LeaseFundingCandidate atomically selects and leases a funding UTXO with at
// least minSats value. Searches confirmed UTXOs first, then mempool.
// Returns nil, nil if no qualifying UTXO is available.
func (tw *TreasuryWatcher) LeaseFundingCandidate(minSats uint64, owner string) (*FundingUTXO, error) {
	tw.mu.Lock()

	now := time.Now()

	// Search confirmed UTXOs first (sorted by value desc)
	for i := range tw.utxos {
		key := fmt.Sprintf("%s:%d", tw.utxos[i].TxID, tw.utxos[i].Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		if tw.utxos[i].Satoshis >= minSats {
			u := tw.utxos[i]
			tw.leased[key] = TreasuryLease{
				TxID: u.TxID, Vout: u.Vout, Satoshis: u.Satoshis,
				LeasedAt: now, ExpiresAt: now.Add(tw.leaseTTL), LeaseOwner: owner,
			}
			snap := snapshotLeases(tw.leased)
			tw.mu.Unlock()
			tw.persistLeased(snap)
			return &u, nil
		}
	}

	// Then check mempool UTXOs
	for i := range tw.mempool {
		key := fmt.Sprintf("%s:%d", tw.mempool[i].TxID, tw.mempool[i].Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		if tw.mempool[i].Satoshis >= minSats {
			u := tw.mempool[i]
			tw.leased[key] = TreasuryLease{
				TxID: u.TxID, Vout: u.Vout, Satoshis: u.Satoshis,
				LeasedAt: now, ExpiresAt: now.Add(tw.leaseTTL), LeaseOwner: owner,
			}
			snap := snapshotLeases(tw.leased)
			tw.mu.Unlock()
			tw.persistLeased(snap)
			return &u, nil
		}
	}

	tw.mu.Unlock()
	return nil, nil
}

// LeaseFundingExplicit leases a known outpoint for exclusive use.
// It derives the satoshi value from watcher state (never trusts the request payload).
// Returns an error if the outpoint is already spent, already actively leased,
// or not found in watcher state.
func (tw *TreasuryWatcher) LeaseFundingExplicit(txid string, vout uint32, owner string) error {
	tw.mu.Lock()

	key := fmt.Sprintf("%s:%d", txid, vout)

	// Reject if already spent
	if tw.spent[key] {
		tw.mu.Unlock()
		return fmt.Errorf("outpoint %s is already spent", key)
	}

	// Reject if already actively leased
	if lease, ok := tw.leased[key]; ok && time.Now().Before(lease.ExpiresAt) {
		tw.mu.Unlock()
		return fmt.Errorf("outpoint %s is already leased by %q (expires %s)",
			key, lease.LeaseOwner, lease.ExpiresAt.Format(time.RFC3339))
	}

	// Look up canonical satoshi value from watcher state
	var satoshis uint64
	found := false
	for _, u := range tw.utxos {
		if u.TxID == txid && u.Vout == vout {
			satoshis = u.Satoshis
			found = true
			break
		}
	}
	if !found {
		for _, u := range tw.mempool {
			if u.TxID == txid && u.Vout == vout {
				satoshis = u.Satoshis
				found = true
				break
			}
		}
	}
	if !found {
		tw.mu.Unlock()
		return fmt.Errorf("outpoint %s not found in watcher state", key)
	}

	now := time.Now()
	tw.leased[key] = TreasuryLease{
		TxID: txid, Vout: vout, Satoshis: satoshis,
		LeasedAt: now, ExpiresAt: now.Add(tw.leaseTTL), LeaseOwner: owner,
	}
	snap := snapshotLeases(tw.leased)
	tw.mu.Unlock()

	tw.persistLeased(snap)
	return nil
}

// ConsumeLease is called after a successful broadcast.
// It removes the lease, marks the outpoint as spent, and removes it from mempool.
func (tw *TreasuryWatcher) ConsumeLease(txid string, vout uint32) {
	tw.mu.Lock()

	key := fmt.Sprintf("%s:%d", txid, vout)
	delete(tw.leased, key)
	tw.spent[key] = true
	tw.removeMempoolLocked(key)

	leasedSnap := snapshotLeases(tw.leased)
	spentSnap := snapshotSpent(tw.spent)
	mempoolSnap := snapshotMempool(tw.mempool)
	tw.mu.Unlock()

	tw.persistLeased(leasedSnap)
	tw.persistSpent(spentSnap)
	tw.persistMempool(mempoolSnap)
}

// ReleaseLease is called after a failed broadcast.
// It removes the lease so the outpoint becomes available again.
func (tw *TreasuryWatcher) ReleaseLease(txid string, vout uint32) {
	tw.mu.Lock()

	key := fmt.Sprintf("%s:%d", txid, vout)
	delete(tw.leased, key)

	snap := snapshotLeases(tw.leased)
	tw.mu.Unlock()

	tw.persistLeased(snap)
}

// RegisterMempool adds a change output to the mempool tracking list.
// Called after a successful broadcast to make the change output immediately
// available for subsequent operations.
func (tw *TreasuryWatcher) RegisterMempool(utxo *FundingUTXO) {
	if utxo == nil {
		return
	}

	tw.mu.Lock()

	key := fmt.Sprintf("%s:%d", utxo.TxID, utxo.Vout)
	// Deduplicate
	for _, m := range tw.mempool {
		if fmt.Sprintf("%s:%d", m.TxID, m.Vout) == key {
			tw.mu.Unlock()
			return
		}
	}
	tw.mempool = append(tw.mempool, *utxo)

	snap := snapshotMempool(tw.mempool)
	tw.mu.Unlock()

	tw.persistMempool(snap)

	tw.logger.Info("registered mempool UTXO",
		"txid", utxo.TxID, "vout", utxo.Vout, "satoshis", utxo.Satoshis)
}

// removeMempoolLocked removes an outpoint from the mempool slice.
// Must be called with tw.mu held.
func (tw *TreasuryWatcher) removeMempoolLocked(key string) {
	for i, m := range tw.mempool {
		if fmt.Sprintf("%s:%d", m.TxID, m.Vout) == key {
			tw.mempool = append(tw.mempool[:i], tw.mempool[i+1:]...)
			return
		}
	}
}

// ── Query Methods ─────────────────────────────────────────────────────────────

// GetUTXOs returns available treasury UTXOs (confirmed + mempool, minus spent/leased).
func (tw *TreasuryWatcher) GetUTXOs() []FundingUTXO {
	tw.mu.RLock()
	defer tw.mu.RUnlock()

	now := time.Now()
	out := make([]FundingUTXO, 0, len(tw.utxos)+len(tw.mempool))

	for _, u := range tw.utxos {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		out = append(out, u)
	}
	for _, u := range tw.mempool {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		out = append(out, u)
	}

	return out
}

// GetUTXOsWithStatus returns available treasury UTXOs with their confirmation status.
// Used by the dashboard API to show confirmed vs mempool badges.
func (tw *TreasuryWatcher) GetUTXOsWithStatus() []FundingUTXOWithStatus {
	tw.mu.RLock()
	defer tw.mu.RUnlock()

	now := time.Now()
	out := make([]FundingUTXOWithStatus, 0, len(tw.utxos)+len(tw.mempool))

	for _, u := range tw.utxos {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		out = append(out, FundingUTXOWithStatus{FundingUTXO: u, Status: UTXOStatusConfirmed})
	}
	for _, u := range tw.mempool {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		out = append(out, FundingUTXOWithStatus{FundingUTXO: u, Status: UTXOStatusMempool})
	}

	return out
}

// GetFunding returns the first available UTXO with at least minSats value.
// This is a read-only operation — it does NOT lease the UTXO.
// Implements the FundingSource interface (defined in refill.go).
// Returns nil, nil if no qualifying UTXO is available.
func (tw *TreasuryWatcher) GetFunding(minSats uint64) (*FundingUTXO, error) {
	tw.mu.RLock()
	defer tw.mu.RUnlock()

	now := time.Now()

	// Search confirmed UTXOs first
	for i := range tw.utxos {
		key := fmt.Sprintf("%s:%d", tw.utxos[i].TxID, tw.utxos[i].Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		if tw.utxos[i].Satoshis >= minSats {
			u := tw.utxos[i]
			return &u, nil
		}
	}

	// Then mempool UTXOs
	for i := range tw.mempool {
		key := fmt.Sprintf("%s:%d", tw.mempool[i].TxID, tw.mempool[i].Vout)
		if tw.spent[key] {
			continue
		}
		if lease, ok := tw.leased[key]; ok && now.Before(lease.ExpiresAt) {
			continue
		}
		if tw.mempool[i].Satoshis >= minSats {
			u := tw.mempool[i]
			return &u, nil
		}
	}

	return nil, nil
}

// LastPoll returns the time of the last successful poll and any error from the last attempt.
func (tw *TreasuryWatcher) LastPoll() (time.Time, error) {
	tw.mu.RLock()
	defer tw.mu.RUnlock()
	return tw.lastPoll, tw.lastErr
}
