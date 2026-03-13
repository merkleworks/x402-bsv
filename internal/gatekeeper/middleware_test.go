// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package gatekeeper

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/merkleworks/x402-bsv/internal/challenge"
	"github.com/merkleworks/x402-bsv/internal/pricing"
	"github.com/merkleworks/x402-bsv/internal/replay"
)

const (
	nonceTxIDHex = "a1a2a3a4a5a6a7a8a9a0b1b2b3b4b5b6b7b8b9b0c1c2c3c4c5c6c7c8c9c0d1d2"
	otherTxIDHex = "1111111111111111111111111111111111111111111111111111111111111111"

	// testPayeeScriptHex is a plausible P2PKH locking script for tests.
	testPayeeScriptHex = "76a91489abcdefab89abcdefab89abcdefab89abcdefab88ac"
	testAmountSats     = int64(100)
)

func newNonceRef(txidHex string, vout uint32) *challenge.NonceRef {
	return &challenge.NonceRef{
		TxID: txidHex,
		Vout: vout,
	}
}

func txWithInputs(txids []string, vouts []uint32) *transaction.Transaction {
	tx := transaction.NewTransaction()
	for i, h := range txids {
		id, _ := chainhash.NewHashFromHex(h)
		tx.AddInput(&transaction.TransactionInput{
			SourceTXID:       id,
			SourceTxOutIndex: vouts[i],
			SequenceNumber:   0xFFFFFFFF,
		})
	}
	return tx
}

func TestVerifyNonceAtInput0_ProfileB(t *testing.T) {
	tests := []struct {
		name    string
		txids   []string
		vouts   []uint32
		nonce   *challenge.NonceRef
		wantErr bool
	}{
		{
			name:    "nonce at input[0] — pass",
			txids:   []string{nonceTxIDHex, otherTxIDHex},
			vouts:   []uint32{0, 0},
			nonce:   newNonceRef(nonceTxIDHex, 0),
			wantErr: false,
		},
		{
			name:    "nonce at input[1] not input[0] — reject",
			txids:   []string{otherTxIDHex, nonceTxIDHex},
			vouts:   []uint32{0, 0},
			nonce:   newNonceRef(nonceTxIDHex, 0),
			wantErr: true,
		},
		{
			name:    "correct txid but wrong vout at input[0] — reject",
			txids:   []string{nonceTxIDHex},
			vouts:   []uint32{1},
			nonce:   newNonceRef(nonceTxIDHex, 0),
			wantErr: true,
		},
		{
			name:    "single input matching nonce — pass",
			txids:   []string{nonceTxIDHex},
			vouts:   []uint32{3},
			nonce:   newNonceRef(nonceTxIDHex, 3),
			wantErr: false,
		},
		{
			name:    "no inputs — reject",
			txids:   []string{},
			vouts:   []uint32{},
			nonce:   newNonceRef(nonceTxIDHex, 0),
			wantErr: true,
		},
		{
			name:    "nil nonce ref — reject",
			txids:   []string{nonceTxIDHex},
			vouts:   []uint32{0},
			nonce:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := txWithInputs(tt.txids, tt.vouts)
			err := verifyNonceAtInput0(tx, tt.nonce)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test helpers
// ---------------------------------------------------------------------------

// mockMempoolChecker always reports the tx as visible (no double-spend).
type mockMempoolChecker struct{}

func (m *mockMempoolChecker) CheckMempool(string) (visible, doubleSpend bool, err error) {
	return true, false, nil
}

// buildTestTx creates a minimal transaction spending the given nonce outpoint
// and paying amountSats to the given payee locking script hex.
func buildTestTx(nonceTxID string, nonceVout uint32, payeeScriptHex string, amountSats uint64) *transaction.Transaction {
	tx := transaction.NewTransaction()

	id, _ := chainhash.NewHashFromHex(nonceTxID)
	tx.AddInput(&transaction.TransactionInput{
		SourceTXID:       id,
		SourceTxOutIndex: nonceVout,
		SequenceNumber:   0xFFFFFFFF,
	})

	scriptBytes, _ := hex.DecodeString(payeeScriptHex)
	lockScript := script.Script(scriptBytes)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      amountSats,
		LockingScript: &lockScript,
	})
	return tx
}

// buildChallengeForTest creates a challenge matching the given HTTP request,
// stores it in the cache, and returns the challenge + its SHA-256 hash.
func buildChallengeForTest(t *testing.T, cache *ChallengeCache, r *http.Request, nonce *challenge.NonceRef, tmpl *challenge.TemplateRef) (*challenge.Challenge, string) {
	t.Helper()

	ch, err := challenge.Build(r, challenge.BuildOptions{
		PayeeLockingScriptHex: testPayeeScriptHex,
		Amount:                testAmountSats,
		Network:               "testnet",
		TTL:                   5 * time.Minute,
		BindHeaders:           HeaderAllowlist,
		NonceUTXO:             nonce,
		Template:              tmpl,
	})
	if err != nil {
		t.Fatalf("challenge.Build: %v", err)
	}

	hash, err := challenge.ComputeHash(ch)
	if err != nil {
		t.Fatalf("challenge.ComputeHash: %v", err)
	}
	cache.Store(hash, ch)
	return ch, hash
}

// buildProofHeader constructs an X402-Proof header value for the given
// transaction and challenge hash, binding it to the supplied request.
func buildProofHeader(t *testing.T, tx *transaction.Transaction, challengeHash string, r *http.Request) string {
	t.Helper()

	rawTxBytes := tx.Bytes()
	rawTxB64 := base64.StdEncoding.EncodeToString(rawTxBytes)
	computedTxID := tx.TxID().String()

	bodyHash := challenge.HashBody(nil) // no body for GET
	headersHash := challenge.HashHeaders(r.Header, HeaderAllowlist)

	proof := &Proof{
		V:               challenge.Version,
		Scheme:          challenge.Scheme,
		TxID:            computedTxID,
		RawTxB64:        rawTxB64,
		ChallengeSHA256: challengeHash,
		Request: RequestBinding{
			Domain:           r.Host,
			Method:           r.Method,
			Path:             r.URL.Path,
			Query:            r.URL.RawQuery,
			ReqHeadersSHA256: headersHash,
			ReqBodySHA256:    bodyHash,
		},
	}

	encoded, err := EncodeProof(proof)
	if err != nil {
		t.Fatalf("EncodeProof: %v", err)
	}
	return encoded
}

// testMiddlewareConfig returns a Config wired with real ChallengeCache,
// real ReplayCache, mock MempoolChecker, and fixed pricing. No NoncePool
// (challenges are pre-built in tests).
func testMiddlewareConfig() Config {
	return Config{
		MempoolChecker:        &mockMempoolChecker{},
		ReplayCache:           replay.New(10*time.Minute, 1000),
		ChallengeCache:        NewChallengeCache(10*time.Minute, 1000),
		PayeeLockingScriptHex: testPayeeScriptHex,
		Network:               "testnet",
		PricingFunc:           pricing.Fixed(testAmountSats),
		ChallengeTTL:          5 * time.Minute,
	}
}

// protectedHandler is a simple 200 OK handler behind the gatekeeper middleware.
func protectedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "unlocked"})
	})
}

