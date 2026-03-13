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
	"strings"
	"testing"
	"time"
)

func buildTestChallenge(t *testing.T) *Challenge {
	t.Helper()

	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	opts := BuildOptions{
		PayeeLockingScriptHex: "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac",
		Amount:                100,
		Network:               "testnet",
		TTL:                   5 * time.Minute,
		BindHeaders:           []string{"Content-Type"},
	}

	ch, err := Build(req, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return ch
}

func TestVerifyBinding_MatchingRequest(t *testing.T) {
	ch := buildTestChallenge(t)

	// Same request — should pass
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestVerifyBinding_MethodMismatch(t *testing.T) {
	ch := buildTestChallenge(t)

	// Different method
	req := httptest.NewRequest("POST", "http://localhost:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err == nil {
		t.Error("expected error for method mismatch")
	}
	if !strings.Contains(err.Error(), "method mismatch") {
		t.Errorf("expected 'method mismatch', got: %v", err)
	}
}

func TestVerifyBinding_PathMismatch(t *testing.T) {
	ch := buildTestChallenge(t)

	// Different path
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/other?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err == nil {
		t.Error("expected error for path mismatch")
	}
	if !strings.Contains(err.Error(), "path mismatch") {
		t.Errorf("expected 'path mismatch', got: %v", err)
	}
}

func TestVerifyBinding_DomainMismatch(t *testing.T) {
	ch := buildTestChallenge(t)

	// Different domain
	req := httptest.NewRequest("GET", "http://evil.com:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "application/json")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err == nil {
		t.Error("expected error for domain mismatch")
	}
	if !strings.Contains(err.Error(), "domain mismatch") {
		t.Errorf("expected 'domain mismatch', got: %v", err)
	}
}

func TestVerifyBinding_QueryMismatch(t *testing.T) {
	ch := buildTestChallenge(t)

	// Different query string — spec uses raw query, not hash
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive?foo=baz", nil)
	req.Header.Set("Content-Type", "application/json")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err == nil {
		t.Error("expected error for query mismatch")
	}
	if !strings.Contains(err.Error(), "query mismatch") {
		t.Errorf("expected 'query mismatch', got: %v", err)
	}
}

func TestVerifyBinding_HeaderMismatch(t *testing.T) {
	ch := buildTestChallenge(t)

	// Different Content-Type header
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive?foo=bar", nil)
	req.Header.Set("Content-Type", "text/plain")

	err := VerifyBinding(ch, req, []string{"Content-Type"})
	if err == nil {
		t.Error("expected error for headers hash mismatch")
	}
	if !strings.Contains(err.Error(), "headers hash mismatch") {
		t.Errorf("expected 'headers hash mismatch', got: %v", err)
	}
}
