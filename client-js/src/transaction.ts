// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import { createHash } from "node:crypto"
import type { Challenge, NonceRef } from "./types.js"

// ---------------------------------------------------------------------------
// Partial transaction builder
// ---------------------------------------------------------------------------

/**
 * Build a partial BSV transaction for the given challenge.
 *
 * - **Profile B** (template present): returns the pre-signed template hex.
 * - **Profile A** (no template): constructs an unsigned tx with the nonce
 *   input and the payee output. The delegator will sign it and add fees.
 *
 * Returns the hex-encoded raw transaction.
 */
export function buildPartialTransaction(challenge: Challenge): string {
  // Profile B: use the gateway-provided pre-signed template
  if (challenge.template?.rawtx_hex) {
    return challenge.template.rawtx_hex
  }

  // Profile A: build an unsigned partial tx
  return buildUnsignedPartialTx(
    challenge.nonce_utxo,
    challenge.payee_locking_script_hex,
    challenge.amount_sats,
  )
}

/**
 * Whether the challenge uses Profile B (pre-signed template).
 */
export function isTemplateMode(challenge: Challenge): boolean {
  return challenge.template != null && challenge.template.rawtx_hex.length > 0
}

/**
 * Compute the txid (double-SHA256, byte-reversed) from a raw transaction hex.
 */
export function computeTxid(rawTxHex: string): string {
  const raw = Buffer.from(rawTxHex, "hex")
  const hash1 = createHash("sha256").update(raw).digest()
  const hash2 = createHash("sha256").update(hash1).digest()
  return Buffer.from(hash2).reverse().toString("hex")
}

// ---------------------------------------------------------------------------
// Raw transaction construction
// ---------------------------------------------------------------------------

/**
 * Build an unsigned BSV transaction with one input (nonce UTXO) and one
 * output (payee payment). No signing is performed — the delegator will
 * sign the nonce input and append fee inputs.
 *
 * BSV raw transaction format:
 *   version (4 LE) | inputCount (varint) | inputs | outputCount (varint) | outputs | locktime (4 LE)
 */
function buildUnsignedPartialTx(
  nonce: NonceRef,
  lockingScriptHex: string,
  amountSats: number,
): string {
  const parts: Buffer[] = []

  // Version 1
  const version = Buffer.alloc(4)
  version.writeUInt32LE(1)
  parts.push(version)

  // --- Inputs ---
  parts.push(encodeVarInt(1))

  // Previous output hash (internal byte order = reversed display txid)
  parts.push(Buffer.from(nonce.txid, "hex").reverse())

  // Previous output index
  const vout = Buffer.alloc(4)
  vout.writeUInt32LE(nonce.vout)
  parts.push(vout)

  // ScriptSig (empty — unsigned)
  parts.push(encodeVarInt(0))

  // Sequence
  const seq = Buffer.alloc(4)
  seq.writeUInt32LE(0xffffffff)
  parts.push(seq)

  // --- Outputs ---
  parts.push(encodeVarInt(1))

  // Value
  const value = Buffer.alloc(8)
  value.writeBigUInt64LE(BigInt(amountSats))
  parts.push(value)

  // Locking script
  const script = Buffer.from(lockingScriptHex, "hex")
  parts.push(encodeVarInt(script.length))
  parts.push(script)

  // Locktime
  const locktime = Buffer.alloc(4)
  locktime.writeUInt32LE(0)
  parts.push(locktime)

  return Buffer.concat(parts).toString("hex")
}

/** Encode an integer as a Bitcoin varint. */
function encodeVarInt(n: number): Buffer {
  if (n < 0xfd) {
    return Buffer.from([n])
  }
  if (n <= 0xffff) {
    const buf = Buffer.alloc(3)
    buf[0] = 0xfd
    buf.writeUInt16LE(n, 1)
    return buf
  }
  if (n <= 0xffffffff) {
    const buf = Buffer.alloc(5)
    buf[0] = 0xfe
    buf.writeUInt32LE(n, 1)
    return buf
  }
  const buf = Buffer.alloc(9)
  buf[0] = 0xff
  buf.writeBigUInt64LE(BigInt(n), 1)
  return buf
}
