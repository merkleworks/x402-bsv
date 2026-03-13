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
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// Scheme is the x402 payment scheme identifier.
	Scheme = "bsv-tx-v1"

	// Version is the protocol version.
	Version = "1"
)

// BuildOptions configures challenge generation.
type BuildOptions struct {
	PayeeLockingScriptHex string        // hex locking script for payments
	Amount                int64         // price in satoshis
	Network               string        // "mainnet" or "testnet" (internal only, not on wire)
	TTL                   time.Duration // challenge validity period
	BindHeaders           []string      // which request headers to include in binding
	NonceUTXO             *NonceRef     // nonce UTXO for replay protection (required)
	Template              *TemplateRef  // Profile B: pre-signed template (nil for Profile A)
}

// Build creates a 402 challenge from an HTTP request.
func Build(req *http.Request, opts BuildOptions) (*Challenge, error) {
	// Read and restore the request body
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	ch := &Challenge{
		V:                     Version,
		Scheme:                Scheme,
		AmountSats:            opts.Amount,
		PayeeLockingScriptHex: opts.PayeeLockingScriptHex,
		ExpiresAt:             time.Now().Add(opts.TTL).Unix(),

		// Flat request binding fields (per spec)
		Domain:           req.Host,
		Method:           req.Method,
		Path:             req.URL.Path,
		Query:            req.URL.RawQuery,
		ReqHeadersSHA256: HashHeaders(req.Header, opts.BindHeaders),
		ReqBodySHA256:    HashBody(bodyBytes),

		// Nonce UTXO for replay protection
		NonceUTXO: opts.NonceUTXO,

		// Profile B template (nil for Profile A — omitted from JSON)
		Template: opts.Template,

		// Settlement parameters
		RequireMempoolAccept:  true,
		ConfirmationsRequired: 0,
	}

	return ch, nil
}

// ComputeHash produces a SHA-256 hex digest of the challenge
// using canonical (sorted-key) JSON serialisation (RFC 8785 style).
func ComputeHash(c *Challenge) (string, error) {
	data, err := CanonicalJSON(c)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// Encode serializes a challenge to base64url for the X402-Challenge header.
// Uses canonical (sorted-key) JSON so the client can SHA-256 the decoded bytes
// and obtain the same hash the server stored in the challenge cache.
func Encode(c *Challenge) (string, error) {
	data, err := CanonicalJSON(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// Decode parses a base64url-encoded challenge.
func Decode(encoded string) (*Challenge, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64url decode: %w", err)
	}

	var c Challenge
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}

	return &c, nil
}

// ValidateSchemeVersion checks that a challenge uses the expected scheme and version.
func ValidateSchemeVersion(c *Challenge) error {
	if c.Scheme != Scheme {
		return fmt.Errorf("invalid_scheme: got %q, want %q", c.Scheme, Scheme)
	}
	if c.V != Version {
		return fmt.Errorf("invalid_version: got %q, want %q", c.V, Version)
	}
	return nil
}
