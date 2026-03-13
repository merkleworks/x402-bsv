// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package treasury

import (
	"context"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

// testBroadcaster is a mock broadcaster for tests.
type testBroadcaster struct {
	lastTx *transaction.Transaction
}

func (tb *testBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	tb.lastTx = tx
	return &transaction.BroadcastSuccess{
		Txid:    tx.TxID().String(),
		Message: "test broadcast",
	}, nil
}

func (tb *testBroadcaster) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return tb.Broadcast(tx)
}

func TestBuildFanout_Basic(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	bc := &testBroadcaster{}

	// Build a fan-out with 10 outputs
	result, err := BuildFanout(key, false, FanoutRequest{
		FundingTxID:     repeatHex("a", 64),
		FundingVout:     0,
		FundingScript:   "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		FundingSatoshis: 10000,
		OutputCount:     10,
		FeeRate:         0.001,
	}, bc)
	if err != nil {
		t.Fatalf("BuildFanout: %v", err)
	}

	if result.TxID == "" {
		t.Error("expected non-empty txid")
	}
	if len(result.UTXOs) != 10 {
		t.Errorf("expected 10 UTXOs, got %d", len(result.UTXOs))
	}

	// Verify each UTXO
	for i, u := range result.UTXOs {
		if u.TxID != result.TxID {
			t.Errorf("UTXO[%d] txid mismatch", i)
		}
		if u.Vout != uint32(i) {
			t.Errorf("UTXO[%d] vout: got %d, want %d", i, u.Vout, i)
		}
		if u.Satoshis != 1 {
			t.Errorf("UTXO[%d] satoshis: got %d, want 1", i, u.Satoshis)
		}
		if u.Script == "" {
			t.Errorf("UTXO[%d] has empty script", i)
		}
	}

	// Verify broadcaster was called
	if bc.lastTx == nil {
		t.Error("expected broadcaster to be called")
	}
}

func TestBuildFanout_InsufficientFunding(t *testing.T) {
	key, _ := ec.NewPrivateKey()
	bc := &testBroadcaster{}

	// Fund with only 5 sats but request 100 UTXOs (need at least 100 + fee)
	_, err := BuildFanout(key, false, FanoutRequest{
		FundingTxID:     repeatHex("b", 64),
		FundingVout:     0,
		FundingScript:   "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		FundingSatoshis: 5,
		OutputCount:     100,
		FeeRate:         0.001,
	}, bc)
	if err == nil {
		t.Error("expected error for insufficient funding")
	}
}

func TestBuildFanout_InvalidCount(t *testing.T) {
	key, _ := ec.NewPrivateKey()
	bc := &testBroadcaster{}

	_, err := BuildFanout(key, false, FanoutRequest{
		FundingTxID:     repeatHex("c", 64),
		FundingVout:     0,
		FundingScript:   "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		FundingSatoshis: 10000,
		OutputCount:     0,
		FeeRate:         0.001,
	}, bc)
	if err == nil {
		t.Error("expected error for zero output count")
	}

	_, err = BuildFanout(key, false, FanoutRequest{
		FundingTxID:     repeatHex("c", 64),
		FundingVout:     0,
		FundingScript:   "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		FundingSatoshis: 10000,
		OutputCount:     -1,
		FeeRate:         0.001,
	}, bc)
	if err == nil {
		t.Error("expected error for negative output count")
	}
}

func TestCeilSats(t *testing.T) {
	tests := []struct {
		input float64
		want  uint64
	}{
		{0.0, 0},
		{0.001, 1},
		{0.5, 1},
		{0.999, 1},
		{1.0, 1},
		{1.001, 2},
		{10.0, 10},
		{10.1, 11},
	}

	for _, tt := range tests {
		got := ceilSats(tt.input)
		if got != tt.want {
			t.Errorf("ceilSats(%f): got %d, want %d", tt.input, got, tt.want)
		}
	}
}

func repeatHex(ch string, length int) string {
	s := ""
	for len(s) < length {
		s += ch
	}
	return s[:length]
}
