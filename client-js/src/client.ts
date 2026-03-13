// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import type {
  X402ClientConfig,
  Delegator,
  Broadcaster,
  DelegationRequest,
} from "./types.js"
import { parseChallenge, CHALLENGE_HEADER, PROOF_HEADER } from "./challenge.js"
import { buildPartialTransaction, isTemplateMode } from "./transaction.js"
import { buildProofHeader } from "./proof.js"
import { HttpDelegator } from "./delegator.js"
import { WoCBroadcaster, WOC_MAINNET } from "./broadcaster.js"
import { X402ChallengeError } from "./errors.js"

/**
 * Client for the x402-BSV protocol.
 *
 * Provides a `fetch()`-like API that transparently handles HTTP 402
 * Payment Required challenges:
 *
 * 1. Sends the original HTTP request.
 * 2. On 402, parses the challenge from `X402-Challenge`.
 * 3. Builds a partial transaction (nonce input + payee output).
 * 4. Submits the partial tx to the delegator for completion.
 * 5. Broadcasts the completed transaction to the network.
 * 6. Retries the original request with the `X402-Proof` header.
 *
 * The client is stateless and requires no wallet or balance tracking.
 */
export class X402Client {
  private readonly delegator: Delegator
  private readonly broadcaster: Broadcaster
  private readonly defaultHeaders: Record<string, string>
  private readonly fetchFn: typeof globalThis.fetch

  constructor(config: X402ClientConfig) {
    const fetchFn = config.fetch ?? globalThis.fetch.bind(globalThis)
    this.fetchFn = fetchFn
    this.defaultHeaders = config.defaultHeaders ?? {}

    this.delegator = new HttpDelegator(
      config.delegatorUrl,
      config.delegatorPath,
      fetchFn,
    )

    this.broadcaster = new WoCBroadcaster(
      config.broadcastUrl ?? WOC_MAINNET,
      fetchFn,
    )
  }

  /**
   * Drop-in replacement for `fetch()` that automatically handles 402
   * payment challenges.
   *
   * Non-402 responses are returned as-is. On 402, the full payment flow
   * is executed and the retried response is returned.
   */
  async fetch(
    input: string | URL,
    init?: RequestInit,
  ): Promise<Response> {
    const mergedInit = this.mergeHeaders(init)

    // 1. Perform original request
    const res = await this.fetchFn(input, mergedInit)

    // 2. Not a 402 — pass through
    if (res.status !== 402) {
      return res
    }

    // 3. Extract and parse challenge
    const challengeHeader = res.headers.get(CHALLENGE_HEADER)
    if (!challengeHeader) {
      throw new X402ChallengeError(
        `Received 402 but no ${CHALLENGE_HEADER} header present`,
      )
    }

    const { challenge, hash: challengeHash } = parseChallenge(challengeHeader)

    // 4. Check expiry
    const nowSecs = Math.floor(Date.now() / 1000)
    if (challenge.expires_at > 0 && challenge.expires_at < nowSecs) {
      throw new X402ChallengeError(
        `Challenge expired at ${challenge.expires_at} (now: ${nowSecs})`,
      )
    }

    // 5. Build partial transaction
    const partialTxHex = buildPartialTransaction(challenge)
    const templateMode = isTemplateMode(challenge)

    // 6. Submit to delegator for completion
    const delegationReq: DelegationRequest = {
      partial_tx_hex: partialTxHex,
      challenge_hash: challengeHash,
      payee_locking_script_hex: challenge.payee_locking_script_hex,
      amount_sats: challenge.amount_sats,
      nonce_outpoint: {
        txid: challenge.nonce_utxo.txid,
        vout: challenge.nonce_utxo.vout,
        satoshis: challenge.nonce_utxo.satoshis,
      },
      template_mode: templateMode,
    }

    const delegation = await this.delegator.completeTransaction(delegationReq)

    // 7. Broadcast the completed transaction
    await this.broadcaster.broadcast(delegation.rawtx_hex)

    // 8. Build proof and retry
    const url = new URL(typeof input === "string" ? input : input.toString())
    const method = mergedInit?.method?.toUpperCase() ?? "GET"
    const headers = new Headers(mergedInit?.headers)
    const body = extractBody(mergedInit)

    const proofHeader = buildProofHeader({
      txid: delegation.txid,
      rawtxHex: delegation.rawtx_hex,
      challengeHash,
      url,
      method,
      headers,
      body,
    })

    // 9. Retry with proof — once only
    const retryHeaders = new Headers(mergedInit?.headers)
    retryHeaders.set(PROOF_HEADER, proofHeader)

    return this.fetchFn(input, {
      ...mergedInit,
      headers: retryHeaders,
    })
  }

  /** Merge default headers into the request init. */
  private mergeHeaders(init?: RequestInit): RequestInit | undefined {
    const defaultKeys = Object.keys(this.defaultHeaders)
    if (defaultKeys.length === 0) return init

    const merged = new Headers(init?.headers)
    for (const [k, v] of Object.entries(this.defaultHeaders)) {
      if (!merged.has(k)) {
        merged.set(k, v)
      }
    }

    return { ...init, headers: merged }
  }
}

/** Extract the body as a string or null from RequestInit. */
function extractBody(
  init: RequestInit | undefined,
): string | null {
  if (!init?.body) return null
  if (typeof init.body === "string") return init.body
  return null
}
