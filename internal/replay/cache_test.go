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
	"strings"
	"testing"
	"time"
)

func TestRecordAndCheck(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonceTxid := strings.Repeat("a", 64)
	var nonceVout uint32 = 0
	spendTxid := strings.Repeat("b", 64)
	challengeHash := strings.Repeat("c", 64)

	// Initially not found
	_, _, found := cache.Check(nonceTxid, nonceVout)
	if found {
		t.Error("expected not found")
	}

	// Record
	cache.Record(nonceTxid, nonceVout, spendTxid, challengeHash)

	// Now found
	resultTxid, resultHash, found := cache.Check(nonceTxid, nonceVout)
	if !found {
		t.Error("expected found after record")
	}
	if resultTxid != spendTxid {
		t.Errorf("spendTxID: got %s, want %s", resultTxid, spendTxid)
	}
	if resultHash != challengeHash {
		t.Errorf("challengeHash: got %s, want %s", resultHash, challengeHash)
	}
}

func TestDifferentVout(t *testing.T) {
	cache := New(5*time.Minute, 100)

	txid := strings.Repeat("a", 64)
	cache.Record(txid, 0, "spend0", "hash0")
	cache.Record(txid, 1, "spend1", "hash1")

	// Different vout = different entry
	result0, _, found0 := cache.Check(txid, 0)
	result1, _, found1 := cache.Check(txid, 1)
	_, _, found2 := cache.Check(txid, 2)

	if !found0 || result0 != "spend0" {
		t.Errorf("vout 0: got %s/%v, want spend0/true", result0, found0)
	}
	if !found1 || result1 != "spend1" {
		t.Errorf("vout 1: got %s/%v, want spend1/true", result1, found1)
	}
	if found2 {
		t.Error("vout 2 should not be found")
	}
}

func TestTTLExpiry(t *testing.T) {
	cache := New(1*time.Millisecond, 100)

	nonceTxid := strings.Repeat("c", 64)
	cache.Record(nonceTxid, 0, "spend1", "hash1")

	time.Sleep(5 * time.Millisecond)

	_, _, found := cache.Check(nonceTxid, 0)
	if found {
		t.Error("expected entry to expire after TTL")
	}
}

func TestLRUEviction(t *testing.T) {
	cache := New(5*time.Minute, 2) // max 2 entries

	txid1 := "tx1" + strings.Repeat("0", 61)
	txid2 := "tx2" + strings.Repeat("0", 61)
	txid3 := "tx3" + strings.Repeat("0", 61)

	cache.Record(txid1, 0, "spend1", "hash1")
	cache.Record(txid2, 0, "spend2", "hash2")

	// Both should exist
	_, _, found1 := cache.Check(txid1, 0)
	_, _, found2 := cache.Check(txid2, 0)
	if !found1 || !found2 {
		t.Error("expected both entries to exist")
	}

	// Adding a third should evict the first (LRU)
	cache.Record(txid3, 0, "spend3", "hash3")

	_, _, found1 = cache.Check(txid1, 0)
	if found1 {
		t.Error("expected first entry to be evicted")
	}

	_, _, found3 := cache.Check(txid3, 0)
	if !found3 {
		t.Error("expected third entry to exist")
	}
}

func TestCleanup(t *testing.T) {
	cache := New(1*time.Millisecond, 100)

	cache.Record(strings.Repeat("d", 64), 0, "spend1", "hash1")
	cache.Record(strings.Repeat("e", 64), 0, "spend2", "hash2")

	time.Sleep(5 * time.Millisecond)

	removed := cache.Cleanup()
	if removed != 2 {
		t.Errorf("expected 2 removed, got %d", removed)
	}

	if cache.Size() != 0 {
		t.Errorf("expected size 0 after cleanup, got %d", cache.Size())
	}
}

func TestSize(t *testing.T) {
	cache := New(5*time.Minute, 100)

	if cache.Size() != 0 {
		t.Errorf("expected size 0, got %d", cache.Size())
	}

	cache.Record(strings.Repeat("f", 64), 0, "spend1", "hash1")
	if cache.Size() != 1 {
		t.Errorf("expected size 1, got %d", cache.Size())
	}
}

// ── TryReserve / Commit / Release tests ──

func TestTryReserveBasic(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	// First reservation succeeds
	reserved, existingTxID, pending := cache.TryReserve(nonce, 0, ch)
	if !reserved || existingTxID != "" || pending {
		t.Fatalf("TryReserve: got (%v, %q, %v), want (true, \"\", false)", reserved, existingTxID, pending)
	}

	// Pending entry is invisible to Check
	_, _, found := cache.Check(nonce, 0)
	if found {
		t.Error("Check should not see pending entries")
	}

	// Second reservation for same outpoint returns pending
	reserved2, _, pending2 := cache.TryReserve(nonce, 0, ch)
	if reserved2 || !pending2 {
		t.Fatalf("second TryReserve: got (%v, _, %v), want (false, _, true)", reserved2, pending2)
	}

	// Different vout still works
	reserved3, _, _ := cache.TryReserve(nonce, 1, ch)
	if !reserved3 {
		t.Error("TryReserve on different vout should succeed")
	}
}

