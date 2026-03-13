// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import type { Proof, RequestBinding } from "./types.js"
import { hashHeaders, hashBody } from "./challenge.js"

/**
 * Build the X402-Proof header value.
 *
 * The proof is JSON encoded with base64url (no padding). The inner
 * `rawtx_b64` field uses standard base64 (with padding) per the spec.
 */
export function buildProofHeader(params: {
  txid: string
  rawtxHex: string
  challengeHash: string
  url: URL
  method: string
  headers: Headers | Record<string, string>
  body: string | Uint8Array | null | undefined
}): string {
  const request: RequestBinding = {
    domain: params.url.host,
    method: params.method.toUpperCase(),
    path: params.url.pathname,
    query: params.url.search.replace(/^\?/, ""),
    req_headers_sha256: hashHeaders(params.headers),
    req_body_sha256: hashBody(params.body),
  }

  const proof: Proof = {
    v: "1",
    scheme: "bsv-tx-v1",
    txid: params.txid,
    rawtx_b64: Buffer.from(params.rawtxHex, "hex").toString("base64"),
    challenge_sha256: params.challengeHash,
    request,
  }

  const json = JSON.stringify(proof)
  return Buffer.from(json, "utf-8").toString("base64url")
}
