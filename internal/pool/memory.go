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
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	p2pkh "github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
)

// Compile-time check that MemoryPool implements Pool.
var _ Pool = (*MemoryPool)(nil)

// MemoryPool is an in-memory UTXO pool implementation.
// Used for demo mode and testing. Not persistent across restarts.
type MemoryPool struct {
	mu          sync.Mutex
	utxos       map[string]*UTXO // key: "txid:vout"
	key         *ec.PrivateKey
	address     *script.Address
	mainnet     bool
	leaseTTL    time.Duration
	broadcaster transaction.Broadcaster
	logger      *slog.Logger
}

// NewMemoryPool creates a new in-memory UTXO pool.
func NewMemoryPool(key *ec.PrivateKey, mainnet bool, leaseTTL time.Duration, broadcaster transaction.Broadcaster) (*MemoryPool, error) {
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}

	return &MemoryPool{
		utxos:       make(map[string]*UTXO),
		key:         key,
		address:     addr,
		mainnet:     mainnet,
		leaseTTL:    leaseTTL,
		broadcaster: broadcaster,
		logger:      slog.Default().With("component", "memory-pool"),
	}, nil
}

// Address returns the BSV address that owns all UTXOs in this pool.
func (p *MemoryPool) Address() string {
	return p.address.AddressString
}

// LockingScriptHex returns the P2PKH locking script hex for the pool address.
func (p *MemoryPool) LockingScriptHex() (string, error) {
	s, err := p.lockingScript()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(*s), nil
}

func (p *MemoryPool) lockingScript() (*script.Script, error) {
	return p2pkh.Lock(p.address)
}

// Mint creates a fan-out transaction that produces `count` new 1-sat UTXOs.
// It requires a funding UTXO to pay for the outputs + miner fee.
// The funding UTXO must belong to the same key that owns the pool.
// feeRate is in sat/byte (e.g. 0.001 for BSV standard 1 sat/KB).
func (p *MemoryPool) Mint(fundingTxID string, fundingVout uint32, fundingScript string, fundingSatoshis uint64, count int, feeRate float64) ([]UTXO, error) {
	if count <= 0 || count > 10000 {
		return nil, fmt.Errorf("count must be between 1 and 10000, got %d", count)
	}

	tx := transaction.NewTransaction()

	allForkID := sighash.AllForkID
	unlocker, err := p2pkh.Unlock(p.key, &allForkID)
	if err != nil {
		return nil, fmt.Errorf("create unlocker: %w", err)
	}

	err = tx.AddInputFrom(fundingTxID, fundingVout, fundingScript, fundingSatoshis, unlocker)
	if err != nil {
		return nil, fmt.Errorf("add funding input: %w", err)
	}

	for i := 0; i < count; i++ {
		if err := tx.PayToAddress(p.Address(), 1); err != nil {
			return nil, fmt.Errorf("add output %d: %w", i, err)
		}
	}

	estimatedSize := 10 + 148 + (count * 34)
	fee := uint64(float64(estimatedSize) * feeRate)
	if fee < 1 {
		fee = 1
	}
	requiredSats := uint64(count) + fee
	if fundingSatoshis < requiredSats {
		return nil, fmt.Errorf("insufficient funding: need %d sats, have %d", requiredSats, fundingSatoshis)
	}

	change := fundingSatoshis - uint64(count) - fee
	if change > 546 {
		if err := tx.PayToAddress(p.Address(), change); err != nil {
			return nil, fmt.Errorf("add change output: %w", err)
		}
	}

	if err := tx.Sign(); err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}

	success, failure := p.broadcaster.Broadcast(tx)
	if failure != nil {
		return nil, fmt.Errorf("broadcast failed: %s - %s", failure.Code, failure.Description)
	}

	txid := success.Txid

	scriptHex, err := p.LockingScriptHex()
	if err != nil {
		return nil, fmt.Errorf("get locking script: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	utxos := make([]UTXO, count)
	for i := 0; i < count; i++ {
		u := UTXO{
			TxID:     txid,
			Vout:     uint32(i),
			Script:   scriptHex,
			Satoshis: 1,
			Status:   StatusAvailable,
		}
		utxos[i] = u
		p.utxos[u.Outpoint()] = &utxos[i]
	}

	p.logger.Info("minted UTXOs", "count", count, "txid", txid)
	return utxos, nil
}

// AddExisting adds pre-existing UTXOs to the pool.
func (p *MemoryPool) AddExisting(utxos []UTXO) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := range utxos {
		utxos[i].Status = StatusAvailable
		p.utxos[utxos[i].Outpoint()] = &utxos[i]
	}
}