// ---------------------------------------------------------------------------
// Integration test: replay rejection
// ---------------------------------------------------------------------------

func TestMiddleware_ReplayRejection(t *testing.T) {
	cfg := testMiddlewareConfig()
	handler := Middleware(cfg)(protectedHandler())

	// --- Step 1: Build a challenge bound to a specific nonce UTXO ---
	nonce := &challenge.NonceRef{
		TxID:             nonceTxIDHex,
		Vout:             0,
		Satoshis:         1,
		LockingScriptHex: "76a914aabbccdd88ac",
	}

	// Build the request that both challenge and proof will be bound to.
	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	_, challengeHash := buildChallengeForTest(t, cfg.ChallengeCache, req, nonce, nil)

	// --- Step 2: Construct a valid tx spending the nonce and paying the payee ---
	tx1 := buildTestTx(nonceTxIDHex, 0, testPayeeScriptHex, uint64(testAmountSats))
	proofHeader1 := buildProofHeader(t, tx1, challengeHash, req)

	// --- Step 3: Submit the valid proof → expect 200 ---
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req1.Header.Set(ProofHeader, proofHeader1)
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("first proof: want 200, got %d: %s", rec1.Code, rec1.Body.String())
	}
	t.Logf("first proof → %d (unlocked)", rec1.Code)

	// --- Step 4: Retry with same proof — challenge is consumed, expect 400 ---
	// After Step 14 in handleProof, the challenge was deleted from the cache.
	// The replay cache has the nonce → tx1 mapping, but since the challenge
	// is gone, the lookup at Step 6 returns nil. The replay check at Step 7
	// is guarded by originalChallenge != nil, so it's skipped. Step 8 rejects
	// with "challenge not found" (400).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req2.Header.Set(ProofHeader, proofHeader1)
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("replay (same proof, consumed challenge): want 400, got %d: %s", rec2.Code, rec2.Body.String())
	}
	t.Logf("replay (consumed challenge) → %d", rec2.Code)

	var body2 map[string]any
	json.Unmarshal(rec2.Body.Bytes(), &body2)
	if code, _ := body2["code"].(string); code != string(ErrChallengeNotFound) {
		t.Errorf("replay code: got %q, want %q", code, ErrChallengeNotFound)
	}

	// --- Step 5: Test double-spend (different tx, same nonce, challenge re-issued) ---
	// Re-issue a fresh challenge for the SAME nonce UTXO.
	req3base := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	_, challengeHash2 := buildChallengeForTest(t, cfg.ChallengeCache, req3base, nonce, nil)

	// Build a DIFFERENT tx that also spends the same nonce UTXO.
	tx2 := buildTestTx(nonceTxIDHex, 0, testPayeeScriptHex, uint64(testAmountSats))
	// Add a second dummy input to make tx2 have a different txid from tx1.
	dummyID, _ := chainhash.NewHashFromHex(otherTxIDHex)
	tx2.AddInput(&transaction.TransactionInput{
		SourceTXID:       dummyID,
		SourceTxOutIndex: 99,
		SequenceNumber:   0xFFFFFFFF,
	})
	proofHeader2 := buildProofHeader(t, tx2, challengeHash2, req3base)

	// The replay cache already has nonce → tx1.TxID. Submitting tx2 (different
	// txid) for the same nonce should trigger the double-spend branch at Step 7.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req3.Header.Set(ProofHeader, proofHeader2)
	handler.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusConflict {
		t.Fatalf("double-spend: want 409, got %d: %s", rec3.Code, rec3.Body.String())
	}
	t.Logf("double-spend (different tx, same nonce) → %d", rec3.Code)

	var body3 map[string]any
	json.Unmarshal(rec3.Body.Bytes(), &body3)
	if code, _ := body3["code"].(string); code != string(ErrDoubleSpend) {
		t.Errorf("double-spend code: got %q, want %q", code, ErrDoubleSpend)
	}
}

