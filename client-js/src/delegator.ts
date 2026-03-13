// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import type {
  Delegator,
  DelegationRequest,
  DelegationResult,
} from "./types.js"
import { DelegatorError } from "./errors.js"

const DEFAULT_PATH = "/delegate/x402"

/**
 * Wire format for the delegation request sent to the gateway.
 * Uses `partial_tx` (not `partial_tx_hex`) to match the live server.
 */
interface DelegateWireRequest {
  partial_tx: string
  challenge_hash: string
  payee_locking_script_hex?: string
  amount_sats?: number
  nonce_outpoint?: {
    txid: string
    vout: number
    satoshis?: number
  }
  template_mode?: boolean
}

/**
 * Wire format for the delegation response from the gateway.
 * The server returns `completed_tx` (not `rawtx_hex`).
 */
interface DelegateWireResponse {
  txid: string
  completed_tx?: string
  rawtx_hex?: string
}

/**
 * HTTP-based delegator that sends the partial transaction to the gateway's
 * delegation endpoint for completion (fee inputs + signing).
 */
export class HttpDelegator implements Delegator {
  private readonly endpoint: string
  private readonly fetchFn: typeof globalThis.fetch

  constructor(
    baseUrl: string,
    path: string = DEFAULT_PATH,
    fetchFn: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
  ) {
    this.endpoint = baseUrl.replace(/\/+$/, "") + path
    this.fetchFn = fetchFn
  }

  async completeTransaction(
    request: DelegationRequest,
  ): Promise<DelegationResult> {
    // Map public interface to wire format
    const wireReq: DelegateWireRequest = {
      partial_tx: request.partial_tx_hex,
      challenge_hash: request.challenge_hash,
      payee_locking_script_hex: request.payee_locking_script_hex,
      amount_sats: request.amount_sats,
      nonce_outpoint: request.nonce_outpoint,
      template_mode: request.template_mode,
    }

    let res: Response
    try {
      res = await this.fetchFn(this.endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(wireReq),
      })
    } catch (err) {
      throw new DelegatorError(
        `Delegator request failed: ${err instanceof Error ? err.message : String(err)}`,
      )
    }

    if (!res.ok) {
      const body = await res.text().catch(() => "")
      let code: string | undefined
      try {
        const json = JSON.parse(body) as { code?: string; message?: string; error?: string }
        code = json.code
        throw new DelegatorError(
          json.message || json.error || `Delegator returned ${res.status}`,
          code,
          res.status,
        )
      } catch (e) {
        if (e instanceof DelegatorError) throw e
        throw new DelegatorError(
          `Delegator returned ${res.status}: ${body}`,
          undefined,
          res.status,
        )
      }
    }

    const data = (await res.json()) as DelegateWireResponse

    // Accept both `completed_tx` (live server) and `rawtx_hex` (spec)
    const rawtx = data.completed_tx || data.rawtx_hex

    if (!rawtx || !data.txid) {
      throw new DelegatorError(
        "Delegator response missing txid or completed_tx",
      )
    }

    return {
      txid: data.txid,
      rawtx_hex: rawtx,
      accepted: true,
    }
  }
}
