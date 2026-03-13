// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package challenge

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestBuildAndHash(t *testing.T) {
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	opts := BuildOptions{
		PayeeLockingScriptHex: "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		Amount:                100,
		Network:               "testnet",
		TTL:                   5 * time.Minute,
		BindHeaders:           []string{"Content-Type"},
		NonceUTXO: &NonceRef{
			TxID:             "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			Vout:             0,
			Satoshis:         1,
			LockingScriptHex: "76a914aabbccdd88ac",
		},
	}

	ch, err := Build(req, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Verify structure (spec-compliant flat fields)
	if ch.Scheme != Scheme {
		t.Errorf("scheme: got %s, want %s", ch.Scheme, Scheme)
	}
	if ch.V != Version {
		t.Errorf("v: got %s, want %s", ch.V, Version)
	}
	if ch.AmountSats != 100 {
		t.Errorf("amount_sats: got %d, want 100", ch.AmountSats)
	}
	if ch.PayeeLockingScriptHex != opts.PayeeLockingScriptHex {
		t.Errorf("payee_locking_script_hex: got %s, want %s", ch.PayeeLockingScriptHex, opts.PayeeLockingScriptHex)
	}
	if ch.ExpiresAt <= time.Now().Unix() {
		t.Error("expires_at should be in the future")
	}
	if !ch.RequireMempoolAccept {
		t.Error("require_mempool_accept should be true")
	}
	if ch.ConfirmationsRequired != 0 {
		t.Errorf("confirmations_required: got %d, want 0", ch.ConfirmationsRequired)
	}

	// Verify nonce UTXO is set
	if ch.NonceUTXO == nil {
		t.Fatal("nonce_utxo should be set")
	}
	if ch.NonceUTXO.TxID != opts.NonceUTXO.TxID {
		t.Errorf("nonce txid: got %s, want %s", ch.NonceUTXO.TxID, opts.NonceUTXO.TxID)
	}
	if ch.NonceUTXO.Vout != 0 {
		t.Errorf("nonce vout: got %d, want 0", ch.NonceUTXO.Vout)
	}
	if ch.NonceUTXO.Satoshis != 1 {
		t.Errorf("nonce satoshis: got %d, want 1", ch.NonceUTXO.Satoshis)
	}

	// Verify flat request binding fields
	if ch.Method != "GET" {
		t.Errorf("method: got %s, want GET", ch.Method)
	}
	if ch.Path != "/v1/expensive" {
		t.Errorf("path: got %s, want /v1/expensive", ch.Path)
	}
	if ch.Domain != "localhost:8402" {
		t.Errorf("domain: got %s, want localhost:8402", ch.Domain)
	}
	if ch.Query != "foo=bar" {
		t.Errorf("query: got %s, want foo=bar", ch.Query)
	}

	// Verify ComputeHash produces a stable hash
	hash1, err := ComputeHash(ch)
	if err != nil {
		t.Fatalf("ComputeHash: %v", err)
	}
	if hash1 == "" {
		t.Error("challenge hash should not be empty")
	}
	if len(hash1) != 64 {
		t.Errorf("hash length: got %d, want 64", len(hash1))
	}

	hash2, err := ComputeHash(ch)
	if err != nil {
		t.Fatalf("ComputeHash (2nd call): %v", err)
	}
	if hash1 != hash2 {
		t.Errorf("hash not stable: %s vs %s", hash1, hash2)
	}
}

func TestEncodeAndDecode(t *testing.T) {
	ch := &Challenge{
		V:                     Version,
		Scheme:                Scheme,
		AmountSats:            200,
		PayeeLockingScriptHex: "76a91489abcdefab88ac",
		ExpiresAt:             time.Now().Add(5 * time.Minute).Unix(),
		Domain:                "example.com",
		Method:                "POST",
		Path:                  "/api/data",
		NonceUTXO: &NonceRef{
			TxID:             "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			Vout:             2,
			Satoshis:         1,
			LockingScriptHex: "76a914aabb88ac",
		},
	}

	encoded, err := Encode(ch)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if encoded == "" {
		t.Error("encoded should not be empty")
	}

	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if decoded.Scheme != ch.Scheme {
		t.Errorf("scheme mismatch: got %s, want %s", decoded.Scheme, ch.Scheme)
	}
	if decoded.AmountSats != ch.AmountSats {
		t.Errorf("amount_sats mismatch: got %d, want %d", decoded.AmountSats, ch.AmountSats)
	}
	if decoded.Domain != ch.Domain {
		t.Errorf("domain mismatch: got %s, want %s", decoded.Domain, ch.Domain)
	}
	if decoded.NonceUTXO == nil {
		t.Fatal("nonce_utxo should survive encode/decode round-trip")
	}
	if decoded.NonceUTXO.TxID != ch.NonceUTXO.TxID {
		t.Errorf("nonce txid mismatch: got %s, want %s", decoded.NonceUTXO.TxID, ch.NonceUTXO.TxID)
	}
	if decoded.NonceUTXO.Vout != ch.NonceUTXO.Vout {
		t.Errorf("nonce vout mismatch: got %d, want %d", decoded.NonceUTXO.Vout, ch.NonceUTXO.Vout)
	}
}

func TestHashBody(t *testing.T) {
	h1 := HashBody([]byte("hello"))
	h2 := HashBody([]byte("hello"))
	h3 := HashBody([]byte("world"))

	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("hash length: got %d, want 64", len(h1))
	}
}

func TestHashBodyEmpty(t *testing.T) {
	h := HashBody(nil)
	if h == "" {
		t.Error("hash of nil should not be empty")
	}
	if len(h) != 64 {
		t.Errorf("hash length: got %d, want 64", len(h))
	}
}

func TestValidateSchemeVersion(t *testing.T) {
	ch := &Challenge{V: Version, Scheme: Scheme}
	if err := ValidateSchemeVersion(ch); err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}

	bad := &Challenge{V: "99", Scheme: Scheme}
	if err := ValidateSchemeVersion(bad); err == nil {
		t.Error("expected error for wrong version")
	}

	bad2 := &Challenge{V: Version, Scheme: "wrong"}
	if err := ValidateSchemeVersion(bad2); err == nil {
		t.Error("expected error for wrong scheme")
	}
}
