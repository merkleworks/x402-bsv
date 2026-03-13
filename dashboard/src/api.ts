// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import type { ConfigResponse, StatsSummary, TimeseriesPoint, TreasuryInfo, TreasuryUTXOsResponse, FanoutHistoryEntry, ChallengeData, BroadcasterHealthResponse, RevenueStats } from './types'

const BASE = ''

async function fetchJSON<T>(url: string, opts?: RequestInit): Promise<T> {
  const res = await fetch(BASE + url, {
    headers: { 'Content-Type': 'application/json' },
    ...opts,
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`${res.status}: ${body}`)
  }
  return res.json()
}

// Config
export async function getConfig(): Promise<ConfigResponse> {
  return fetchJSON('/api/v1/config')
}

export async function updateConfig(updates: Record<string, unknown>): Promise<{ success: boolean; updated: Record<string, unknown>; restart_required?: boolean; restart_reason?: string }> {
  return fetchJSON('/api/v1/config', {
    method: 'PUT',
    body: JSON.stringify(updates),
  })
}

// Stats
export async function getStatsSummary(): Promise<StatsSummary> {
  return fetchJSON('/api/v1/stats/summary')
}

export async function getTimeseries(): Promise<{ points: TimeseriesPoint[]; bucketMs: number }> {
  return fetchJSON('/api/v1/stats/timeseries')
}

// Treasury
export async function getTreasuryInfo(): Promise<TreasuryInfo> {
  return fetchJSON('/api/v1/treasury/info')
}

export async function triggerFanout(req: {
  pool: string
  count: number
  fundingTxid: string
  fundingVout: number
  fundingScript: string
  fundingSatoshis: number
}): Promise<{ success: boolean; txid: string; utxoCount: number; pool: string }> {
  return fetchJSON('/api/v1/treasury/fanout', {
    method: 'POST',
    body: JSON.stringify(req),
  })
}

export async function getTreasuryUTXOs(): Promise<TreasuryUTXOsResponse> {
  return fetchJSON('/api/v1/treasury/utxos')
}

export async function getFanoutHistory(): Promise<{ success: boolean; history: FanoutHistoryEntry[] }> {
  return fetchJSON('/api/v1/treasury/history')
}

export async function sweepRevenue(): Promise<{ success: boolean; txid: string; inputCount: number; inputSats: number; outputSats: number; fee: number }> {
  return fetchJSON('/api/v1/treasury/sweep-revenue', { method: 'POST' })
}

// Pool reconciliation (checks UTXOs against blockchain with positive confirmation)
export async function reconcilePools(): Promise<{
  success: boolean
  pools: {
    pool: string
    address: string
    checked: number
    valid: number
    zombies_removed: number
    leased_confirmed: number
    stale_released: number
    skipped: number
    error?: string
  }[]
  total_zombies: number
  total_confirmed: number
  total_stale_released: number
}> {
  return fetchJSON('/api/v1/pools/reconcile', { method: 'POST' })
}

// Revenue (persistent settlement tracker)
export async function getRevenue(): Promise<RevenueStats> {
  return fetchJSON('/api/v1/revenue')
}

// Health
export async function getHealth(): Promise<Record<string, unknown>> {
  return fetchJSON('/health')
}

// Broadcaster health (composite mode — per-service status)
export async function getBroadcasterHealth(): Promise<BroadcasterHealthResponse> {
  return fetchJSON('/api/v1/health/broadcasters')
}

// Testing: x402 flow
export async function testExpensiveEndpoint(proof?: string): Promise<{ status: number; body: unknown; headers: Record<string, string> }> {
  const headers: Record<string, string> = {}
  if (proof) {
    headers['X402-Proof'] = proof
  }
  const res = await fetch('/v1/expensive', { headers })
  const body = await res.json().catch(() => res.text())
  const respHeaders: Record<string, string> = {}
  res.headers.forEach((v, k) => { respHeaders[k] = v })
  return { status: res.status, body, headers: respHeaders }
}

// ---------------------------------------------------------------------------
// x402 Protocol Flow Functions (client-orchestrated, no BSV crypto needed)
// ---------------------------------------------------------------------------

// Decode a base64url-encoded challenge from the X402-Challenge header.
export function decodeChallenge(encoded: string): ChallengeData {
  // base64url → base64
  let b64 = encoded.replace(/-/g, '+').replace(/_/g, '/')
  while (b64.length % 4) b64 += '='
  const json = atob(b64)
  return JSON.parse(json)
}

