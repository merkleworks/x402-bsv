// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import type { Broadcaster } from "./types.js"
import { BroadcastError } from "./errors.js"

/** WhatsOnChain mainnet base URL. */
export const WOC_MAINNET = "https://api.whatsonchain.com/v1/bsv/main"

/** WhatsOnChain testnet base URL. */
export const WOC_TESTNET = "https://api.whatsonchain.com/v1/bsv/test"

/**
 * Broadcasts raw BSV transactions via the WhatsOnChain API.
 *
 * Sends the raw transaction hex as JSON `{"txhex": "..."}` to
 * `POST {baseUrl}/tx/raw`. Returns the txid on success.
 */
export class WoCBroadcaster implements Broadcaster {
  private readonly endpoint: string
  private readonly fetchFn: typeof globalThis.fetch

  constructor(
    baseUrl: string = WOC_MAINNET,
    fetchFn: typeof globalThis.fetch = globalThis.fetch.bind(globalThis),
  ) {
    this.endpoint = baseUrl.replace(/\/+$/, "") + "/tx/raw"
    this.fetchFn = fetchFn
  }

  async broadcast(rawtx: string): Promise<string> {
    let res: Response
    try {
      res = await this.fetchFn(this.endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ txhex: rawtx }),
      })
    } catch (err) {
      throw new BroadcastError(
        `Broadcast request failed: ${err instanceof Error ? err.message : String(err)}`,
      )
    }

    if (!res.ok) {
      const body = await res.text().catch(() => "")
      throw new BroadcastError(
        `Broadcast failed: ${res.status} ${body}`,
        String(res.status),
      )
    }

    // WoC returns the txid as a JSON-quoted string with possible trailing newline
    const body = await res.text()
    let txid = body.trim()

    // Remove surrounding quotes if present
    if (txid.length >= 2 && txid[0] === '"' && txid[txid.length - 1] === '"') {
      txid = txid.slice(1, -1)
    }

    if (!txid || txid.length !== 64) {
      throw new BroadcastError(
        `Broadcast response unexpected: ${body}`,
      )
    }

    return txid
  }
}
