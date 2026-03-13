// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import { createHash } from "node:crypto"
import type { Challenge, ParsedChallenge } from "./types.js"
import { X402ChallengeError } from "./errors.js"

export const CHALLENGE_HEADER = "x402-challenge"
export const PROOF_HEADER = "X402-Proof"
export const ACCEPT_HEADER = "x402-accept"

/** Header names included in request-binding hashes. */
const HEADER_ALLOWLIST = [
  "accept",
  "content-length",
  "content-type",
  "x402-client",
  "x402-idempotency-key",
]

// ---------------------------------------------------------------------------
// Challenge parsing
// ---------------------------------------------------------------------------

/**
 * Parse the X402-Challenge header value.
 *
 * Accepts either plain base64url JSON or the compact prefix form
 * `v1.bsv-tx.<base64url>`.
 *
 * Returns the parsed challenge, the original canonical bytes (for hashing),
 * and the SHA-256 hex digest of those bytes.
 */
export function parseChallenge(headerValue: string): ParsedChallenge {
  let payload = headerValue

  // Strip compact prefix: "v1.bsv-tx.<base64url>"
  const prefixMatch = payload.match(/^v\d+\.[^.]+\.(.+)$/)
  if (prefixMatch) {
    payload = prefixMatch[1]
  }

  const rawBytes = Buffer.from(payload, "base64url")
  const hash = sha256hex(rawBytes)

  let challenge: Challenge
  try {
    challenge = JSON.parse(rawBytes.toString("utf-8"))
  } catch {
    throw new X402ChallengeError("Invalid challenge JSON")
  }

  if (!challenge.nonce_utxo) {
    throw new X402ChallengeError("Challenge missing nonce_utxo")
  }
  if (!challenge.payee_locking_script_hex) {
    throw new X402ChallengeError("Challenge missing payee_locking_script_hex")
  }
  if (typeof challenge.amount_sats !== "number" || challenge.amount_sats <= 0) {
    throw new X402ChallengeError("Challenge missing or invalid amount_sats")
  }

  return { challenge, rawBytes, hash }
}

// ---------------------------------------------------------------------------
// Canonical JSON (RFC 8785 / JCS style)
// ---------------------------------------------------------------------------

/**
 * Produce a canonical JSON string with sorted keys and no whitespace.
 * Used to compute the challenge hash deterministically.
 */
export function canonicalize(value: unknown): string {
  if (value === null || value === undefined) return "null"
  if (typeof value === "boolean") return value ? "true" : "false"
  if (typeof value === "number") {
    if (!isFinite(value)) return "null"
    // Integers render without decimal; floats as-is
    return Object.is(value, -0) ? "0" : String(value)
  }
  if (typeof value === "string") return JSON.stringify(value)
  if (Array.isArray(value)) {
    return "[" + value.map(canonicalize).join(",") + "]"
  }
  if (typeof value === "object") {
    const obj = value as Record<string, unknown>
    const keys = Object.keys(obj).sort()
    const pairs: string[] = []
    for (const k of keys) {
      if (obj[k] === undefined) continue
      pairs.push(JSON.stringify(k) + ":" + canonicalize(obj[k]))
    }
    return "{" + pairs.join(",") + "}"
  }
  return String(value)
}

// ---------------------------------------------------------------------------
// Hashing utilities
// ---------------------------------------------------------------------------

/** SHA-256 hex digest. */
export function sha256hex(data: Buffer | string): string {
  return createHash("sha256")
    .update(typeof data === "string" ? Buffer.from(data, "utf-8") : data)
    .digest("hex")
}

/**
 * Hash request headers per the x402 spec.
 *
 * 1. Only headers in the allowlist are included.
 * 2. Header names are lowercased.
 * 3. Values are trimmed and internal whitespace collapsed.
 * 4. Sorted lexicographically by name.
 * 5. Formatted as `name:value\n` for each, concatenated.
 * 6. SHA-256 of the UTF-8 bytes.
 */
export function hashHeaders(
  headers: Headers | Record<string, string>,
): string {
  // Per spec: ALL allowlist headers are always included, even with empty values.
  // This matches Go's HashHeaders which uses headers.Get(k) returning "" for absent keys.
  const sorted = [...HEADER_ALLOWLIST].sort()
  const parts: string[] = []

  for (const key of sorted) {
    let value = ""

    if (headers instanceof Headers) {
      value = headers.get(key) ?? ""
    } else {
      for (const [k, v] of Object.entries(headers)) {
        if (k.toLowerCase() === key) {
          value = v
          break
        }
      }
    }

    const normalized = value.trim().replace(/\s+/g, " ")
    parts.push(`${key}:${normalized}`)
  }

  let canonical = parts.join("\n")
  if (parts.length > 0) canonical += "\n"

  return sha256hex(canonical)
}

/** SHA-256 hex digest of the request body (empty string → empty hash). */
export function hashBody(body: string | Uint8Array | null | undefined): string {
  if (!body || (typeof body === "string" && body.length === 0)) {
    return sha256hex("")
  }
  if (typeof body === "string") {
    return sha256hex(body)
  }
  return sha256hex(Buffer.from(body))
}
