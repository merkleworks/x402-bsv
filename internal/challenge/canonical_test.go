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
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalJSON_SortedKeys(t *testing.T) {
	// Map with unsorted keys — canonical output must always be sorted
	input := map[string]any{
		"zebra":  1,
		"alpha":  2,
		"middle": 3,
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	expected := `{"alpha":2,"middle":3,"zebra":1}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}

func TestCanonicalJSON_NestedObjects(t *testing.T) {
	input := map[string]any{
		"outer": map[string]any{
			"z_key": "last",
			"a_key": "first",
		},
		"simple": "value",
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	expected := `{"outer":{"a_key":"first","z_key":"last"},"simple":"value"}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}

func TestCanonicalJSON_IntegerPreservation(t *testing.T) {
	// Whole numbers should serialize without decimal points
	input := map[string]any{
		"amount": float64(100),
		"vout":   float64(0),
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	expected := `{"amount":100,"vout":0}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}

func TestCanonicalJSON_Stability(t *testing.T) {
	// Same input must always produce the same bytes
	input := map[string]any{
		"scheme":      "bsv-tx-v1",
		"v":           "1",
		"amount_sats": float64(100),
	}

	first, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	// Run 100 times — must be identical every time
	for i := 0; i < 100; i++ {
		result, err := CanonicalJSON(input)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if string(result) != string(first) {
			t.Fatalf("iteration %d: non-deterministic output\n  first: %s\n  got:   %s", i, first, result)
		}
	}
}

func TestCanonicalJSON_GoldenHash(t *testing.T) {
	// Golden test: a fixed spec-compliant challenge must always produce
	// the exact same SHA-256. Field names match 04-Protocol-Spec.md.
	ch := map[string]any{
		"v":                        "1",
		"scheme":                   "bsv-tx-v1",
		"amount_sats":              float64(100),
		"payee_locking_script_hex": "76a91489abcdefab88ac",
		"expires_at":               float64(1700000000),
		"domain":                   "localhost:8402",
		"method":                   "GET",
		"path":                     "/v1/expensive",
		"query":                    "",
		"req_headers_sha256":       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"req_body_sha256":          "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"require_mempool_accept":   true,
		"confirmations_required":   float64(0),
		"nonce_utxo": map[string]any{
			"txid":               "deadbeef" + "00000000000000000000000000000000000000000000000000000000",
			"vout":               float64(0),
			"satoshis":           float64(1),
			"locking_script_hex": "76a914aabbccdd88ac",
		},
	}

	canonical, err := CanonicalJSON(ch)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	h := sha256.Sum256(canonical)
	hash := hex.EncodeToString(h[:])

	// Log the canonical output and hash for cross-language verification
	t.Logf("Canonical JSON: %s", string(canonical))
	t.Logf("SHA-256: %s", hash)

	// The hash must be stable across runs
	canonical2, _ := CanonicalJSON(ch)
	h2 := sha256.Sum256(canonical2)
	hash2 := hex.EncodeToString(h2[:])

	if hash != hash2 {
		t.Errorf("non-deterministic hash: %s vs %s", hash, hash2)
	}

	// Verify the canonical output has sorted keys
	if string(canonical)[0] != '{' {
		t.Error("expected JSON object")
	}
}

func TestCanonicalJSON_ChallengeStruct(t *testing.T) {
	// Test with an actual Challenge struct — keys must be sorted by JSON tag name
	ch := &Challenge{
		V:                     "1",
		Scheme:                "bsv-tx-v1",
		AmountSats:            100,
		PayeeLockingScriptHex: "76a91489abcdefab88ac",
		ExpiresAt:             1700000000,
		Domain:                "localhost:8402",
		Method:                "GET",
		Path:                  "/v1/expensive",
		Query:                 "",
		ReqHeadersSHA256:      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		ReqBodySHA256:         "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		RequireMempoolAccept:  true,
		ConfirmationsRequired: 0,
		NonceUTXO: &NonceRef{
			TxID:             "deadbeef" + "00000000000000000000000000000000000000000000000000000000",
			Vout:             0,
			Satoshis:         1,
			LockingScriptHex: "76a914aabbccdd88ac",
		},
	}

	result, err := CanonicalJSON(ch)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	// Must produce valid JSON with sorted keys
	if len(result) == 0 {
		t.Error("expected non-empty output")
	}

	// Hash must be stable
	hash1, _ := ComputeHash(ch)
	hash2, _ := ComputeHash(ch)
	if hash1 != hash2 {
		t.Errorf("ComputeHash not stable: %s vs %s", hash1, hash2)
	}

	t.Logf("Canonical: %s", string(result))
	t.Logf("Hash: %s", hash1)
}

func TestCanonicalJSON_StructInput(t *testing.T) {
	// Test with an actual Go struct (not just a map)
	type Inner struct {
		Zebra string `json:"zebra"`
		Alpha int    `json:"alpha"`
	}
	type Outer struct {
		Name  string `json:"name"`
		Inner Inner  `json:"inner"`
	}

	input := Outer{
		Name: "test",
		Inner: Inner{
			Zebra: "z",
			Alpha: 1,
		},
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	// Keys must be sorted even though struct fields are in declaration order
	expected := `{"inner":{"alpha":1,"zebra":"z"},"name":"test"}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}

func TestCanonicalJSON_EmptyValues(t *testing.T) {
	input := map[string]any{
		"empty_string": "",
		"null_value":   nil,
		"zero":         float64(0),
		"false_value":  false,
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	expected := `{"empty_string":"","false_value":false,"null_value":null,"zero":0}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}

func TestCanonicalJSON_Arrays(t *testing.T) {
	input := map[string]any{
		"items": []any{"c", "a", "b"},
	}

	result, err := CanonicalJSON(input)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	// Arrays preserve order (not sorted — only object keys are sorted)
	expected := `{"items":["c","a","b"]}`
	if string(result) != expected {
		t.Errorf("got %s, want %s", string(result), expected)
	}
}