// ---------------------------------------------------------------------------
// Integration test: tampered template (output[0] value reduced)
// ---------------------------------------------------------------------------

func TestMiddleware_TamperedOutputValue(t *testing.T) {
	cfg := testMiddlewareConfig()
	handler := Middleware(cfg)(protectedHandler())

	// --- Build a Profile B challenge with amount = 100 sats ---
	nonce := &challenge.NonceRef{
		TxID:             nonceTxIDHex,
		Vout:             0,
		Satoshis:         1,
		LockingScriptHex: "76a914aabbccdd88ac",
	}
	tmpl := &challenge.TemplateRef{
		RawTxHex:  "deadbeef", // placeholder — only used to signal Profile B
		PriceSats: uint64(testAmountSats),
	}

	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	_, challengeHash := buildChallengeForTest(t, cfg.ChallengeCache, req, nonce, tmpl)

	// --- Construct a tampered tx paying only 50 sats (< 100 required) ---
	tamperedAmount := uint64(50)
	txTampered := buildTestTx(nonceTxIDHex, 0, testPayeeScriptHex, tamperedAmount)
	proofHeader := buildProofHeader(t, txTampered, challengeHash, req)

	rec := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req1.Header.Set(ProofHeader, proofHeader)
	handler.ServeHTTP(rec, req1)

	// verifyPayeeOutput at Step 13 checks output[0].Satoshis >= AmountSats.
	// 50 < 100 → ErrInvalidPayee → HTTP 403.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered output value: want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	t.Logf("tampered output value (50 < 100) → %d", rec.Code)

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if code, _ := body["code"].(string); code != string(ErrInvalidPayee) {
		t.Errorf("tampered output code: got %q, want %q", code, ErrInvalidPayee)
	}

	// --- Also verify that the correct amount passes ---
	// Re-issue challenge (the previous one is still in cache since verification
	// failed before Step 14 deletes it, but let's be explicit).
	_, challengeHash2 := buildChallengeForTest(t, cfg.ChallengeCache, req, nonce, tmpl)
	txValid := buildTestTx(nonceTxIDHex, 0, testPayeeScriptHex, uint64(testAmountSats))
	proofHeader2 := buildProofHeader(t, txValid, challengeHash2, req)

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req2.Header.Set(ProofHeader, proofHeader2)
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("valid output value: want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	t.Logf("valid output value (100 >= 100) → %d (unlocked)", rec2.Code)
}

// ---------------------------------------------------------------------------
// Integration test: tampered payee script
// ---------------------------------------------------------------------------

func TestMiddleware_TamperedPayeeScript(t *testing.T) {
	cfg := testMiddlewareConfig()
	handler := Middleware(cfg)(protectedHandler())

	nonce := &challenge.NonceRef{
		TxID:             nonceTxIDHex,
		Vout:             0,
		Satoshis:         1,
		LockingScriptHex: "76a914aabbccdd88ac",
	}

	req := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	_, challengeHash := buildChallengeForTest(t, cfg.ChallengeCache, req, nonce, nil)

	// Pay the correct amount but to an ATTACKER's script (different from payee)
	attackerScriptHex := "76a914" + fmt.Sprintf("%040x", 0xDEAD) + "88ac"
	txBad := buildTestTx(nonceTxIDHex, 0, attackerScriptHex, uint64(testAmountSats))
	proofHeader := buildProofHeader(t, txBad, challengeHash, req)

	rec := httptest.NewRecorder()
	req1 := httptest.NewRequest("GET", "http://localhost:8402/v1/expensive", nil)
	req1.Header.Set(ProofHeader, proofHeader)
	handler.ServeHTTP(rec, req1)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("tampered payee script: want 403, got %d: %s", rec.Code, rec.Body.String())
	}

	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if code, _ := body["code"].(string); code != string(ErrInvalidPayee) {
		t.Errorf("tampered payee code: got %q, want %q", code, ErrInvalidPayee)
	}
	t.Logf("tampered payee script → %d (correctly rejected)", rec.Code)
}
