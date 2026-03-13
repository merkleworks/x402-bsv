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
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"

	"github.com/merkleworks/x402-bsv/internal/challenge"
	"github.com/merkleworks/x402-bsv/internal/pool"
)

const (
	// ChallengeHeader is the HTTP header carrying the 402 challenge (per spec).
	ChallengeHeader = "X402-Challenge"

	// AcceptHeader signals supported payment schemes (per spec).
	AcceptHeader = "X402-Accept"

	// ProofHeader is the HTTP header carrying the client's payment proof (per spec).
	ProofHeader = "X402-Proof"

	// ReceiptHeader is the HTTP header carrying the payment receipt (per spec).
	ReceiptHeader = "X402-Receipt"

	// StatusHeader carries the mempool acceptance status (per spec).
	StatusHeader = "X402-Status"

	// AmountHeader carries the payment amount in satoshis (internal, for stats).
	AmountHeader = "X402-Amount-Sats"
)

// Middleware returns an http.Handler middleware that gates access behind x402 payment.
//
// Flow:
//  1. If no X402-Proof header → build challenge, return 402
//  2. If X402-Proof present → parse proof, verify tx structure, verify binding, check mempool, gate response
func Middleware(cfg Config) func(http.Handler) http.Handler {
	logger := slog.Default().With("component", "gatekeeper")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			proofHeader := r.Header.Get(ProofHeader)

			if proofHeader == "" {
				// No proof — issue a 402 challenge
				handleChallenge(w, r, cfg, logger)
				return
			}

			// Proof present — verify and gate
			handleProof(w, r, next, proofHeader, cfg, logger)
		})
	}
}

// handleChallenge issues a 402 Payment Required response with a challenge.
func handleChallenge(w http.ResponseWriter, r *http.Request, cfg Config, logger *slog.Logger) {
	// Determine price
	amount, err := cfg.PricingFunc(r)
	if err != nil {
		logger.Error("pricing function failed", "error", err)
		writeError(w, http.StatusInternalServerError, string(ErrInternalError), "failed to determine price")
		return
	}

	// Lease a nonce UTXO for replay protection
	var nonceRef *challenge.NonceRef
	var templateRef *challenge.TemplateRef
	if cfg.NoncePool != nil {
		nonceUTXO, err := cfg.NoncePool.Lease()
		if err != nil {
			logger.Error("no nonce UTXOs available", "error", err)
			writeError(w, HTTPStatusForError(ErrNoUTXOsAvailable), string(ErrNoUTXOsAvailable),
				"no nonce UTXOs available for challenge")
			return
		}
		nonceRef = &challenge.NonceRef{
			TxID:             nonceUTXO.TxID,
			Vout:             nonceUTXO.Vout,
			Satoshis:         nonceUTXO.Satoshis,
			LockingScriptHex: nonceUTXO.Script,
		}

		// Profile B: include pre-signed template if available
		if nonceUTXO.RawTxTemplate != "" {
			templateRef = &challenge.TemplateRef{
				RawTxHex:  nonceUTXO.RawTxTemplate,
				PriceSats: nonceUTXO.TemplatePriceSats,
			}
		}
	}

	// Build the challenge
	bindHeaders := cfg.BindHeaders
	if len(bindHeaders) == 0 {
		bindHeaders = HeaderAllowlist
	}

	opts := challenge.BuildOptions{
		PayeeLockingScriptHex: cfg.PayeeLockingScriptHex,
		Amount:                amount,
		Network:               cfg.Network,
		TTL:                   cfg.ChallengeTTL,
		BindHeaders:           bindHeaders,
		NonceUTXO:             nonceRef,
		Template:              templateRef,
	}

	ch, err := challenge.Build(r, opts)
	if err != nil {
		logger.Error("challenge build failed", "error", err)
		writeError(w, http.StatusInternalServerError, string(ErrInternalError), "failed to build challenge")
		return
	}

	// Compute challenge hash for storage key
	challengeHash, err := challenge.ComputeHash(ch)
	if err != nil {
		logger.Error("challenge hash failed", "error", err)
		writeError(w, http.StatusInternalServerError, string(ErrInternalError), "failed to compute challenge hash")
		return
	}

	// Store challenge in cache for binding verification on proof submission
	if cfg.ChallengeCache != nil {
		cfg.ChallengeCache.Store(challengeHash, ch)
	}

	// Encode challenge for the header
	encoded, err := challenge.Encode(ch)
	if err != nil {
		logger.Error("challenge encode failed", "error", err)
		writeError(w, http.StatusInternalServerError, string(ErrInternalError), "failed to encode challenge")
		return
	}

	logger.Info("issuing 402 challenge",
		"path", r.URL.Path,
		"amount", amount,
		"challenge_hash", challengeHash,
		"nonce", nonceRefString(nonceRef),
	)

	// Return 402 with challenge per spec
	w.Header().Set(ChallengeHeader, encoded)
	w.Header().Set(AcceptHeader, challenge.Scheme)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  402,
		"code":    "payment_required",
		"message": "Payment required. See X402-Challenge header.",
	})
}

