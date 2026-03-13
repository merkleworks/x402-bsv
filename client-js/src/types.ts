// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

// ---------------------------------------------------------------------------
// Protocol types
// ---------------------------------------------------------------------------

/** Nonce UTXO reference included in a 402 challenge. */
export interface NonceRef {
  txid: string
  vout: number
  satoshis: number
  locking_script_hex: string
}

/** Pre-signed template (Profile B only). */
export interface TemplateRef {
  rawtx_hex: string
  price_sats: number
}

/** Decoded 402 challenge from the X402-Challenge header. */
export interface Challenge {
  v: string
  scheme: string
  amount_sats: number
  payee_locking_script_hex: string
  expires_at: number
  domain: string
  method: string
  path: string
  query: string
  req_headers_sha256: string
  req_body_sha256: string
  nonce_utxo: NonceRef
  template?: TemplateRef | null
  require_mempool_accept: boolean
  confirmations_required: number
}

/** Result of parsing the X402-Challenge header. */
export interface ParsedChallenge {
  challenge: Challenge
  /** Raw canonical JSON bytes (for hashing). */
  rawBytes: Buffer
  /** SHA-256 hex of rawBytes. */
  hash: string
}

// ---------------------------------------------------------------------------
// Client configuration
// ---------------------------------------------------------------------------

export interface X402ClientConfig {
  /** Base URL of the delegator service. */
  delegatorUrl: string
  /** Path on the delegator for tx completion. @default "/delegate/x402" */
  delegatorPath?: string
  /** Base URL for WhatsOnChain broadcast API. @default WOC_MAINNET */
  broadcastUrl?: string
  /** Extra headers sent with every proxied request. */
  defaultHeaders?: Record<string, string>
  /** Override the global fetch (useful for testing). */
  fetch?: typeof globalThis.fetch
}

// ---------------------------------------------------------------------------
// Delegator
// ---------------------------------------------------------------------------

export interface DelegationRequest {
  partial_tx_hex: string
  challenge_hash: string
  payee_locking_script_hex?: string
  amount_sats?: number
  nonce_outpoint: {
    txid: string
    vout: number
    satoshis?: number
  }
  template_mode?: boolean
}

export interface DelegationResult {
  txid: string
  rawtx_hex: string
  accepted: boolean
}

export interface Delegator {
  completeTransaction(request: DelegationRequest): Promise<DelegationResult>
}

// ---------------------------------------------------------------------------
// Broadcaster
// ---------------------------------------------------------------------------

export interface Broadcaster {
  broadcast(rawtx: string): Promise<string>
}

// ---------------------------------------------------------------------------
// Proof
// ---------------------------------------------------------------------------

export interface RequestBinding {
  domain: string
  method: string
  path: string
  query: string
  req_headers_sha256: string
  req_body_sha256: string
}

export interface Proof {
  v: string
  scheme: string
  txid: string
  rawtx_b64: string
  challenge_sha256: string
  request: RequestBinding
}
