// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package replay

import (
	"fmt"
	"sync"
	"time"
)

// Cache provides LRU-evicting, TTL-based replay detection.
// Key: nonce outpoint (txid:vout) → Value: the txid that settled it + challenge hash.
type Cache struct {
	mu      sync.RWMutex
	items   map[string]entry
	order   []string // for LRU eviction
	maxSize int
	ttl     time.Duration
}

type entry struct {
	spendTxID     string
	challengeHash string
	createdAt     time.Time
}

// New creates a replay cache with the given TTL and max size.
func New(ttl time.Duration, maxSize int) *Cache {
	return &Cache{
		items:   make(map[string]entry, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

// outpointKey builds the cache key from a nonce outpoint.
func outpointKey(txid string, vout uint32) string {
	return fmt.Sprintf("%s:%d", txid, vout)
}

// Check returns the spending txid and challenge hash if the nonce outpoint
// has already been settled (committed). Pending reservations are invisible
// to Check — this ensures the gatekeeper never sees an empty spendTxID.
// Returns ("", "", false) if not found, expired, or pending.
func (c *Cache) Check(txid string, vout uint32) (spendTxID, challengeHash string, found bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := outpointKey(txid, vout)
	e, ok := c.items[key]
	if !ok {
		return "", "", false
	}

	// Check TTL
	if time.Since(e.createdAt) > c.ttl {
		return "", "", false
	}

	// Pending entries (spendTxID == "") are invisible to Check.
	// Only committed entries (spendTxID != "") are returned.
	if e.spendTxID == "" {
		return "", "", false
	}

	return e.spendTxID, e.challengeHash, true
}

// TryReserve atomically checks for an existing entry and, if absent,
// inserts a pending reservation (spendTxID = ""). Uses a single exclusive
// Lock for the entire check+insert — eliminates the TOCTOU window.
//
// Returns:
//   - (true, "", false)            — reserved successfully
//   - (false, "", true)            — another goroutine has a pending reservation
//   - (false, existingTxID, false) — already committed with existingTxID
func (c *Cache) TryReserve(txid string, vout uint32, challengeHash string) (reserved bool, existingTxID string, pending bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := outpointKey(txid, vout)

	if e, ok := c.items[key]; ok {
		if time.Since(e.createdAt) <= c.ttl {
			if e.spendTxID == "" {
				return false, "", true // pending reservation by another goroutine
			}
			return false, e.spendTxID, false // already committed
		}
		// Expired — treat as absent, clean up stale entry
		delete(c.items, key)
	}

	// Evict if at capacity
	if len(c.items) >= c.maxSize {
		c.evictOldest()
	}

	c.items[key] = entry{
		spendTxID:     "", // pending — no txid yet
		challengeHash: challengeHash,
		createdAt:     time.Now(),
	}
	c.order = append(c.order, key)
	return true, "", false
}

// Commit transitions a pending reservation to committed state by setting the
// final spendTxID. Returns an error if the entry is missing, already committed,
// or the challengeHash does not match (prevents cross-challenge corruption).
func (c *Cache) Commit(txid string, vout uint32, challengeHash, spendTxID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := outpointKey(txid, vout)
	e, ok := c.items[key]
	if !ok {
		return fmt.Errorf("replay cache commit: no entry for %s", key)
	}
	if e.spendTxID != "" {
		return fmt.Errorf("replay cache commit: %s already committed with txid %s", key, e.spendTxID)
	}
	if e.challengeHash != challengeHash {
		return fmt.Errorf("replay cache commit: challengeHash mismatch for %s", key)
	}
	e.spendTxID = spendTxID
	c.items[key] = e // re-assign to update map value (entry is a value type)
	return nil
}

// Release removes a pending reservation, allowing retries.
// Only deletes if the entry exists, is still pending (spendTxID == ""),
// and the challengeHash matches. No-op otherwise.
func (c *Cache) Release(txid string, vout uint32, challengeHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := outpointKey(txid, vout)
	e, ok := c.items[key]
	if !ok {
		return
	}
	if e.spendTxID != "" {
		return // already committed — do not delete
	}
	if e.challengeHash != challengeHash {
		return // belongs to a different challenge — do not delete
	}
	delete(c.items, key)
	// Note: key remains in c.order as a stale ref — evictOldest() handles
	// missing keys gracefully (delete on absent key is a no-op).
}

// Record stores a nonce outpoint → spending txid + challenge hash mapping.
func (c *Cache) Record(txid string, vout uint32, spendTxID, challengeHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity
	if len(c.items) >= c.maxSize {
		c.evictOldest()
	}

	key := outpointKey(txid, vout)
	c.items[key] = entry{
		spendTxID:     spendTxID,
		challengeHash: challengeHash,
		createdAt:     time.Now(),
	}
	c.order = append(c.order, key)
}

// Cleanup removes expired entries. Call periodically.
func (c *Cache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	now := time.Now()
	newOrder := make([]string, 0, len(c.order))
	for _, key := range c.order {
		e, ok := c.items[key]
		if !ok {
			continue
		}
		if now.Sub(e.createdAt) > c.ttl {
			delete(c.items, key)
			removed++
		} else {
			newOrder = append(newOrder, key)
		}
	}
	c.order = newOrder
	return removed
}

// Size returns the number of entries in the cache.
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

func (c *Cache) evictOldest() {
	if len(c.order) == 0 {
		return
	}
	oldest := c.order[0]
	c.order = c.order[1:]
	delete(c.items, oldest)
}
