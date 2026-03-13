// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package gatekeeper

import (
	"sync"
	"time"

	"github.com/merkleworks/x402-bsv/internal/challenge"
)

// ChallengeCache stores issued challenges keyed by their SHA-256 hash.
// Used to look up the original challenge when a proof is submitted,
// enabling request binding verification and scheme/version validation.
//
// Features (adopted from OpenClaw):
//   - Bounded: max size with FIFO eviction when at capacity
//   - Auto-cleanup: background goroutine removes expired entries every minute
//   - TTL-based: entries expire after configurable TTL
type ChallengeCache struct {
	mu      sync.RWMutex
	items   map[string]cachedChallenge
	ttl     time.Duration
	maxSize int
}

type cachedChallenge struct {
	challenge *challenge.Challenge
	createdAt time.Time
}

// NewChallengeCache creates a bounded challenge cache with TTL and max size.
// Starts a background cleanup goroutine.
func NewChallengeCache(ttl time.Duration, maxSize int) *ChallengeCache {
	if maxSize <= 0 {
		maxSize = 10000
	}

	c := &ChallengeCache{
		items:   make(map[string]cachedChallenge),
		ttl:     ttl,
		maxSize: maxSize,
	}

	// Start background cleanup
	go c.cleanupLoop()

	return c
}

// Store saves a challenge keyed by its SHA-256 hash.
// If at capacity, evicts the oldest entry first.
func (c *ChallengeCache) Store(hash string, ch *challenge.Challenge) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict oldest if at capacity
	if len(c.items) >= c.maxSize {
		c.evictOldest()
	}

	c.items[hash] = cachedChallenge{
		challenge: ch,
		createdAt: time.Now(),
	}
}

// Lookup retrieves a challenge by its SHA-256 hash.
// Returns nil if not found or expired.
func (c *ChallengeCache) Lookup(challengeHash string) *challenge.Challenge {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.items[challengeHash]
	if !ok {
		return nil
	}

	// Check TTL
	if time.Since(entry.createdAt) > c.ttl {
		return nil
	}

	return entry.challenge
}

// Delete removes a challenge by its hash (used after successful verification).
func (c *ChallengeCache) Delete(challengeHash string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.items, challengeHash)
}

// Cleanup removes expired entries. Can be called manually.
func (c *ChallengeCache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	now := time.Now()
	for key, entry := range c.items {
		if now.Sub(entry.createdAt) > c.ttl {
			delete(c.items, key)
			removed++
		}
	}
	return removed
}

// Size returns the number of entries in the cache.
func (c *ChallengeCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.items)
}

// evictOldest removes the oldest challenge (must be called with lock held).
func (c *ChallengeCache) evictOldest() {
	var oldestHash string
	var oldestTime time.Time

	for hash, entry := range c.items {
		if oldestHash == "" || entry.createdAt.Before(oldestTime) {
			oldestHash = hash
			oldestTime = entry.createdAt
		}
	}

	if oldestHash != "" {
		delete(c.items, oldestHash)
	}
}

// cleanupLoop periodically removes expired challenges.
func (c *ChallengeCache) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		c.Cleanup()
	}
}