// handleProof verifies a payment proof and gates the response.
func handleProof(w http.ResponseWriter, r *http.Request, next http.Handler, proofHeader string, cfg Config, logger *slog.Logger) {
	// Step 1: Parse the proof
	proof, err := ParseProof(proofHeader)
	if err != nil {
		logger.Warn("invalid proof header", "error", err)
		writeError(w, HTTPStatusForError(ErrInvalidProof), string(ErrInvalidProof), err.Error())
		return
	}

	// Step 2: Validate version and scheme
	if proof.V != challenge.Version {
		writeError(w, HTTPStatusForError(ErrInvalidVersion), string(ErrInvalidVersion),
			fmt.Sprintf("got version %q, want %q", proof.V, challenge.Version))
		return
	}
	if proof.Scheme != challenge.Scheme {
		writeError(w, HTTPStatusForError(ErrInvalidScheme), string(ErrInvalidScheme),
			fmt.Sprintf("got scheme %q, want %q", proof.Scheme, challenge.Scheme))
		return
	}

	// Step 3: Decode the raw transaction from base64
	rawTxBytes, err := base64.StdEncoding.DecodeString(proof.RawTxB64)
	if err != nil {
		writeError(w, HTTPStatusForError(ErrInvalidProof), string(ErrInvalidProof), "invalid rawtx_b64")
		return
	}

	tx, err := transaction.NewTransactionFromBytes(rawTxBytes)
	if err != nil {
		writeError(w, HTTPStatusForError(ErrInvalidProof), string(ErrInvalidProof), "cannot parse transaction")
		return
	}

	// Step 4: Compute txid and compare to proof.txid (constant-time)
	computedTxID := tx.TxID().String()
	if proof.TxID != "" && !constantTimeEqual(proof.TxID, computedTxID) {
		writeError(w, HTTPStatusForError(ErrInvalidProof), string(ErrInvalidProof),
			fmt.Sprintf("txid mismatch: proof=%s, computed=%s", proof.TxID, computedTxID))
		return
	}

	// Step 5: Verify transaction has inputs
	if tx.InputCount() < 1 {
		writeError(w, HTTPStatusForError(ErrInvalidProof), string(ErrInvalidProof), "transaction has no inputs")
		return
	}

	// Step 6: Look up original challenge (needed for nonce outpoint + binding verification)
	var originalChallenge *challenge.Challenge
	if cfg.ChallengeCache != nil {
		originalChallenge = cfg.ChallengeCache.Lookup(proof.ChallengeSHA256)
	}

	// Step 7: Replay check using nonce outpoint from the challenge (constant-time)
	if cfg.ReplayCache != nil && originalChallenge != nil && originalChallenge.NonceUTXO != nil {
		nonce := originalChallenge.NonceUTXO
		if existingTxID, _, found := cfg.ReplayCache.Check(nonce.TxID, nonce.Vout); found {
			if constantTimeEqual(existingTxID, computedTxID) {
				// Same tx — idempotent re-serve, but must still enforce mempool
				// semantics when require_mempool_accept is true.
				logger.Info("idempotent re-serve",
					"txid", computedTxID,
					"nonce", fmt.Sprintf("%s:%d", nonce.TxID, nonce.Vout),
				)
				receiptHash := computeReceiptHash(computedTxID, proof.ChallengeSHA256)
				w.Header().Set(ReceiptHeader, receiptHash)
				w.Header().Set("X402-Receipt-Time", time.Now().UTC().Format(time.RFC3339))

				if originalChallenge.RequireMempoolAccept {
					if cfg.MempoolChecker == nil {
						logger.Error("require_mempool_accept but no MempoolChecker configured", "txid", computedTxID)
						w.Header().Set(StatusHeader, "error")
						writeError(w, HTTPStatusForError(ErrMempoolError), string(ErrMempoolError),
							"mempool verification required but not configured")
						return
					}
					visible, doubleSpend, mErr := cfg.MempoolChecker.CheckMempool(computedTxID)
					if mErr != nil {
						logger.Error("mempool check failed on re-serve", "txid", computedTxID, "error", mErr)
						w.Header().Set(StatusHeader, "error")
						writeError(w, HTTPStatusForError(ErrMempoolError), string(ErrMempoolError),
							fmt.Sprintf("mempool verification failed: %s", mErr))
						return
					}
					if doubleSpend {
						logger.Warn("mempool double-spend on re-serve", "txid", computedTxID)
						w.Header().Set(StatusHeader, "rejected")
						writeError(w, HTTPStatusForError(ErrDoubleSpend), string(ErrDoubleSpend),
							"transaction rejected by mempool as double-spend")
						return
					}
					if !visible {
						logger.Info("re-serve: payment pending (not yet in mempool)", "txid", computedTxID)
						w.Header().Set(StatusHeader, "pending")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusAccepted)
						json.NewEncoder(w).Encode(map[string]any{
							"status":  202,
							"code":    string(ErrMempoolPending),
							"message": "transaction not yet visible in mempool",
							"txid":    computedTxID,
						})
						return
					}
				}

				// Idempotent re-serve succeeded — delete challenge from cache (single-use)
				if cfg.ChallengeCache != nil {
					cfg.ChallengeCache.Delete(proof.ChallengeSHA256)
				}
				// Mark nonce as spent so it's never re-leased for a new challenge.
				// Without this, the lease TTL expiry would return the nonce to "available"
				// even though it's been spent on-chain → subsequent broadcasts fail.
				if cfg.NoncePool != nil {
					cfg.NoncePool.MarkSpent(nonce.TxID, nonce.Vout)
				}
				w.Header().Set(StatusHeader, "accepted")
				w.Header().Set(AmountHeader, fmt.Sprintf("%d", originalChallenge.AmountSats))
				next.ServeHTTP(w, r)
				return
			}
			// Different txid — nonce already consumed by another tx (double-spend attempt)
			logger.Warn("replay detected at gatekeeper",
				"nonce", fmt.Sprintf("%s:%d", nonce.TxID, nonce.Vout),
				"existing_txid", existingTxID,
				"proof_txid", computedTxID,
			)
			w.Header().Set(StatusHeader, "rejected")
			writeError(w, HTTPStatusForError(ErrDoubleSpend), string(ErrDoubleSpend),
				fmt.Sprintf("nonce already spent in tx %s", existingTxID))
			return
		}
	}

	// Step 8: If no original challenge found (expired or unknown), reject
	if originalChallenge == nil {
		writeError(w, HTTPStatusForError(ErrChallengeNotFound), string(ErrChallengeNotFound),
			"challenge not found or expired")
		return
	}

	// Step 9: Validate scheme and version on the challenge
	if err := challenge.ValidateSchemeVersion(originalChallenge); err != nil {
		logger.Warn("challenge scheme/version mismatch", "error", err)
		if originalChallenge.Scheme != challenge.Scheme {
			writeError(w, HTTPStatusForError(ErrInvalidScheme), string(ErrInvalidScheme), err.Error())
		} else {
			writeError(w, HTTPStatusForError(ErrInvalidVersion), string(ErrInvalidVersion), err.Error())
		}
		return
	}

	// Step 10: Check challenge expiry
	if originalChallenge.ExpiresAt <= time.Now().Unix() {
		writeError(w, HTTPStatusForError(ErrExpiredChallenge), string(ErrExpiredChallenge),
			"challenge has expired")
		return
	}

	// Step 11: Verify nonce spend — the tx MUST consume the nonce outpoint
	if originalChallenge.NonceUTXO != nil {
		if err := verifyNonceSpend(tx, originalChallenge.NonceUTXO); err != nil {
			logger.Warn("nonce spend verification failed", "error", err)
			writeError(w, HTTPStatusForError(ErrNonceMissing), string(ErrNonceMissing), err.Error())
			return
		}

		// Step 11b: For Profile B (template mode), verify nonce is at input[0].
		// SIGHASH_SINGLE binds the signature to output[input_index], so the nonce
		// input at index 0 commits to output[0] (the payee output). If the nonce
		// were at a different index, the gateway's 0xC3 signature would lock the
		// wrong output — which is consensus-invalid, but we catch it here early
		// with a clear error for defence-in-depth.
		if originalChallenge.Template != nil {
			if err := verifyNonceAtInput0(tx, originalChallenge.NonceUTXO); err != nil {
				logger.Warn("nonce position verification failed (template mode)", "error", err)
				writeError(w, HTTPStatusForError(ErrNonceMissing), string(ErrNonceMissing), err.Error())
				return
			}
		}
	}

	// Step 12: Verify request binding
	bindHeaders := cfg.BindHeaders
	if len(bindHeaders) == 0 {
		bindHeaders = HeaderAllowlist
	}
	if err := challenge.VerifyBinding(originalChallenge, r, bindHeaders); err != nil {
		logger.Warn("binding mismatch", "error", err)
		writeError(w, HTTPStatusForError(ErrInvalidBinding), string(ErrInvalidBinding), err.Error())
		return
	}

	// Step 13: Confirm tx has output paying >= amount_sats to payee
	if err := verifyPayeeOutput(tx, originalChallenge.PayeeLockingScriptHex, originalChallenge.AmountSats); err != nil {
		logger.Warn("payee output verification failed", "error", err)
		writeError(w, HTTPStatusForError(ErrInvalidPayee), string(ErrInvalidPayee), err.Error())
		return
	}

	// Step 14: Challenge cache deletion is deferred until the final 200 response.
	// If we deleted here unconditionally, a 202 (payment pending) response would
	// prevent the client from polling — on retry the challenge would be gone,
	// causing "challenge_not_found". Instead, the challenge stays in cache until
	// mempool acceptance is confirmed (200), or until it expires naturally (TTL).
	// The replay cache (Step 15) provides replay protection in the interim.

	// Step 15: Record in replay cache (nonce outpoint → txid + challenge hash)
	if cfg.ReplayCache != nil && originalChallenge.NonceUTXO != nil {
		nonce := originalChallenge.NonceUTXO
		cfg.ReplayCache.Record(nonce.TxID, nonce.Vout, computedTxID, proof.ChallengeSHA256)
	}

	// Step 15b: Mark nonce UTXO as spent immediately after replay-cache recording.
	// This must happen on BOTH the 200 and 202 paths. Once a valid proof has been
	// recorded, the nonce is consumed regardless of mempool visibility — the
	// transaction has been built and broadcast, so this nonce's on-chain outpoint
	// is spent. Without this, the 202 path leaves the nonce as merely "leased",
	// and the lease reclaim loop eventually returns it to "available" even though
	// it's spent on-chain, causing txn-mempool-conflict on the next flow.
	if cfg.NoncePool != nil && originalChallenge.NonceUTXO != nil {
		cfg.NoncePool.MarkSpent(originalChallenge.NonceUTXO.TxID, originalChallenge.NonceUTXO.Vout)
	}

	// Step 16: Mempool acceptance matrix (CRIT-04)
	// Per Protocol-Spec:
	//   200 = mempool-visible → serve protected response
	//   202 = pending (not visible yet) → do NOT serve
	//   409 = explicit double-spend → do NOT serve
	//   503 = checker error → do NOT serve
	receiptHash := computeReceiptHash(computedTxID, proof.ChallengeSHA256)
	w.Header().Set(ReceiptHeader, receiptHash)
	w.Header().Set("X402-Receipt-Time", time.Now().UTC().Format(time.RFC3339))

	if originalChallenge.RequireMempoolAccept && cfg.MempoolChecker == nil {
		// Hard failure: challenge requires mempool verification but no checker configured
		logger.Error("require_mempool_accept but no MempoolChecker configured", "txid", computedTxID)
		w.Header().Set(StatusHeader, "error")
		writeError(w, HTTPStatusForError(ErrMempoolError), string(ErrMempoolError),
			"mempool verification required but not configured")
		return
	}

	if cfg.MempoolChecker != nil && originalChallenge.RequireMempoolAccept {
		visible, doubleSpend, err := cfg.MempoolChecker.CheckMempool(computedTxID)
		if err != nil {
			// 503 — mempool check failed
			logger.Error("mempool check failed", "txid", computedTxID, "error", err)
			w.Header().Set(StatusHeader, "error")
			writeError(w, HTTPStatusForError(ErrMempoolError), string(ErrMempoolError),
				fmt.Sprintf("mempool verification failed: %s", err))
			return
		}

		if doubleSpend {
			// 409 — explicit double-spend detected
			logger.Warn("mempool double-spend detected", "txid", computedTxID)
			w.Header().Set(StatusHeader, "rejected")
			writeError(w, HTTPStatusForError(ErrDoubleSpend), string(ErrDoubleSpend),
				"transaction rejected by mempool as double-spend")
			return
		}

		if !visible {
			// 202 — tx not yet visible, payment acknowledged but not confirmed
			logger.Info("payment pending (not yet in mempool)", "txid", computedTxID)
			w.Header().Set(StatusHeader, "pending")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]any{
				"status":  202,
				"code":    string(ErrMempoolPending),
				"message": "transaction not yet visible in mempool",
				"txid":    computedTxID,
			})
			return
		}

		// 200 — tx visible in mempool, serve protected response
		w.Header().Set(StatusHeader, "accepted")
	}

	// Step 14b: Delete challenge from cache now that payment is fully accepted (single-use).
	// This runs only on the 200 path (mempool-visible or no mempool check required).
	if cfg.ChallengeCache != nil {
		cfg.ChallengeCache.Delete(proof.ChallengeSHA256)
	}

	// Step 14c: Mark nonce UTXO as spent in the pool so it's never re-leased.
	// Critical: without this, the nonce lease expires → returns to "available" →
	// next challenge picks the same nonce → broadcast fails ("Missing inputs")
	// because the nonce UTXO was already spent on-chain.
	if cfg.NoncePool != nil && originalChallenge.NonceUTXO != nil {
		cfg.NoncePool.MarkSpent(originalChallenge.NonceUTXO.TxID, originalChallenge.NonceUTXO.Vout)
	}

	// Set payment amount header for stats collector (read by loggingMiddleware)
	w.Header().Set(AmountHeader, fmt.Sprintf("%d", originalChallenge.AmountSats))

	// Find the payee output for settlement tracking.
	// In Profile B, the payee output is always at vout 0 (SIGHASH_SINGLE at input[0]
	// commits to output[0]). We scan all outputs to handle both profiles correctly.
	var payeeVout uint32
	var payeeSats uint64
	var payeeScript string
	for idx, out := range tx.Outputs {
		scriptHex := hex.EncodeToString(*out.LockingScript)
		if constantTimeEqual(scriptHex, cfg.PayeeLockingScriptHex) && int64(out.Satoshis) >= originalChallenge.AmountSats {
			payeeVout = uint32(idx)
			payeeSats = out.Satoshis
			payeeScript = scriptHex
			break
		}
	}

	// Record settlement revenue + UTXO details (persistent — Redis-backed).
	// The UTXO info allows sweep-to-treasury without an indexer query.
	if cfg.SettlementRecorder != nil {
		cfg.SettlementRecorder.RecordSettlement(originalChallenge.AmountSats, computedTxID, payeeVout, payeeSats, payeeScript)
	}

	// Track the settlement output in the payment pool so it can be swept to treasury.
	if cfg.PaymentPool != nil && payeeScript != "" {
		cfg.PaymentPool.AddExisting([]pool.UTXO{{
			TxID:     computedTxID,
			Vout:     payeeVout,
			Script:   payeeScript,
			Satoshis: payeeSats,
		}})
		logger.Debug("tracked settlement output in payment pool",
			"txid", computedTxID,
			"vout", payeeVout,
			"satoshis", payeeSats,
		)
	}

	logger.Info("payment accepted",
		"txid", computedTxID,
		"path", r.URL.Path,
		"amount_sats", originalChallenge.AmountSats,
		"nonce", nonceRefString(originalChallenge.NonceUTXO),
		"receipt", receiptHash,
	)

	next.ServeHTTP(w, r)
}

