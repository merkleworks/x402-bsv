// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package delegator

import (
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
)

// mockScriptSig builds a minimal P2PKH-shaped scriptSig with a given sighash byte.
// extractSighashByte reads [pushLen] [sig_data...sighashByte] [pushLen] [pubkey...].
// We use a 1-byte sig data containing only the sighash byte for simplicity.
func mockScriptSig(sighashByte byte) *script.Script {
	s := script.Script([]byte{
		0x01, sighashByte, // push 1 byte = sighash byte (stands in for DER sig + sighash)
		0x01, 0x00, // push 1 byte = fake pubkey
	})
	return &s
}

// mockTxWithInputs creates a transaction with n inputs, each having the given sighash byte.
// All inputs reference a deterministic outpoint.
func mockTxWithInputs(sighashByte byte, n int) *transaction.Transaction {
	tx := transaction.NewTransaction()
	for i := 0; i < n; i++ {
		txid, _ := chainhash.NewHashFromHex("a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2")
		input := &transaction.TransactionInput{
			SourceTXID:       txid,
			SourceTxOutIndex: uint32(i),
			UnlockingScript:  mockScriptSig(sighashByte),
			SequenceNumber:   0xFFFFFFFF,
		}
		tx.AddInput(input)
	}
	return tx
}

// mockTxWithMixedSighash creates a transaction where input 0 has sighash0 and
// all subsequent inputs have sighash1.
func mockTxWithMixedSighash(sighash0, sighash1 byte, n int) *transaction.Transaction {
	tx := transaction.NewTransaction()
	for i := 0; i < n; i++ {
		sh := sighash1
		if i == 0 {
			sh = sighash0
		}
		txid, _ := chainhash.NewHashFromHex("a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2")
		input := &transaction.TransactionInput{
			SourceTXID:       txid,
			SourceTxOutIndex: uint32(i),
			UnlockingScript:  mockScriptSig(sh),
			SequenceNumber:   0xFFFFFFFF,
		}
		tx.AddInput(input)
	}
	return tx
}

// ── enforceSighash tests ─────────────────────────────────────────────────────

func TestEnforceSighash_TemplateMode_Accepts0xC3OnInput0(t *testing.T) {
	// Profile B: input 0 signed with 0xC3 (gateway template), rest with 0xC1
	tx := mockTxWithMixedSighash(templateSighashByte, requiredSighashByte, 3)
	if err := enforceSighash(tx, true); err != nil {
		t.Fatalf("expected pass for 0xC3 on input 0 in template mode, got: %v", err)
	}
}

func TestEnforceSighash_TemplateMode_Rejects0xC1OnInput0(t *testing.T) {
	// Profile B requires 0xC3 on input 0 — 0xC1 commits to all outputs,
	// which breaks template extensibility (sponsors can't add change outputs).
	tx := mockTxWithMixedSighash(requiredSighashByte, requiredSighashByte, 3)
	err := enforceSighash(tx, true)
	if err == nil {
		t.Fatal("expected error for 0xC1 on input 0 in template mode")
	}
}

func TestEnforceSighash_TemplateMode_RejectsWrongSighash(t *testing.T) {
	// Profile B: input 0 with a sighash that is neither 0xC3 nor 0xC1 must fail
	tx := mockTxWithMixedSighash(0x42, requiredSighashByte, 2)
	err := enforceSighash(tx, true)
	if err == nil {
		t.Fatal("expected error for 0x42 on input 0 in template mode")
	}
}

func TestEnforceSighash_TemplateMode_Rejects0xC3OnInput1(t *testing.T) {
	// Profile B: only input 0 may use 0xC3; input 1 with 0xC3 must fail
	tx := mockTxWithMixedSighash(templateSighashByte, templateSighashByte, 2)
	err := enforceSighash(tx, true)
	if err == nil {
		t.Fatal("expected error for 0xC3 on input 1 in template mode")
	}
}

func TestEnforceSighash_ProfileA_Rejects0xC3(t *testing.T) {
	// Profile A: all inputs must be 0xC1; 0xC3 on input 0 must fail
	tx := mockTxWithMixedSighash(templateSighashByte, requiredSighashByte, 2)
	err := enforceSighash(tx, false)
	if err == nil {
		t.Fatal("expected error for 0xC3 on input 0 in Profile A mode")
	}
}

func TestEnforceSighash_ProfileA_AllC1_Passes(t *testing.T) {
	// Profile A: all inputs 0xC1 → pass
	tx := mockTxWithInputs(requiredSighashByte, 3)
	if err := enforceSighash(tx, false); err != nil {
		t.Fatalf("expected pass for all 0xC1 in Profile A, got: %v", err)
	}
}

func TestEnforceSighash_ProfileA_Accepts0x41(t *testing.T) {
	// Profile A: 0x41 (SIGHASH_ALL|FORKID without ANYONECANPAY) is also valid
	tx := mockTxWithInputs(allForkIDSighashByte, 3)
	if err := enforceSighash(tx, false); err != nil {
		t.Fatalf("expected pass for all 0x41 in Profile A, got: %v", err)
	}
}

