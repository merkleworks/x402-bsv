// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package challenge

// NonceRef identifies a specific UTXO that the challenge is bound to.
// The proof transaction MUST spend this outpoint as one of its inputs.
// Bitcoin consensus guarantees single-spend, providing replay protection
// without any server-side state.
type NonceRef struct {
	TxID             string `json:"txid"`               // 64-char hex txid of the nonce UTXO
	Vout             uint32 `json:"vout"`                // output index
	Satoshis         uint64 `json:"satoshis"`            // value (typically 1 sat)
	LockingScriptHex string `json:"locking_script_hex"`  // hex P2PKH locking script
}

// TemplateRef carries a pre-signed transaction template for Profile B
// (Gateway Template mode). The template contains the nonce input (signed
// by the gateway with 0xC3) and the payment output. Sponsors extend it
// by appending funding inputs and optional change outputs.
type TemplateRef struct {
	RawTxHex  string `json:"rawtx_hex"`  // hex pre-signed partial transaction
	PriceSats uint64 `json:"price_sats"` // payment amount locked in the template
}

// Challenge is the 402 payment challenge sent to the client.
type Challenge struct {
	V                     string `json:"v"`                        // "1"
	Scheme                string `json:"scheme"`                   // "bsv-tx-v1"
	AmountSats            int64  `json:"amount_sats"`              // price in satoshis
	PayeeLockingScriptHex string `json:"payee_locking_script_hex"` // hex-encoded locking script
	ExpiresAt             int64  `json:"expires_at"`               // unix timestamp (seconds)

	// Request binding fields (flat, per spec)
	Domain           string `json:"domain"`             // host header
	Method           string `json:"method"`             // HTTP method
	Path             string `json:"path"`               // request path
	Query            string `json:"query"`              // raw query string (empty string if none)
	ReqHeadersSHA256 string `json:"req_headers_sha256"` // SHA-256 of canonical headers
	ReqBodySHA256    string `json:"req_body_sha256"`    // SHA-256 of body bytes

	// Nonce UTXO — binds this challenge to a specific UTXO for replay protection.
	// The proof transaction MUST spend this outpoint as one of its inputs.
	NonceUTXO *NonceRef `json:"nonce_utxo,omitempty"`

	// Profile B: pre-signed transaction template containing the nonce input
	// and payment output. Omitted for Profile A challenges.
	Template *TemplateRef `json:"template,omitempty"`

	// Settlement parameters (spec-defined)
	RequireMempoolAccept  bool `json:"require_mempool_accept"`  // true = 0-conf gating
	ConfirmationsRequired int  `json:"confirmations_required"`  // 0 for instant
}
