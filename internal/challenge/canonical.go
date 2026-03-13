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
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// CanonicalJSON produces a deterministic JSON byte representation suitable for hashing.
// It implements sorted-key serialisation (RFC 8785 / JCS style):
//   - Object keys are sorted lexicographically
//   - No whitespace between tokens
//   - Numbers are serialised without trailing zeros
//   - Nested objects/arrays are processed recursively
//
// This guarantees that the same Go struct always produces the same bytes,
// regardless of map iteration order or struct field declaration order.
func CanonicalJSON(v any) ([]byte, error) {
	// First, marshal to standard JSON to get a generic representation
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical json marshal: %w", err)
	}

	// Unmarshal into an untyped structure so we can sort keys
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("canonical json unmarshal: %w", err)
	}

	// Rebuild with sorted keys
	return canonicalValue(generic)
}

// canonicalValue recursively produces canonical JSON for any value.
func canonicalValue(v any) ([]byte, error) {
	switch val := v.(type) {
	case nil:
		return []byte("null"), nil

	case bool:
		if val {
			return []byte("true"), nil
		}
		return []byte("false"), nil

	case float64:
		// JSON numbers are always float64 after json.Unmarshal.
		// Use compact integer representation when possible.
		if val == float64(int64(val)) {
			return []byte(strconv.FormatInt(int64(val), 10)), nil
		}
		return []byte(strconv.FormatFloat(val, 'f', -1, 64)), nil

	case string:
		// Use standard Go JSON string encoding (handles escaping)
		b, err := json.Marshal(val)
		if err != nil {
			return nil, err
		}
		return b, nil

	case map[string]any:
		return canonicalObject(val)

	case []any:
		return canonicalArray(val)

	default:
		return nil, fmt.Errorf("unsupported type: %T", v)
	}
}

// canonicalObject produces canonical JSON for a map with sorted keys.
func canonicalObject(m map[string]any) ([]byte, error) {
	// Sort keys lexicographically
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	sb.WriteByte('{')

	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}

		// Write key
		keyJSON, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		sb.Write(keyJSON)
		sb.WriteByte(':')

		// Write value
		valJSON, err := canonicalValue(m[k])
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", k, err)
		}
		sb.Write(valJSON)
	}

	sb.WriteByte('}')
	return []byte(sb.String()), nil
}

// canonicalArray produces canonical JSON for an array.
func canonicalArray(arr []any) ([]byte, error) {
	var sb strings.Builder
	sb.WriteByte('[')

	for i, v := range arr {
		if i > 0 {
			sb.WriteByte(',')
		}
		valJSON, err := canonicalValue(v)
		if err != nil {
			return nil, fmt.Errorf("index %d: %w", i, err)
		}
		sb.Write(valJSON)
	}

	sb.WriteByte(']')
	return []byte(sb.String()), nil
}
