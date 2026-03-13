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
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestRedisPool(t *testing.T) (*RedisPool, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	pool, err := NewRedisPool(rdb, "test:", key, false, 5*time.Minute)
	if err != nil {
		t.Fatalf("create redis pool: %v", err)
	}

	t.Cleanup(func() {
		rdb.Close()
		mr.Close()
	})

	return pool, mr
}

func TestRedisPool_NewAndAddress(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	if pool.Address() == "" {
		t.Error("expected non-empty address")
	}
	if pool.Available() != 0 {
		t.Errorf("expected 0 available, got %d", pool.Available())
	}
}

func TestRedisPool_AddExistingAndLease(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	scriptHex, err := pool.LockingScriptHex()
	if err != nil {
		t.Fatalf("locking script: %v", err)
	}

	utxos := []UTXO{
		{TxID: repeatHex("a", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("a", 64), Vout: 1, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("b", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
	}
	pool.AddExisting(utxos)

	if pool.Available() != 3 {
		t.Errorf("expected 3 available, got %d", pool.Available())
	}

	// Lease one
	leased, err := pool.Lease()
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if leased == nil {
		t.Fatal("expected non-nil UTXO")
	}
	if leased.Status != StatusLeased {
		t.Errorf("expected status leased, got %s", leased.Status)
	}
	if pool.Available() != 2 {
		t.Errorf("expected 2 available after lease, got %d", pool.Available())
	}
}

func TestRedisPool_LeaseExhaustion(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	scriptHex, _ := pool.LockingScriptHex()
	pool.AddExisting([]UTXO{
		{TxID: repeatHex("c", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
	})

	_, err := pool.Lease()
	if err != nil {
		t.Fatalf("first lease failed: %v", err)
	}

	_, err = pool.Lease()
	if err == nil {
		t.Error("expected error on exhausted pool, got nil")
	}
}

func TestRedisPool_MarkSpentAndLookup(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	scriptHex, _ := pool.LockingScriptHex()
	txid := repeatHex("d", 64)

	pool.AddExisting([]UTXO{
		{TxID: txid, Vout: 0, Script: scriptHex, Satoshis: 1},
	})

	// Lookup
	u := pool.Lookup(txid, 0)
	if u == nil {
		t.Fatal("expected to find UTXO")
	}
	if u.Status != StatusAvailable {
		t.Errorf("expected available, got %s", u.Status)
	}

	// Mark spent
	pool.MarkSpent(txid, 0)
	u = pool.Lookup(txid, 0)
	if u == nil {
		t.Fatal("expected to find UTXO after spend")
	}
	if u.Status != StatusSpent {
		t.Errorf("expected spent, got %s", u.Status)
	}
}

func TestRedisPool_Stats(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	scriptHex, _ := pool.LockingScriptHex()
	pool.AddExisting([]UTXO{
		{TxID: repeatHex("f", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("f", 64), Vout: 1, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("f", 64), Vout: 2, Script: scriptHex, Satoshis: 1},
	})

	// Lease one
	leased, err := pool.Lease()
	if err != nil {
		t.Fatalf("lease: %v", err)
	}

	// Spend a different one
	spentVout := uint32(0)
	for _, v := range []uint32{0, 1, 2} {
		if v != leased.Vout {
			spentVout = v
			break
		}
	}
	pool.MarkSpent(repeatHex("f", 64), spentVout)

	stats := pool.Stats()
	if stats.Total != 3 {
		t.Errorf("expected total 3, got %d", stats.Total)
	}
	// Available should be 2 (3 added - 1 leased = 2 in ZSET, then 1 spent removes from ZSET = 1)
	// Actually: AddExisting adds 3 to ZSET. Lease removes 1 from ZSET (2 left). MarkSpent tries to
	// ZREM from available (1 left if it was still in ZSET, or 2 if it was the leased one).
	// Since we picked a non-leased one, it's still in ZSET, so after spend: 1 available.
	if stats.Available != 1 {
		t.Errorf("expected 1 available, got %d", stats.Available)
	}
	if stats.Spent != 1 {
		t.Errorf("expected 1 spent, got %d", stats.Spent)
	}
	// Leased = total - available - spent = 3 - 1 - 1 = 1
	if stats.Leased != 1 {
		t.Errorf("expected 1 leased, got %d", stats.Leased)
	}
}

func TestRedisPool_LeaseN(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	scriptHex, _ := pool.LockingScriptHex()
	pool.AddExisting([]UTXO{
		{TxID: repeatHex("a", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("a", 64), Vout: 1, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("a", 64), Vout: 2, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("a", 64), Vout: 3, Script: scriptHex, Satoshis: 1},
		{TxID: repeatHex("a", 64), Vout: 4, Script: scriptHex, Satoshis: 1},
	})

	// Lease 3
	leased, err := pool.LeaseN(3)
	if err != nil {
		t.Fatalf("LeaseN: %v", err)
	}
	if len(leased) != 3 {
		t.Errorf("expected 3 leased, got %d", len(leased))
	}
	for _, u := range leased {
		if u.Status != StatusLeased {
			t.Errorf("expected leased, got %s", u.Status)
		}
	}
	if pool.Available() != 2 {
		t.Errorf("expected 2 available after LeaseN(3), got %d", pool.Available())
	}

	// Try to lease 3 more — should fail
	_, err = pool.LeaseN(3)
	if err == nil {
		t.Error("expected error when requesting more UTXOs than available")
	}

	// Lease remaining 2
	leased2, err := pool.LeaseN(2)
	if err != nil {
		t.Fatalf("LeaseN(2): %v", err)
	}
	if len(leased2) != 2 {
		t.Errorf("expected 2 leased, got %d", len(leased2))
	}
	if pool.Available() != 0 {
		t.Errorf("expected 0 available, got %d", pool.Available())
	}
}

func TestRedisPool_LeaseNZero(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	_, err := pool.LeaseN(0)
	if err == nil {
		t.Error("expected error for LeaseN(0)")
	}

	_, err = pool.LeaseN(-1)
	if err == nil {
		t.Error("expected error for LeaseN(-1)")
	}
}

func TestRedisPool_ReclaimExpired(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("start miniredis: %v", err)
	}
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	key, _ := ec.NewPrivateKey()
	// Use 1-second TTL — the reclaim logic uses Unix seconds for comparison,
	// so sub-second TTLs round to 0 and may not expire correctly.
	pool, _ := NewRedisPool(rdb, "test:", key, false, 1*time.Second)

	scriptHex, _ := pool.LockingScriptHex()
	pool.AddExisting([]UTXO{
		{TxID: repeatHex("e", 64), Vout: 0, Script: scriptHex, Satoshis: 1},
	})

	// Lease it
	_, err = pool.Lease()
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if pool.Available() != 0 {
		t.Errorf("expected 0 available after lease, got %d", pool.Available())
	}

	// Wait for the lease to expire (1 second + generous margin for CI/slow systems)
	time.Sleep(2 * time.Second)

	// Reclaim
	pool.reclaimExpired()

	if pool.Available() != 1 {
		t.Errorf("expected 1 available after reclaim, got %d", pool.Available())
	}
}

func TestRedisPool_Interface(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	// Verify RedisPool satisfies Pool interface
	var p Pool = pool
	_ = p.Address()
	_ = p.Available()
	_ = p.Stats()
}

func TestRedisPool_LockingScriptHex(t *testing.T) {
	pool, _ := newTestRedisPool(t)

	hex, err := pool.LockingScriptHex()
	if err != nil {
		t.Fatalf("LockingScriptHex: %v", err)
	}
	if hex == "" {
		t.Error("expected non-empty locking script hex")
	}
	if len(hex) < 40 {
		t.Errorf("locking script hex too short: %d chars", len(hex))
	}
}
