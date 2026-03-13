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
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
)

// VerifyBinding re-computes the request binding from the current HTTP request
// and compares it against the flat binding fields in the original challenge.
// Returns nil if all fields match, or an error describing the first mismatch.
//
// This prevents:
//   - Proof replay across endpoints
//   - Proof reuse across domains
//   - Body substitution attacks
func VerifyBinding(ch *Challenge, req *http.Request, bindHeaders []string) error {
	// Verify method
	if req.Method != ch.Method {
		return fmt.Errorf("method mismatch: request=%q, challenge=%q", req.Method, ch.Method)
	}

	// Verify path
	if req.URL.Path != ch.Path {
		return fmt.Errorf("path mismatch: request=%q, challenge=%q", req.URL.Path, ch.Path)
	}

	// Verify domain
	if req.Host != ch.Domain {
		return fmt.Errorf("domain mismatch: request=%q, challenge=%q", req.Host, ch.Domain)
	}

	// Verify query (raw query string, not hash)
	if req.URL.RawQuery != ch.Query {
		return fmt.Errorf("query mismatch: request=%q, challenge=%q", req.URL.RawQuery, ch.Query)
	}

	// Recompute body hash
	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}
		// Restore the body for downstream handlers
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}
	bodyHash := HashBody(bodyBytes)
	if subtle.ConstantTimeCompare([]byte(bodyHash), []byte(ch.ReqBodySHA256)) != 1 {
		return fmt.Errorf("body hash mismatch: request=%s, challenge=%s", bodyHash, ch.ReqBodySHA256)
	}

	// Recompute headers hash (constant-time comparison)
	headersHash := HashHeaders(req.Header, bindHeaders)
	if subtle.ConstantTimeCompare([]byte(headersHash), []byte(ch.ReqHeadersSHA256)) != 1 {
		return fmt.Errorf("headers hash mismatch: request=%s, challenge=%s", headersHash, ch.ReqHeadersSHA256)
	}

	return nil
}
