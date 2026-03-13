// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package delegator

// DelegationRequest is the input to the delegator's Accept method.
// Per canonical spec: the client constructs and signs the partial transaction;
// the delegator validates structure, adds fee inputs, and signs only those.
type DelegationRequest struct {
	// PartialTxHex is the hex-encoded partial transaction constructed by the client.
	// Profile A: must contain nonce input (signed with 0xC1), payment input(s) (signed with 0xC1),
	//            and payee output.
	// Profile B: must contain nonce input (signed with 0xC3 by gateway template), sponsor
	//            funding input(s) (signed with 0xC1), and payee output.
	// The delegator will append fee inputs and sign them.
	PartialTxHex string `json:"partial_tx_hex"`

	// ChallengeHash is the SHA-256 of the original challenge.
	ChallengeHash string `json:"challenge_hash"`

	// ExpectedPayeeLockingScriptHex is the expected payee locking script.
	ExpectedPayeeLockingScriptHex string `json:"payee_locking_script_hex,omitempty"`

	// ExpectedAmount is the minimum expected payment amount.
	ExpectedAmount int64 `json:"amount_sats,omitempty"`

	// NonceOutpoint identifies the nonce UTXO that must appear as an input
	// in the partial transaction. Used for replay cache lookup only.
	NonceOutpoint *NonceOutpointRef `json:"nonce_outpoint,omitempty"`

	// TemplateMode indicates this is a Profile B request where the nonce input
	// was pre-signed by the gateway with 0xC3 (SIGHASH_SINGLE|ANYONECANPAY|FORKID).
	// When true, input 0 accepts 0xC3 sighash; all other inputs require 0xC1.
	TemplateMode bool `json:"template_mode,omitempty"`
}

// NonceOutpointRef identifies a nonce UTXO by its outpoint (txid:vout).
// Satoshis is the nonce input's value so the delegator can account for it
// when calculating the fee pool deficit (the nonce's 1 sat covers the miner fee).
type NonceOutpointRef struct {
	TxID     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	Satoshis uint64 `json:"satoshis,omitempty"` // nonce input value (default 1 sat)
}

// DelegationResult is returned after successful delegation.
// The client uses txid + rawtx to build the proof for the gatekeeper.
// Per spec: the client is responsible for broadcasting.
type DelegationResult struct {
	TxID     string `json:"txid"`
	RawTxHex string `json:"rawtx_hex"`
	Accepted bool   `json:"accepted"`
}

// DelegationError represents a specific delegation failure.
type DelegationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"` // HTTP status code
}

func (e *DelegationError) Error() string {
	return e.Code + ": " + e.Message
}

// Standard delegation error codes.
var (
	// 400 — malformed request
	ErrInvalidProof      = &DelegationError{Code: "invalid_proof", Message: "invalid transaction", Status: 400}
	ErrInvalidPartialTx  = &DelegationError{Code: "invalid_partial_tx", Message: "invalid partial transaction", Status: 400}
	ErrInvalidSighash    = &DelegationError{Code: "invalid_sighash", Message: "client input does not use required sighash flag 0xC1", Status: 400}
	ErrUnexpectedOutputs = &DelegationError{Code: "invalid_proof", Message: "unexpected transaction outputs", Status: 400}

	// 402 — payment problems (client must fix and retry)
	ErrInsufficientAmount = &DelegationError{Code: "insufficient_amount", Message: "payment amount is below minimum", Status: 402}
	ErrExpiredChallenge   = &DelegationError{Code: "expired_challenge", Message: "challenge has expired", Status: 402}

	// 403 — payee/binding mismatch
	ErrInvalidPayee   = &DelegationError{Code: "invalid_payee", Message: "payment does not go to expected address", Status: 403}
	ErrInvalidBinding = &DelegationError{Code: "invalid_binding", Message: "request does not match challenge binding", Status: 403}
	ErrInvalidVersion = &DelegationError{Code: "invalid_version", Message: "challenge version does not match expected", Status: 400}
	ErrInvalidScheme  = &DelegationError{Code: "invalid_scheme", Message: "challenge scheme does not match expected", Status: 400}

	// 202 — nonce reservation in flight (concurrent request is processing)
	ErrNoncePending = &DelegationError{Code: "nonce_pending", Message: "nonce is being processed by another request", Status: 202}

	// 409 — replay / double-spend
	ErrDoubleSpend = &DelegationError{Code: "double_spend", Message: "challenge has already been settled", Status: 409}

	// 503 — resource exhaustion
	ErrNoUTXOAvailable = &DelegationError{Code: "no_utxos_available", Message: "no UTXOs available for delegation", Status: 503}
)
