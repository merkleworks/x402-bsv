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
	"net/http"
	"time"

	"github.com/merkleworks/x402-bsv/internal/broadcast"
	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/pricing"
	"github.com/merkleworks/x402-bsv/internal/replay"
)

// Config configures the gatekeeper middleware.
type Config struct {
	// MempoolChecker provides independent mempool verification (CRIT-04).
	// If nil, mempool check is skipped (backwards compat).
	MempoolChecker broadcast.MempoolChecker

	// ReplayCache provides early replay detection at the gatekeeper layer.
	// Defence-in-depth: the delegator also checks replay independently.
	ReplayCache *replay.Cache

	// ChallengeCache stores issued challenges for binding verification.
	ChallengeCache *ChallengeCache

	// NoncePool is the pool of 1-sat UTXOs used for replay-protection nonces.
	// Each 402 challenge leases a nonce from this pool. The proof transaction
	// must spend the nonce outpoint, providing Bitcoin-enforced single-use.
	NoncePool pool.Pool

	// PayeeLockingScriptHex is the hex-encoded locking script for payments.
	PayeeLockingScriptHex string

	// Network is "mainnet" or "testnet" (server-side only, not on wire).
	Network string

	// PricingFunc determines the price for each request.
	PricingFunc pricing.Func

	// ChallengeTTL is how long a challenge remains valid.
	ChallengeTTL time.Duration

	// BindHeaders specifies which request headers to include in the challenge binding.
	// Defaults to HeaderAllowlist if empty.
	BindHeaders []string

	// SettlementRecorder is called on every successful 200 OK settlement.
	// If nil, settlement recording is skipped.
	SettlementRecorder SettlementRecorder

	// PaymentPool receives settlement revenue UTXOs. When a settlement succeeds
	// (200 OK), the payee output is added to this pool so it can later be swept
	// to the treasury address. If nil, settlement UTXO tracking is skipped.
	PaymentPool pool.Pool
}

// SettlementRecorder records successful payment settlements for revenue tracking.
// amountSats is the challenge price; txid/vout/satoshis/scriptHex describe the
// payee output UTXO so it can be swept to treasury without an indexer query.
type SettlementRecorder interface {
	RecordSettlement(amountSats int64, txid string, vout uint32, satoshis uint64, scriptHex string)
}

// HeaderAllowlist defines headers included in canonical request hash (per spec).
var HeaderAllowlist = []string{
	"accept",
	"content-length",
	"content-type",
	"x402-client",
	"x402-idempotency-key",
}

// Proof is the client's payment proof submitted in the X402-Proof header.
type Proof struct {
	V               string         `json:"v"`
	Scheme          string         `json:"scheme"`
	TxID            string         `json:"txid"`
	RawTxB64        string         `json:"rawtx_b64"`
	ChallengeSHA256 string         `json:"challenge_sha256"`
	Request         RequestBinding `json:"request"`
	ClientSig       *ClientSig     `json:"client_sig,omitempty"`
}

// RequestBinding contains the request parameters for binding validation.
type RequestBinding struct {
	Domain           string `json:"domain"`
	Method           string `json:"method"`
	Path             string `json:"path"`
	Query            string `json:"query"`
	ReqHeadersSHA256 string `json:"req_headers_sha256"`
	ReqBodySHA256    string `json:"req_body_sha256"`
}

// ClientSig contains optional client signature (required in v1.1).
type ClientSig struct {
	Alg          string `json:"alg"`
	PubkeyHex    string `json:"pubkey_hex"`
	SignatureHex string `json:"signature_hex"`
}

// ---------------------------------------------------------------------------
// Spec error codes
// ---------------------------------------------------------------------------

// ErrorCode is a spec-defined error identifier.
type ErrorCode string

const (
	ErrInvalidVersion     ErrorCode = "invalid_version"
	ErrInvalidScheme      ErrorCode = "invalid_scheme"
	ErrInvalidPayee       ErrorCode = "invalid_payee"
	ErrInsufficientAmount ErrorCode = "insufficient_amount"
	ErrExpiredChallenge   ErrorCode = "expired_challenge"
	ErrMempoolRejected    ErrorCode = "mempool_rejected"
	ErrMempoolPending     ErrorCode = "payment_pending"
	ErrMempoolError       ErrorCode = "mempool_check_error"
	ErrInvalidBinding     ErrorCode = "invalid_binding"
	ErrDoubleSpend        ErrorCode = "double_spend"
	ErrInvalidProof       ErrorCode = "invalid_proof"
	ErrChallengeNotFound  ErrorCode = "challenge_not_found"
	ErrNonceMissing       ErrorCode = "nonce_missing"
	ErrNoUTXOsAvailable   ErrorCode = "no_utxos_available"
	ErrInternalError      ErrorCode = "internal_error"
)

// HTTPStatusForError maps spec error codes to HTTP status codes.
func HTTPStatusForError(code ErrorCode) int {
	switch code {
	case ErrInvalidVersion, ErrInvalidScheme, ErrInvalidProof, ErrChallengeNotFound, ErrNonceMissing:
		return http.StatusBadRequest
	case ErrExpiredChallenge, ErrInsufficientAmount:
		return http.StatusPaymentRequired
	case ErrInvalidBinding, ErrInvalidPayee:
		return http.StatusForbidden
	case ErrDoubleSpend, ErrMempoolRejected:
		return http.StatusConflict
	case ErrMempoolPending:
		return http.StatusAccepted
	case ErrNoUTXOsAvailable, ErrMempoolError:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
