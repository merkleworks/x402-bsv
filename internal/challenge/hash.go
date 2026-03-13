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
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// collapseWS replaces runs of whitespace with a single space.
var collapseWS = regexp.MustCompile(`\s+`)

// HashBody returns the SHA-256 hex digest of the request body.
// Per spec §3.2: SHA256(raw_body_bytes) → hex. If no body: SHA256("") → hex.
func HashBody(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// HashHeaders returns the SHA-256 hex digest of selected headers in canonical form.
// Per spec (04-Protocol-Spec.md §3.1):
//   - Lowercase header names
//   - Trim surrounding whitespace in values
//   - Collapse internal runs of whitespace to single space
//   - Sort by header name (lexicographic)
//   - Join as: name:value\n for each header
//   - SHA256(utf8(canonical_string)) → hex
func HashHeaders(headers http.Header, keys []string) string {
	sortedKeys := make([]string, len(keys))
	copy(sortedKeys, keys)
	sort.Strings(sortedKeys)

	var parts []string
	for _, k := range sortedKeys {
		lk := strings.ToLower(k)
		val := strings.TrimSpace(headers.Get(k))
		val = collapseWS.ReplaceAllString(val, " ")
		parts = append(parts, lk+":"+val)
	}

	// Each header line ends with \n per spec
	canonical := strings.Join(parts, "\n")
	if len(parts) > 0 {
		canonical += "\n"
	}
	h := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(h[:])
}