// Call the delegator to complete a partial transaction (add fee inputs).
// POST /delegate/x402 → { completed_tx, txid }
export async function delegateTransaction(
  delegatorUrl: string,
  partialTxHex: string
): Promise<{ completed_tx: string; txid: string }> {
  const res = await fetch(`${delegatorUrl}/delegate/x402`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ partial_tx: partialTxHex }),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`Delegator error ${res.status}: ${body}`)
  }
  return res.json()
}

// Broadcast a completed transaction to the BSV network.
// For demo mode (mock broadcaster), this calls the gateway's mock broadcast endpoint.
export async function broadcastTransaction(
  broadcasterUrl: string,
  rawTxHex: string
): Promise<{ txid: string }> {
  const res = await fetch(broadcasterUrl, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ rawtx: rawTxHex }),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(`Broadcast error ${res.status}: ${body}`)
  }
  return res.json()
}

// Compute SHA-256 hash of a string (UTF-8 encoded), returned as hex.
async function sha256hex(data: string): Promise<string> {
  const encoder = new TextEncoder()
  const hashBuffer = await crypto.subtle.digest('SHA-256', encoder.encode(data))
  const hashArray = Array.from(new Uint8Array(hashBuffer))
  return hashArray.map(b => b.toString(16).padStart(2, '0')).join('')
}

// Produce canonical JSON with sorted keys (RFC 8785 / JCS style).
// Matches the Go gateway's challenge.CanonicalJSON() function.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function canonicalJSON(value: any): string {
  if (value === null || value === undefined) return 'null'
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  if (typeof value === 'number') {
    if (Number.isInteger(value)) return String(value)
    return String(value)
  }
  if (typeof value === 'string') return JSON.stringify(value)
  if (Array.isArray(value)) {
    return '[' + value.map(canonicalJSON).join(',') + ']'
  }
  if (typeof value === 'object') {
    const keys = Object.keys(value).sort()
    const entries = keys
      .filter(k => value[k] !== undefined) // omit undefined (matches Go's omitempty)
      .map(k => JSON.stringify(k) + ':' + canonicalJSON(value[k]))
    return '{' + entries.join(',') + '}'
  }
  return String(value)
}

// Convert hex string to base64.
function hexToBase64(hex: string): string {
  const bytes = new Uint8Array(hex.match(/.{1,2}/g)!.map(byte => parseInt(byte, 16)))
  let binary = ''
  bytes.forEach(b => { binary += String.fromCharCode(b) })
  return btoa(binary)
}

// Encode to base64url (no padding).
function toBase64url(str: string): string {
  return btoa(str).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// Build the X402-Proof header value from a completed transaction and challenge.
// No BSV signing needed — proof is just metadata binding the tx to the challenge.
//
// IMPORTANT: The challenge_sha256 must use canonical JSON (sorted keys) to match
// the gateway's challenge.ComputeHash() which uses CanonicalJSON(). The raw bytes
// from the X402-Challenge header use standard JSON (unsorted), so we must parse
// and re-serialize with sorted keys before hashing.
//
// The request binding hashes (req_headers_sha256, req_body_sha256) must match the
// values the gateway computed when it issued the challenge. Since the dashboard
// sends the same GET request with no custom headers or body, the hashes match.
export async function buildCompleteProof(
  completedTxHex: string,
  txid: string,
  _challengeEncoded: string,
  challenge: ChallengeData
): Promise<string> {
  // SHA-256 of canonical JSON representation of the challenge.
  // Must match the gateway's challenge.CanonicalJSON() → SHA-256.
  const canonical = canonicalJSON(challenge)
  const challengeHash = await sha256hex(canonical)

  const rawTxB64 = hexToBase64(completedTxHex)

  const proof = {
    v: challenge.v || '1',
    scheme: challenge.scheme || 'bsv-tx-v1',
    txid,
    rawtx_b64: rawTxB64,
    challenge_sha256: challengeHash,
    request: {
      domain: challenge.domain,
      method: challenge.method,
      path: challenge.path,
      query: challenge.query || '',
      // Use the binding hashes from the challenge itself — the gateway computed
      // these when issuing the challenge, and they must match exactly on proof.
      req_headers_sha256: challenge.req_headers_sha256,
      req_body_sha256: challenge.req_body_sha256,
    },
  }

  return toBase64url(JSON.stringify(proof))
}
