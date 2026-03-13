// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package broadcast

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// ---------------------------------------------------------------------------
// MempoolChecker — independent mempool verification (CRIT-04)
// ---------------------------------------------------------------------------

// MempoolChecker verifies whether a transaction is visible in the mempool.
// This provides the gatekeeper with independent confirmation that the
// client's payment transaction has propagated to the network.
type MempoolChecker interface {
	// CheckMempool verifies transaction visibility in the mempool.
	// Returns:
	//   visible=true,  doubleSpend=false → tx found in mempool (200 path)
	//   visible=false, doubleSpend=false → tx not yet visible (202 path)
	//   visible=false, doubleSpend=true  → explicit double-spend detected (409 path)
	//   err != nil                       → check failed (503 path)
	CheckMempool(txid string) (visible bool, doubleSpend bool, err error)
}

// ---------------------------------------------------------------------------
// Swappable — runtime-switchable broadcaster wrapper
// ---------------------------------------------------------------------------

// Swappable wraps a Broadcaster and allows hot-swapping the inner
// implementation at runtime (e.g. switching from mock to WoC via dashboard).
// All components (pools, delegator, dashboard) hold a reference to the same
// Swappable, so swapping the inner broadcaster affects everything atomically.
type Swappable struct {
	mu    sync.RWMutex
	inner transaction.Broadcaster
	mode  string // "mock", "woc", etc.
}

// NewSwappable creates a new swappable broadcaster wrapper.
func NewSwappable(inner transaction.Broadcaster, mode string) *Swappable {
	return &Swappable{inner: inner, mode: mode}
}

// Broadcast delegates to the current inner broadcaster.
func (s *Swappable) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner.Broadcast(tx)
}

// BroadcastCtx delegates to the current inner broadcaster.
func (s *Swappable) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.inner.BroadcastCtx(ctx, tx)
}

// Swap replaces the inner broadcaster with a new one.
func (s *Swappable) Swap(inner transaction.Broadcaster, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inner = inner
	s.mode = mode
}

// Mode returns the current broadcaster mode name (e.g. "mock", "woc").
func (s *Swappable) Mode() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

// IsMock returns true if the current broadcaster is in mock/demo mode.
func (s *Swappable) IsMock() bool {
	return s.Mode() == "mock"
}

// Health returns the HealthTracker from the inner broadcaster if it's a
// CompositeBroadcaster. Returns nil otherwise (e.g. for WoC-only or mock).
func (s *Swappable) Health() *HealthTracker {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type healthProvider interface {
		Health() *HealthTracker
	}
	if hp, ok := s.inner.(healthProvider); ok {
		return hp.Health()
	}
	return nil
}

// SkipPrimary returns the circuit breaker state from the inner CompositeBroadcaster.
// Returns false if the inner broadcaster is not a CompositeBroadcaster.
func (s *Swappable) SkipPrimary() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type circuitBreaker interface {
		SkipPrimary() bool
	}
	if cb, ok := s.inner.(circuitBreaker); ok {
		return cb.SkipPrimary()
	}
	return false
}

// CheckMempool delegates to the inner broadcaster if it implements MempoolChecker.
// If the inner broadcaster does not implement MempoolChecker, returns (true, false, nil)
// as a safe fallback (assumes visible).
func (s *Swappable) CheckMempool(txid string) (bool, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if checker, ok := s.inner.(MempoolChecker); ok {
		return checker.CheckMempool(txid)
	}
	// Fallback: inner doesn't implement MempoolChecker — assume visible
	return true, false, nil
}

// ---------------------------------------------------------------------------
// MockBroadcaster — development/demo mode
// ---------------------------------------------------------------------------

// MockBroadcaster is a development broadcaster that accepts all transactions
// without actually broadcasting to the network.
type MockBroadcaster struct{}

// Broadcast accepts the transaction and returns its txid.
func (m *MockBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return &transaction.BroadcastSuccess{
		Txid:    tx.TxID().String(),
		Message: "mock broadcast accepted",
	}, nil
}

// BroadcastCtx accepts the transaction and returns its txid.
func (m *MockBroadcaster) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return m.Broadcast(tx)
}

// CheckMempool always returns visible for mock mode.
func (m *MockBroadcaster) CheckMempool(txid string) (bool, bool, error) {
	return true, false, nil
}

// ---------------------------------------------------------------------------
// WoCBroadcaster — WhatsOnChain (testnet/mainnet)
// ---------------------------------------------------------------------------

// WoCBroadcaster broadcasts transactions via the WhatsOnChain API.
type WoCBroadcaster struct {
	baseURL    string
	httpClient *http.Client
}

// NewWoCBroadcaster creates a WoC broadcaster using the given base URL
// (e.g. "https://api.whatsonchain.com/v1/bsv/main").
func NewWoCBroadcaster(baseURL string) *WoCBroadcaster {
	return &WoCBroadcaster{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Broadcast sends a raw transaction to WoC and returns the txid.
func (w *WoCBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	url := w.baseURL + "/tx/raw"

	body, _ := json.Marshal(map[string]string{
		"txhex": tx.Hex(),
	})

	resp, err := w.httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, &transaction.BroadcastFailure{
			Code:        "NETWORK_ERROR",
			Description: fmt.Sprintf("WoC request failed: %s", err),
		}
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, &transaction.BroadcastFailure{
			Code:        fmt.Sprintf("HTTP_%d", resp.StatusCode),
			Description: string(respBody),
		}
	}

	// WoC returns the txid as a JSON-quoted string with possible trailing newline
	txid := strings.TrimSpace(string(respBody))
	// Remove surrounding quotes if present
	if len(txid) >= 2 && txid[0] == '"' && txid[len(txid)-1] == '"' {
		txid = txid[1 : len(txid)-1]
	}

	return &transaction.BroadcastSuccess{
		Txid:    txid,
		Message: "WoC broadcast accepted",
	}, nil
}

// BroadcastCtx is the context-aware version.
func (w *WoCBroadcaster) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return w.Broadcast(tx)
}

// CheckMempool queries WoC to verify if a transaction is visible in the mempool.
// GET /v1/bsv/{network}/tx/{txid} — 200=found, 404=not found.
func (w *WoCBroadcaster) CheckMempool(txid string) (bool, bool, error) {
	url := w.baseURL + "/tx/" + txid

	resp, err := w.httpClient.Get(url)
	if err != nil {
		return false, false, fmt.Errorf("WoC mempool check failed: %w", err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body) // drain body

	switch resp.StatusCode {
	case http.StatusOK:
		return true, false, nil // tx visible
	case http.StatusNotFound:
		return false, false, nil // tx not yet visible
	case http.StatusTooManyRequests:
		// Rate-limited — treat as "not yet visible" so the polling loop retries
		// rather than aborting with a 503 error.
		return false, false, nil
	default:
		// Unexpected status — treat as error
		return false, false, fmt.Errorf("WoC mempool check returned HTTP %d", resp.StatusCode)
	}
}
