import { describe, it } from "node:test"
import assert from "node:assert/strict"
import {
  buildPartialTransaction,
  isTemplateMode,
  computeTxid,
} from "../src/transaction.js"
import type { Challenge } from "../src/types.js"

const BASE_CHALLENGE: Challenge = {
  v: "1",
  scheme: "bsv-tx-v1",
  amount_sats: 100,
  payee_locking_script_hex:
    "76a91489abcdefabbaabbaabbaabbaabbaabbaabbaabba88ac",
  expires_at: Math.floor(Date.now() / 1000) + 300,
  domain: "api.example.com",
  method: "GET",
  path: "/v1/resource",
  query: "",
  req_headers_sha256: "0".repeat(64),
  req_body_sha256: "0".repeat(64),
  nonce_utxo: {
    txid: "ab".repeat(32),
    vout: 0,
    satoshis: 1,
    locking_script_hex:
      "76a91489abcdefabbaabbaabbaabbaabbaabbaabbaabba88ac",
  },
  require_mempool_accept: true,
  confirmations_required: 0,
}

describe("buildPartialTransaction", () => {
  it("builds an unsigned partial tx for Profile A", () => {
    const hex = buildPartialTransaction(BASE_CHALLENGE)

    // Must be valid hex
    assert.match(hex, /^[0-9a-f]+$/i)

    // Parse raw tx bytes
    const buf = Buffer.from(hex, "hex")

    // Version: 1
    assert.equal(buf.readUInt32LE(0), 1)

    // Input count: 1
    assert.equal(buf[4], 1)

    // Previous txid (bytes 5..36): reversed nonce txid
    const prevTxid = Buffer.from(buf.subarray(5, 37)).reverse().toString("hex")
    assert.equal(prevTxid, BASE_CHALLENGE.nonce_utxo.txid)

    // Previous vout (bytes 37..40)
    assert.equal(buf.readUInt32LE(37), 0)

    // ScriptSig length: 0 (unsigned)
    assert.equal(buf[41], 0)

    // Sequence: 0xffffffff
    assert.equal(buf.readUInt32LE(42), 0xffffffff)

    // Output count: 1
    assert.equal(buf[46], 1)

    // Output value: 100 sats
    assert.equal(Number(buf.readBigUInt64LE(47)), 100)
  })

  it("returns the template hex for Profile B", () => {
    const templateHex = "deadbeef"
    const challenge: Challenge = {
      ...BASE_CHALLENGE,
      template: {
        rawtx_hex: templateHex,
        price_sats: 100,
      },
    }

    assert.equal(buildPartialTransaction(challenge), templateHex)
  })
})

describe("isTemplateMode", () => {
  it("returns false for Profile A", () => {
    assert.equal(isTemplateMode(BASE_CHALLENGE), false)
  })

  it("returns true for Profile B", () => {
    const challenge: Challenge = {
      ...BASE_CHALLENGE,
      template: { rawtx_hex: "deadbeef", price_sats: 100 },
    }
    assert.equal(isTemplateMode(challenge), true)
  })

  it("returns false for null template", () => {
    const challenge: Challenge = { ...BASE_CHALLENGE, template: null }
    assert.equal(isTemplateMode(challenge), false)
  })
})

describe("computeTxid", () => {
  it("returns a 64-char hex string", () => {
    const hex = buildPartialTransaction(BASE_CHALLENGE)
    const txid = computeTxid(hex)
    assert.equal(txid.length, 64)
    assert.match(txid, /^[0-9a-f]+$/)
  })

  it("is deterministic", () => {
    const hex = buildPartialTransaction(BASE_CHALLENGE)
    assert.equal(computeTxid(hex), computeTxid(hex))
  })
})