func TestTryReserveAfterCommitted(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)
	spend := strings.Repeat("b", 64)

	// Record a committed entry directly
	cache.Record(nonce, 0, spend, ch)

	// TryReserve should fail with existing txid
	reserved, existingTxID, pending := cache.TryReserve(nonce, 0, ch)
	if reserved || pending {
		t.Fatalf("TryReserve on committed: got (%v, _, %v), want (false, _, false)", reserved, pending)
	}
	if existingTxID != spend {
		t.Errorf("existingTxID: got %q, want %q", existingTxID, spend)
	}
}

func TestTryReserveExpiredEntry(t *testing.T) {
	cache := New(1*time.Millisecond, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	// Record and let it expire
	cache.Record(nonce, 0, "spend1", ch)
	time.Sleep(5 * time.Millisecond)

	// TryReserve should succeed because entry is expired
	reserved, _, _ := cache.TryReserve(nonce, 0, ch)
	if !reserved {
		t.Error("TryReserve should succeed on expired entry")
	}
}

func TestCommitSuccess(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)
	spend := strings.Repeat("b", 64)

	// Reserve
	reserved, _, _ := cache.TryReserve(nonce, 0, ch)
	if !reserved {
		t.Fatal("TryReserve failed")
	}

	// Commit
	if err := cache.Commit(nonce, 0, ch, spend); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Now Check should see it
	resultTxID, resultHash, found := cache.Check(nonce, 0)
	if !found {
		t.Fatal("Check should find committed entry")
	}
	if resultTxID != spend {
		t.Errorf("spendTxID: got %q, want %q", resultTxID, spend)
	}
	if resultHash != ch {
		t.Errorf("challengeHash: got %q, want %q", resultHash, ch)
	}
}

func TestCommitFailures(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)
	spend := strings.Repeat("b", 64)

	// Commit on absent entry
	if err := cache.Commit(nonce, 0, ch, spend); err == nil {
		t.Error("Commit on absent entry should fail")
	}

	// Reserve then commit
	cache.TryReserve(nonce, 0, ch)
	if err := cache.Commit(nonce, 0, ch, spend); err != nil {
		t.Fatalf("first Commit failed: %v", err)
	}

	// Double commit
	if err := cache.Commit(nonce, 0, ch, spend); err == nil {
		t.Error("double Commit should fail")
	}

	// Commit with wrong challengeHash
	nonce2 := strings.Repeat("d", 64)
	cache.TryReserve(nonce2, 0, ch)
	if err := cache.Commit(nonce2, 0, "wrong_hash", spend); err == nil {
		t.Error("Commit with mismatched challengeHash should fail")
	}
}

func TestReleaseSuccess(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	// Reserve
	cache.TryReserve(nonce, 0, ch)
	if cache.Size() != 1 {
		t.Fatalf("expected size 1 after reserve, got %d", cache.Size())
	}

	// Release
	cache.Release(nonce, 0, ch)
	if cache.Size() != 0 {
		t.Fatalf("expected size 0 after release, got %d", cache.Size())
	}

	// Can reserve again after release
	reserved, _, _ := cache.TryReserve(nonce, 0, ch)
	if !reserved {
		t.Error("TryReserve should succeed after Release")
	}
}

func TestReleaseDoesNotDeleteCommitted(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)
	spend := strings.Repeat("b", 64)

	// Reserve + Commit
	cache.TryReserve(nonce, 0, ch)
	cache.Commit(nonce, 0, ch, spend)

	// Release should be a no-op on committed entry
	cache.Release(nonce, 0, ch)

	// Entry should still be visible
	_, _, found := cache.Check(nonce, 0)
	if !found {
		t.Error("Release should not delete committed entries")
	}
}

func TestReleaseWrongChallengeHash(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	// Reserve
	cache.TryReserve(nonce, 0, ch)

	// Release with wrong challengeHash — should be a no-op
	cache.Release(nonce, 0, "wrong_hash")
	if cache.Size() != 1 {
		t.Error("Release with wrong challengeHash should not delete entry")
	}

	// Release with correct challengeHash — should work
	cache.Release(nonce, 0, ch)
	if cache.Size() != 0 {
		t.Error("Release with correct challengeHash should delete entry")
	}
}

func TestCheckInvisibleForPending(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	// Reserve creates a pending entry
	cache.TryReserve(nonce, 0, ch)

	// Check must NOT see it (gatekeeper compatibility)
	_, _, found := cache.Check(nonce, 0)
	if found {
		t.Error("Check must not return pending entries")
	}

	// Commit makes it visible
	cache.Commit(nonce, 0, ch, "txid123")
	_, _, found = cache.Check(nonce, 0)
	if !found {
		t.Error("Check should see committed entries")
	}
}

func TestConcurrentTryReserve(t *testing.T) {
	cache := New(5*time.Minute, 100)

	nonce := strings.Repeat("a", 64)
	ch := strings.Repeat("c", 64)

	const goroutines = 50
	results := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			reserved, _, _ := cache.TryReserve(nonce, 0, ch)
			results <- reserved
		}()
	}

	reservedCount := 0
	for i := 0; i < goroutines; i++ {
		if <-results {
			reservedCount++
		}
	}

	if reservedCount != 1 {
		t.Errorf("exactly 1 goroutine should succeed, got %d", reservedCount)
	}
}