func TestEnforceSighash_ProfileA_Mixed0xC1And0x41_Passes(t *testing.T) {
	// Profile A: mix of 0xC1 and 0x41 is valid
	tx := mockTxWithMixedSighash(allForkIDSighashByte, requiredSighashByte, 3)
	if err := enforceSighash(tx, false); err != nil {
		t.Fatalf("expected pass for mixed 0x41/0xC1 in Profile A, got: %v", err)
	}
}

func TestEnforceSighash_TemplateMode_SponsorAccepts0x41(t *testing.T) {
	// Profile B: sponsor inputs (index >= 1) may use 0x41
	tx := mockTxWithMixedSighash(templateSighashByte, allForkIDSighashByte, 3)
	if err := enforceSighash(tx, true); err != nil {
		t.Fatalf("expected pass for 0x41 on sponsor inputs in template mode, got: %v", err)
	}
}

func TestEnforceSighash_EmptyUnlockingScript(t *testing.T) {
	tx := transaction.NewTransaction()
	txid, _ := chainhash.NewHashFromHex("a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2")
	emptyScript := script.Script([]byte{})
	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       txid,
		SourceTxOutIndex: 0,
		UnlockingScript:  &emptyScript,
		SequenceNumber:   0xFFFFFFFF,
	})
	err := enforceSighash(tx, false)
	if err == nil {
		t.Fatal("expected error for empty unlocking script")
	}
}

// ── verifyNonceAtIndex0 tests ────────────────────────────────────────────────

func TestVerifyNonceAtIndex0_Valid(t *testing.T) {
	nonceTxID := "a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2"
	var nonceVout uint32 = 0

	tx := transaction.NewTransaction()
	txid, _ := chainhash.NewHashFromHex(nonceTxID)
	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       txid,
		SourceTxOutIndex: nonceVout,
		UnlockingScript:  mockScriptSig(0xC3),
		SequenceNumber:   0xFFFFFFFF,
	})

	ref := &NonceOutpointRef{TxID: nonceTxID, Vout: nonceVout}
	if err := verifyNonceAtIndex0(tx, ref); err != nil {
		t.Fatalf("expected pass for nonce at index 0, got: %v", err)
	}
}

func TestVerifyNonceAtIndex0_WrongPosition(t *testing.T) {
	nonceTxID := "a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2"
	otherTxID := "b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2d3d4d5d6d7d8d9d0e1e2"

	tx := transaction.NewTransaction()
	// Input 0: some other UTXO (not the nonce)
	otherHash, _ := chainhash.NewHashFromHex(otherTxID)
	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       otherHash,
		SourceTxOutIndex: 0,
		UnlockingScript:  mockScriptSig(0xC1),
		SequenceNumber:   0xFFFFFFFF,
	})
	// Input 1: the nonce (wrong position!)
	nonceHash, _ := chainhash.NewHashFromHex(nonceTxID)
	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       nonceHash,
		SourceTxOutIndex: 0,
		UnlockingScript:  mockScriptSig(0xC3),
		SequenceNumber:   0xFFFFFFFF,
	})

	ref := &NonceOutpointRef{TxID: nonceTxID, Vout: 0}
	err := verifyNonceAtIndex0(tx, ref)
	if err == nil {
		t.Fatal("expected error when nonce is at input index 1 instead of 0")
	}
}

func TestVerifyNonceAtIndex0_NilNonce(t *testing.T) {
	tx := mockTxWithInputs(0xC3, 1)
	err := verifyNonceAtIndex0(tx, nil)
	if err == nil {
		t.Fatal("expected error for nil nonce ref")
	}
}

// ── extractSighashByte tests ─────────────────────────────────────────────────

func TestExtractSighashByte_Valid(t *testing.T) {
	cases := []struct {
		name     string
		sighash  byte
	}{
		{"0xC1_SIGHASH_ALL_ANYONECANPAY_FORKID", 0xC1},
		{"0xC3_SIGHASH_SINGLE_ANYONECANPAY_FORKID", 0xC3},
		{"0x41_SIGHASH_ALL_FORKID", 0x41},
		{"0x43_SIGHASH_SINGLE_FORKID", 0x43},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := *mockScriptSig(tc.sighash)
			got, err := extractSighashByte(s)
			if err != nil {
				t.Fatalf("extractSighashByte: %v", err)
			}
			if got != tc.sighash {
				t.Errorf("got 0x%02X, want 0x%02X", got, tc.sighash)
			}
		})
	}
}

func TestExtractSighashByte_TooShort(t *testing.T) {
	s := script.Script([]byte{0x01})
	_, err := extractSighashByte(s)
	if err == nil {
		t.Fatal("expected error for 1-byte scriptSig")
	}
}

func TestExtractSighashByte_BadPushOpcode(t *testing.T) {
	// Push opcode > 75 (invalid for direct data push)
	s := script.Script([]byte{0x4C, 0xC1}) // 0x4C = 76
	_, err := extractSighashByte(s)
	if err == nil {
		t.Fatal("expected error for push opcode > 75")
	}
}
