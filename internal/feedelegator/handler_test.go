// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package feedelegator

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// newTestHandler creates a fee delegator handler with an in-memory pool seeded with UTXOs.
func newTestHandler(t *testing.T, feeUTXOCount int) (*Handler, *pool.MemoryPool) {
	t.Helper()

	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	feePool, err := pool.NewMemoryPool(key, false, 5*time.Minute, &mockBroadcaster{})
	if err != nil {
		t.Fatalf("create fee pool: %v", err)
	}

	// Seed fee pool with 1-sat UTXOs
	scriptHex, err := feePool.LockingScriptHex()
	if err != nil {
		t.Fatalf("locking script: %v", err)
	}
	utxos := make([]pool.UTXO, feeUTXOCount)
	for i := 0; i < feeUTXOCount; i++ {
		utxos[i] = pool.UTXO{
			TxID:     repeatHex("f", 64),
			Vout:     uint32(i),
			Script:   scriptHex,
			Satoshis: 1,
		}
	}
	feePool.AddExisting(utxos)

	handler, err := NewHandler(key, false, feePool, 0.001) // 1 sat/KB
	if err != nil {
		t.Fatalf("create handler: %v", err)
	}

	return handler, feePool
}

// mockBroadcaster for tests
type mockBroadcaster struct{}

func (m *mockBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return &transaction.BroadcastSuccess{Txid: tx.TxID().String()}, nil
}

func (m *mockBroadcaster) BroadcastCtx(_ context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return m.Broadcast(tx)
}

// buildClientInput creates a signed P2PKH input for testing.
func buildClientInput(t *testing.T) (string, string, uint64) {
	t.Helper()

	key, _ := ec.NewPrivateKey()
	addr, _ := script.NewAddressFromPublicKey(key.PubKey(), false)
	lockScript, _ := p2pkh.Lock(addr)
	lockScriptHex := hex.EncodeToString(*lockScript)

	// Create a funding tx (simulated)
	fundingTx := transaction.NewTransaction()
	fundingTx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      1000,
		LockingScript: lockScript,
	})

	txid := fundingTx.TxID().String()
	return txid, lockScriptHex, 1000
}

