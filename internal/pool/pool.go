// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package pool

import "time"

// Pool is the interface that all UTXO pool backends must implement.
// Both nonce pools (gatekeeper) and fee pools (fee delegator) satisfy this
// interface. Backends include in-memory (demo mode) and Redis (production).
type Pool interface {
	// Lease returns a single available UTXO and marks it as leased.
	// Returns an error if the pool is exhausted.
	Lease() (*UTXO, error)

	// LeaseN returns exactly n available UTXOs, each marked as leased.
	// Used by the fee delegator to collect multiple 1-sat UTXOs for miner fees.
	// Returns an error if fewer than n UTXOs are available.
	LeaseN(n int) ([]*UTXO, error)

	// Lookup returns the UTXO for a given outpoint, or nil if not found.
	Lookup(txid string, vout uint32) *UTXO

	// MarkSpent marks a UTXO as spent after successful settlement.
	MarkSpent(txid string, vout uint32)

	// AddExisting adds pre-existing UTXOs to the pool (e.g., from fan-out or persistence).
	AddExisting(utxos []UTXO)

	// Available returns the count of UTXOs in the available state.
	Available() int

	// Stats returns pool statistics.
	Stats() PoolStats

	// Address returns the BSV address that owns all UTXOs in this pool.
	Address() string

	// LockingScriptHex returns the P2PKH locking script hex for the pool address.
	LockingScriptHex() (string, error)

	// ListAvailable returns all UTXOs in the available state (read-only, no leasing).
	ListAvailable() ([]UTXO, error)

	// StartReclaimLoop starts a background goroutine that reclaims expired leases.
	StartReclaimLoop(interval time.Duration, stop <-chan struct{})
}

// Status represents the lifecycle state of a UTXO in the pool.
type Status string

const (
	StatusAvailable Status = "available"
	StatusLeased    Status = "leased"
	StatusSpent     Status = "spent"
)

// UTXO represents a single unspent transaction output managed by a pool.
// All UTXOs in x402 pools are 1-sat (both nonce and fee pools).
type UTXO struct {
	TxID      string    `json:"txid"`
	Vout      uint32    `json:"vout"`
	Script    string    `json:"script"`   // hex-encoded locking script (P2PKH)
	Satoshis  uint64    `json:"satoshis"` // always 1
	Status    Status    `json:"status"`
	LeasedAt  time.Time `json:"leased_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	// Mode-segregation metadata: tracks UTXO provenance for integrity checks.
	Synthetic  bool   `json:"synthetic,omitempty"`   // true for demo-generated UTXOs
	OriginMode string `json:"origin_mode,omitempty"` // "mock" or "live" — mode when UTXO was created

	// Profile B (Gateway Template mode) fields.
	// Empty/zero for Profile A nonces and all fee/payment pool UTXOs.
	RawTxTemplate     string `json:"rawtx_template,omitempty"`      // hex pre-signed template tx
	TemplatePriceSats uint64 `json:"template_price_sats,omitempty"` // price embedded in template
	EndpointClass     string `json:"endpoint_class,omitempty"`      // e.g. "basic"
	TemplateVersion   uint32 `json:"template_version,omitempty"`    // schema version for future migrations (current: 1)
}

// Outpoint returns the canonical "txid:vout" string for this UTXO.
func (u *UTXO) Outpoint() string {
	return u.TxID + ":" + uitoa(u.Vout)
}

// uitoa converts a uint32 to its string representation without importing strconv.
func uitoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	buf := make([]byte, 0, 10)
	for v > 0 {
		buf = append(buf, byte('0'+v%10))
		v /= 10
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

// PoolStats provides aggregate statistics for a UTXO pool.
type PoolStats struct {
	Total       int    `json:"total"`
	Available   int    `json:"available"`
	Leased      int    `json:"leased"`
	Spent       int    `json:"spent"`
	Quarantined int    `json:"quarantined"` // UTXOs removed by integrity check
	UTXOValue   uint64 `json:"utxo_value"`  // denomination per UTXO (sats)
}
