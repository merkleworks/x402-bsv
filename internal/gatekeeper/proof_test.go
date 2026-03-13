// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package gatekeeper

import (
	"strings"
	"testing"
)

func TestParseAndEncodeProof(t *testing.T) {
	original := &Proof{
		V:               "1",
		Scheme:          "bsv-tx-v1",
		TxID:            strings.Repeat("a", 64),
		RawTxB64:        "AQAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
		ChallengeSHA256: "abc123def456",
		Request: RequestBinding{
			Domain: "localhost:8402",
			Method: "GET",
			Path:   "/v1/expensive",
		},
	}

	encoded, err := EncodeProof(original)
	if err != nil {
		t.Fatalf("EncodeProof: %v", err)
	}
	if encoded == "" {
		t.Error("encoded should not be empty")
	}

	parsed, err := ParseProof(encoded)
	if err != nil {
		t.Fatalf("ParseProof: %v", err)
	}

	if parsed.V != original.V {
		t.Errorf("v mismatch: got %s, want %s", parsed.V, original.V)
	}
	if parsed.Scheme != original.Scheme {
		t.Errorf("scheme mismatch: got %s, want %s", parsed.Scheme, original.Scheme)
	}
	if parsed.TxID != original.TxID {
		t.Errorf("txid mismatch: got %s, want %s", parsed.TxID, original.TxID)
	}
	if parsed.RawTxB64 != original.RawTxB64 {
		t.Errorf("rawtx_b64 mismatch: got %s, want %s", parsed.RawTxB64, original.RawTxB64)
	}
	if parsed.ChallengeSHA256 != original.ChallengeSHA256 {
		t.Errorf("challenge_sha256 mismatch: got %s, want %s", parsed.ChallengeSHA256, original.ChallengeSHA256)
	}
	if parsed.Request.Domain != original.Request.Domain {
		t.Errorf("request.domain mismatch: got %s, want %s", parsed.Request.Domain, original.Request.Domain)
	}
}

func TestParseProofEmpty(t *testing.T) {
	_, err := ParseProof("")
	if err == nil {
		t.Error("expected error for empty header")
	}
}

func TestParseProofMissingRawTx(t *testing.T) {
	// Valid base64url JSON but missing rawtx_b64
	proof := &Proof{
		V:               "1",
		Scheme:          "bsv-tx-v1",
		RawTxB64:        "",
		ChallengeSHA256: "abc123",
	}
	encoded, _ := EncodeProof(proof)
	_, err := ParseProof(encoded)
	if err == nil {
		t.Error("expected error for missing rawtx_b64")
	}
}

func TestParseProofMissingChallengeHash(t *testing.T) {
	// Valid base64url JSON but missing challenge_sha256
	proof := &Proof{
		V:               "1",
		Scheme:          "bsv-tx-v1",
		RawTxB64:        "AQAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
		ChallengeSHA256: "",
	}
	encoded, _ := EncodeProof(proof)
	_, err := ParseProof(encoded)
	if err == nil {
		t.Error("expected error for missing challenge_sha256")
	}
}

func TestParseProofCompactPrefix(t *testing.T) {
	// Test v1.bsv-tx.<base64url> compact format
	original := &Proof{
		V:               "1",
		Scheme:          "bsv-tx-v1",
		TxID:            strings.Repeat("b", 64),
		RawTxB64:        "AQAAAAABAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA==",
		ChallengeSHA256: "deadbeef",
	}

	encoded, err := EncodeProof(original)
	if err != nil {
		t.Fatalf("EncodeProof: %v", err)
	}

	// Prepend compact prefix
	compactEncoded := "v1.bsv-tx." + encoded

	parsed, err := ParseProof(compactEncoded)
	if err != nil {
		t.Fatalf("ParseProof with compact prefix: %v", err)
	}

	if parsed.TxID != original.TxID {
		t.Errorf("txid mismatch: got %s, want %s", parsed.TxID, original.TxID)
	}
}

func TestHTTPStatusForError(t *testing.T) {
	tests := []struct {
		code   ErrorCode
		status int
	}{
		{ErrInvalidVersion, 400},
		{ErrInvalidScheme, 400},
		{ErrInvalidProof, 400},
		{ErrChallengeNotFound, 400},
		{ErrExpiredChallenge, 402},
		{ErrMempoolRejected, 409},
		{ErrInsufficientAmount, 402},
		{ErrInvalidBinding, 403},
		{ErrInvalidPayee, 403},
		{ErrDoubleSpend, 409},
		{ErrNoUTXOsAvailable, 503},
		{ErrInternalError, 500},
	}

	for _, tt := range tests {
		got := HTTPStatusForError(tt.code)
		if got != tt.status {
			t.Errorf("HTTPStatusForError(%s): got %d, want %d", tt.code, got, tt.status)
		}
	}
}