// Lease finds an available UTXO, marks it as leased, and returns it.
func (p *MemoryPool) Lease() (*UTXO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for _, u := range p.utxos {
		if u.Status == StatusAvailable {
			u.Status = StatusLeased
			u.LeasedAt = now
			u.ExpiresAt = now.Add(p.leaseTTL)
			return u, nil
		}
	}

	return nil, fmt.Errorf("no UTXOs available (pool exhausted)")
}

// LeaseN leases exactly n UTXOs atomically. Used by the fee delegator
// to collect multiple 1-sat UTXOs for miner fees.
func (p *MemoryPool) LeaseN(n int) ([]*UTXO, error) {
	if n <= 0 {
		return nil, fmt.Errorf("n must be positive, got %d", n)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// First pass: count available to ensure we can satisfy the request
	available := make([]*UTXO, 0, n)
	for _, u := range p.utxos {
		if u.Status == StatusAvailable {
			available = append(available, u)
			if len(available) == n {
				break
			}
		}
	}

	if len(available) < n {
		return nil, fmt.Errorf("need %d UTXOs, only %d available", n, len(available))
	}

	// Second pass: mark all as leased
	now := time.Now()
	for _, u := range available {
		u.Status = StatusLeased
		u.LeasedAt = now
		u.ExpiresAt = now.Add(p.leaseTTL)
	}

	return available, nil
}

// MarkSpent marks a UTXO as spent after successful settlement.
func (p *MemoryPool) MarkSpent(txid string, vout uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := txid + ":" + uitoa(vout)
	if u, ok := p.utxos[key]; ok {
		u.Status = StatusSpent
	}
}

// Lookup returns the UTXO for a given outpoint, or nil if not found.
func (p *MemoryPool) Lookup(txid string, vout uint32) *UTXO {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := txid + ":" + uitoa(vout)
	return p.utxos[key]
}

// Available returns the number of available UTXOs.
func (p *MemoryPool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	count := 0
	for _, u := range p.utxos {
		if u.Status == StatusAvailable {
			count++
		}
	}
	return count
}

// Reclaim checks for expired leases and returns them to the available pool.
func (p *MemoryPool) Reclaim() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	reclaimed := 0
	for _, u := range p.utxos {
		if u.Status == StatusLeased && now.After(u.ExpiresAt) {
			u.Status = StatusAvailable
			u.LeasedAt = time.Time{}
			u.ExpiresAt = time.Time{}
			reclaimed++
		}
	}

	if reclaimed > 0 {
		p.logger.Info("reclaimed expired leases", "count", reclaimed)
	}
	return reclaimed
}

// StartReclaimLoop starts a background goroutine that periodically reclaims expired leases.
func (p *MemoryPool) StartReclaimLoop(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.Reclaim()
			case <-stop:
				return
			}
		}
	}()
}

// ListAvailable returns all UTXOs in the available state (read-only, no leasing).
func (p *MemoryPool) ListAvailable() ([]UTXO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	utxos := make([]UTXO, 0)
	for _, u := range p.utxos {
		if u.Status == StatusAvailable {
			utxos = append(utxos, *u)
		}
	}
	return utxos, nil
}

// ListLeased returns all UTXOs currently in the leased state.
func (p *MemoryPool) ListLeased() ([]UTXO, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	utxos := make([]UTXO, 0)
	for _, u := range p.utxos {
		if u.Status == StatusLeased {
			utxos = append(utxos, *u)
		}
	}
	return utxos, nil
}

// ReleaseLease returns a leased UTXO back to available state.
// Only acts if the UTXO is currently in leased status.
func (p *MemoryPool) ReleaseLease(txid string, vout uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := txid + ":" + uitoa(vout)
	if u, ok := p.utxos[key]; ok && u.Status == StatusLeased {
		u.Status = StatusAvailable
		u.LeasedAt = time.Time{}
		u.ExpiresAt = time.Time{}
	}
}

// Stats returns pool statistics including UTXO denomination.
func (p *MemoryPool) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	var stats PoolStats
	var utxoValue uint64
	for _, u := range p.utxos {
		switch u.Status {
		case StatusAvailable:
			stats.Available++
		case StatusLeased:
			stats.Leased++
		case StatusSpent:
			stats.Spent++
		}
		// All UTXOs in a pool have the same denomination (from fan-out).
		// Capture the first UTXO's value as the pool denomination.
		if utxoValue == 0 && u.Satoshis > 0 {
			utxoValue = u.Satoshis
		}
	}
	stats.Total = len(p.utxos)
	stats.UTXOValue = utxoValue
	return stats
}