func TestHandleDelegateTx_Success(t *testing.T) {
	handler, feePool := newTestHandler(t, 10)

	clientTxID, clientScript, clientSats := buildClientInput(t)

	// Build a valid request
	req := DelegateRequest{
		TxJSON: TxJSON{
			Inputs: []TxInput{
				{
					TxID:     clientTxID,
					Vout:     0,
					Satoshis: clientSats,
				},
			},
			Outputs: []TxOutput{
				{
					Satoshis: 100,
					Script:   clientScript,
				},
			},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp DelegateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if !resp.Success {
		t.Error("expected success=true")
	}
	if resp.TxID == "" {
		t.Error("expected non-empty txid")
	}
	if resp.RawTx == "" {
		t.Error("expected non-empty rawtx")
	}
	if resp.Fee == 0 {
		t.Error("expected non-zero fee")
	}
	if resp.Mode != "raw_transaction_returned" {
		t.Errorf("expected mode=raw_transaction_returned, got %s", resp.Mode)
	}

	// Verify fee UTXOs were consumed
	beforeAvailable := feePool.Available()
	if beforeAvailable >= 10 {
		t.Errorf("expected fewer than 10 fee UTXOs available (some should be spent), got %d", beforeAvailable)
	}
}

func TestHandleDelegateTx_EmptyInputs(t *testing.T) {
	handler, _ := newTestHandler(t, 10)

	req := DelegateRequest{
		TxJSON: TxJSON{
			Inputs:  []TxInput{},
			Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleDelegateTx_EmptyOutputs(t *testing.T) {
	handler, _ := newTestHandler(t, 10)

	req := DelegateRequest{
		TxJSON: TxJSON{
			Inputs:  []TxInput{{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 100}},
			Outputs: []TxOutput{},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleDelegateTx_InvalidTxID(t *testing.T) {
	handler, _ := newTestHandler(t, 10)

	req := DelegateRequest{
		TxJSON: TxJSON{
			Inputs:  []TxInput{{TxID: "not-hex", Vout: 0, Satoshis: 100}},
			Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleDelegateTx_InsufficientFeeUTXOs(t *testing.T) {
	handler, _ := newTestHandler(t, 0) // No fee UTXOs

	req := DelegateRequest{
		TxJSON: TxJSON{
			Inputs: []TxInput{
				{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 1000},
			},
			Outputs: []TxOutput{
				{Satoshis: 100, Script: "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac"},
			},
		},
	}

	body, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleDelegateTx_InvalidBody(t *testing.T) {
	handler, _ := newTestHandler(t, 10)

	r := httptest.NewRequest("POST", "/api/v1/tx", bytes.NewReader([]byte("not json")))
	w := httptest.NewRecorder()

	handler.HandleDelegateTx().ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	handler, _ := newTestHandler(t, 5)
	startTime := time.Now().Add(-10 * time.Second)

	r := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	handler.HandleHealth(startTime).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["success"] != true {
		t.Error("expected success=true")
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", resp["status"])
	}
	if resp["uptime"] == nil || resp["uptime"].(float64) < 10 {
		t.Errorf("expected uptime >= 10, got %v", resp["uptime"])
	}
}

func TestHandleUTXOStats(t *testing.T) {
	handler, feePool := newTestHandler(t, 5)

	r := httptest.NewRequest("GET", "/api/utxo/stats", nil)
	w := httptest.NewRecorder()

	handler.HandleUTXOStats(false).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["success"] != true {
		t.Error("expected success=true")
	}
	if resp["redisEnabled"] != false {
		t.Error("expected redisEnabled=false")
	}
	_ = feePool // used implicitly via handler
}

func TestHandleUTXOHealth(t *testing.T) {
	handler, _ := newTestHandler(t, 5)

	r := httptest.NewRequest("GET", "/api/utxo/health", nil)
	w := httptest.NewRecorder()

	handler.HandleUTXOHealth().ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["healthy"] != true {
		t.Errorf("expected healthy=true, got %v", resp["healthy"])
	}
	if resp["status"] != "healthy" {
		t.Errorf("expected status=healthy, got %v", resp["status"])
	}
}

func TestHandleUTXOHealth_Degraded(t *testing.T) {
	handler, _ := newTestHandler(t, 0) // No fee UTXOs → degraded

	r := httptest.NewRequest("GET", "/api/utxo/health", nil)
	w := httptest.NewRecorder()

	handler.HandleUTXOHealth().ServeHTTP(w, r)

	var resp map[string]any
	json.Unmarshal(w.Body.Bytes(), &resp)

	if resp["healthy"] != false {
		t.Errorf("expected healthy=false, got %v", resp["healthy"])
	}
	if resp["status"] != "degraded" {
		t.Errorf("expected status=degraded, got %v", resp["status"])
	}
}

func TestValidateRequest(t *testing.T) {
	tests := []struct {
		name    string
		req     DelegateRequest
		wantErr bool
	}{
		{
			name: "valid request",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
				},
			},
			wantErr: false,
		},
		{
			name: "empty inputs",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{},
					Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty outputs",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid txid length",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: "abc", Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid txid hex",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: repeatHex("g", 64), Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty txid",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: "", Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: "76a91489abcdefab88ac"}},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid output script",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: "not-hex"}},
				},
			},
			wantErr: true,
		},
		{
			name: "empty output script",
			req: DelegateRequest{
				TxJSON: TxJSON{
					Inputs:  []TxInput{{TxID: repeatHex("a", 64), Vout: 0, Satoshis: 100}},
					Outputs: []TxOutput{{Satoshis: 100, Script: ""}},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequest(&tt.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCalculateFee(t *testing.T) {
	handler, _ := newTestHandler(t, 0)

	// Build a minimal transaction
	tx := transaction.NewTransaction()

	// The fee should be at least 1 sat (minimum)
	fee := handler.calculateFee(tx)
	if fee < 1 {
		t.Errorf("expected fee >= 1, got %d", fee)
	}
}

func repeatHex(ch string, length int) string {
	s := ""
	for len(s) < length {
		s += ch
	}
	return s[:length]
}