// verifyNonceSpend checks that the transaction spends the nonce UTXO
// specified in the challenge. This is the core replay protection mechanism:
// Bitcoin consensus guarantees that an outpoint can only be spent once.
// Uses constant-time comparison for txid.
func verifyNonceSpend(tx *transaction.Transaction, nonce *challenge.NonceRef) error {
	if nonce == nil {
		return fmt.Errorf("challenge has no nonce_utxo")
	}
	for _, input := range tx.Inputs {
		if input.SourceTXID != nil &&
			constantTimeEqual(input.SourceTXID.String(), nonce.TxID) &&
			input.SourceTxOutIndex == nonce.Vout {
			return nil // found the nonce input
		}
	}
	return fmt.Errorf("transaction does not spend nonce outpoint %s:%d", nonce.TxID, nonce.Vout)
}

// verifyNonceAtInput0 checks that the nonce outpoint is consumed at input
// index 0. Required for Profile B (template mode) because the gateway's
// SIGHASH_SINGLE signature on input 0 commits to output[0] (the payee output).
// Uses constant-time comparison for txid.
func verifyNonceAtInput0(tx *transaction.Transaction, nonce *challenge.NonceRef) error {
	if nonce == nil {
		return fmt.Errorf("challenge has no nonce_utxo")
	}
	if tx.InputCount() < 1 {
		return fmt.Errorf("transaction has no inputs")
	}
	input0 := tx.Inputs[0]
	if input0.SourceTXID == nil ||
		!constantTimeEqual(input0.SourceTXID.String(), nonce.TxID) ||
		input0.SourceTxOutIndex != nonce.Vout {
		return fmt.Errorf("template mode requires nonce at input[0], but input[0] is %s:%d (expected %s:%d)",
			input0.SourceTXID, input0.SourceTxOutIndex, nonce.TxID, nonce.Vout)
	}
	return nil
}

// verifyPayeeOutput checks that the transaction has at least one output
// paying >= minAmount to the expected payee locking script.
// Uses constant-time comparison for script hex.
func verifyPayeeOutput(tx *transaction.Transaction, expectedScriptHex string, minAmount int64) error {
	for _, out := range tx.Outputs {
		scriptHex := hex.EncodeToString(*out.LockingScript)
		if constantTimeEqual(scriptHex, expectedScriptHex) && int64(out.Satoshis) >= minAmount {
			return nil // found valid payee output
		}
	}
	return fmt.Errorf("no output paying >= %d sats to expected payee", minAmount)
}

// constantTimeEqual compares two strings in constant time to prevent timing attacks.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func nonceRefString(n *challenge.NonceRef) string {
	if n == nil {
		return "<none>"
	}
	return fmt.Sprintf("%s:%d", n.TxID, n.Vout)
}

func computeReceiptHash(txid, challengeHash string) string {
	h := sha256.Sum256([]byte(txid + ":" + challengeHash))
	return hex.EncodeToString(h[:])
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"status":  status,
		"code":    code,
		"message": message,
	})
}
