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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// ParseProof decodes the X402-Proof header value into a Proof struct.
// Supports both formats:
//   - Plain base64url-encoded JSON
//   - Compact prefix: v1.bsv-tx.<base64url(JSON)>
func ParseProof(header string) (*Proof, error) {
	if header == "" {
		return nil, fmt.Errorf("empty proof header")
	}

	// Handle compact prefix format: v1.bsv-tx.<base64url>
	payload := header
	if strings.HasPrefix(header, "v1.bsv-tx.") {
		payload = strings.TrimPrefix(header, "v1.bsv-tx.")
	}

	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("base64url decode: %w", err)
	}

	var p Proof
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}

	// Validate required fields
	if p.RawTxB64 == "" {
		return nil, fmt.Errorf("missing rawtx_b64 field")
	}
	if p.ChallengeSHA256 == "" {
		return nil, fmt.Errorf("missing challenge_sha256 field")
	}
	if p.V == "" {
		p.V = "1" // default
	}
	if p.Scheme == "" {
		p.Scheme = "bsv-tx-v1" // default
	}

	return &p, nil
}

// EncodeProof encodes a Proof struct to base64url JSON for the X402-Proof header.
func EncodeProof(p *Proof) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
