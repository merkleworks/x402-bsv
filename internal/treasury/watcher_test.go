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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

// newTestWatcher creates a TreasuryWatcher pointing at the given mock server URL.
func newTestWatcher(t *testing.T, baseURL string) *TreasuryWatcher {
	t.Helper()
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}

	tw, err := NewTreasuryWatcher(true, "1TestAddress", key, 60*time.Second, nil, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	return tw
}

func TestPoll(t *testing.T) {
	items := []wocUnspent{
		{Height: 800000, TxPos: 0, TxHash: "aaaa" + "0000000000000000000000000000000000000000000000000000000000000000"[:60], Value: 50000},
		{Height: 800001, TxPos: 1, TxHash: "bbbb" + "0000000000000000000000000000000000000000000000000000000000000000"[:60], Value: 10000},
	}
	body, _ := json.Marshal(items)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)
	if err := tw.poll(); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	utxos := tw.GetUTXOs()
	if len(utxos) != 2 {
		t.Fatalf("expected 2 UTXOs, got %d", len(utxos))
	}

	// Should be sorted by value descending
	if utxos[0].Satoshis != 50000 {
		t.Errorf("expected first UTXO to have 50000 sats, got %d", utxos[0].Satoshis)
	}
	if utxos[1].Satoshis != 10000 {
		t.Errorf("expected second UTXO to have 10000 sats, got %d", utxos[1].Satoshis)
	}

	// Verify fields
	if utxos[0].Vout != 0 {
		t.Errorf("expected vout 0, got %d", utxos[0].Vout)
	}
	if utxos[0].Script == "" {
		t.Error("expected non-empty script")
	}

	// LastPoll should be set
	lastPoll, lastErr := tw.LastPoll()
	if lastPoll.IsZero() {
		t.Error("expected lastPoll to be set")
	}
	if lastErr != nil {
		t.Errorf("expected no error, got %v", lastErr)
	}
}

func TestEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)
	if err := tw.poll(); err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	utxos := tw.GetUTXOs()
	if utxos == nil {
		t.Fatal("expected non-nil slice")
	}
	if len(utxos) != 0 {
		t.Fatalf("expected 0 UTXOs, got %d", len(utxos))
	}
}

func TestNotFoundResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("Address not found"))
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)
	if err := tw.poll(); err != nil {
		t.Fatalf("poll should succeed on 404, got: %v", err)
	}

	utxos := tw.GetUTXOs()
	if len(utxos) != 0 {
		t.Fatalf("expected 0 UTXOs on 404, got %d", len(utxos))
	}
}

func TestGetFunding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []wocUnspent{
			{TxHash: "aaa0000000000000000000000000000000000000000000000000000000000000", TxPos: 0, Value: 50000},
			{TxHash: "bbb0000000000000000000000000000000000000000000000000000000000000", TxPos: 1, Value: 10000},
			{TxHash: "ccc0000000000000000000000000000000000000000000000000000000000000", TxPos: 2, Value: 500},
		}
		body, _ := json.Marshal(items)
		w.Write(body)
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)
	tw.poll()

	// Should return the 50000-sat UTXO (first match ≥ 20000)
	u, err := tw.GetFunding(20000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u == nil {
		t.Fatal("expected a UTXO")
	}
	if u.Satoshis != 50000 {
		t.Errorf("expected 50000 sats, got %d", u.Satoshis)
	}

	// Should return 10000-sat UTXO (first match ≥ 5000)
	u, err = tw.GetFunding(5000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u == nil {
		t.Fatal("expected a UTXO")
	}
	if u.Satoshis != 50000 {
		t.Errorf("expected 50000 sats (largest first, sorted descending), got %d", u.Satoshis)
	}
}

func TestGetFundingNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := []wocUnspent{
			{TxHash: "aaa0000000000000000000000000000000000000000000000000000000000000", TxPos: 0, Value: 500},
			{TxHash: "bbb0000000000000000000000000000000000000000000000000000000000000", TxPos: 1, Value: 200},
		}
		body, _ := json.Marshal(items)
		w.Write(body)
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)
	tw.poll()

	// All UTXOs are below 10000
	u, err := tw.GetFunding(10000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u != nil {
		t.Fatalf("expected nil, got UTXO with %d sats", u.Satoshis)
	}
}

func TestAPIError(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: return UTXOs
			items := []wocUnspent{
				{TxHash: "aaa0000000000000000000000000000000000000000000000000000000000000", TxPos: 0, Value: 50000},
			}
			body, _ := json.Marshal(items)
			w.Write(body)
		} else {
			// Second call: return 500
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("internal server error"))
		}
	}))
	defer srv.Close()

	tw := newTestWatcher(t, srv.URL)

	// First poll should succeed
	if err := tw.poll(); err != nil {
		t.Fatalf("first poll failed: %v", err)
	}
	if len(tw.GetUTXOs()) != 1 {
		t.Fatal("expected 1 UTXO after first poll")
	}

	// Second poll should fail but preserve previous UTXOs
	if err := tw.poll(); err == nil {
		t.Fatal("expected error on second poll")
	}

	// UTXOs should still be from first poll
	utxos := tw.GetUTXOs()
	if len(utxos) != 1 {
		t.Fatalf("expected 1 UTXO preserved after error, got %d", len(utxos))
	}
	if utxos[0].Satoshis != 50000 {
		t.Errorf("expected preserved UTXO to have 50000 sats, got %d", utxos[0].Satoshis)
	}

	// LastPoll error should be set
	_, lastErr := tw.LastPoll()
	if lastErr == nil {
		t.Error("expected lastErr to be set after failed poll")
	}
}
