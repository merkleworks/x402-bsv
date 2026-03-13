import { describe, it } from "node:test"
import assert from "node:assert/strict"
import {
  parseChallenge,
  canonicalize,
  sha256hex,
  hashHeaders,
  hashBody,
} from "../src/challenge.js"
import type { Challenge } from "../src/types.js"

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const SAMPLE_CHALLENGE: Challenge = {
  v: "1",
  scheme: "bsv-tx-v1",
  amount_sats: 100,
  payee_locking_script_hex: "76a91489abcdefabbaabbaabbaabbaabbaabbaabbaabba88ac",
  expires_at: Math.floor(Date.now() / 1000) + 300,
  domain: "api.example.com",
  method: "GET",
  path: "/v1/resource",
  query: "",
  req_headers_sha256: sha256hex(""),
  req_body_sha256: sha256hex(""),
  nonce_utxo: {
    txid: "a".repeat(64),
    vout: 0,
    satoshis: 1,
    locking_script_hex: "76a91489abcdefabbaabbaabbaabbaabbaabbaabbaabba88ac",
  },
  require_mempool_accept: true,
  confirmations_required: 0,
}

function encodeChallenge(challenge: Challenge): string {
  const canonical = canonicalize(challenge)
  return Buffer.from(canonical, "utf-8").toString("base64url")
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("canonicalize", () => {
  it("sorts object keys lexicographically", () => {
    assert.equal(canonicalize({ b: 1, a: 2 }), '{"a":2,"b":1}')
  })

  it("handles nested objects", () => {
    assert.equal(
      canonicalize({ z: { b: 1, a: 2 }, a: 0 }),
      '{"a":0,"z":{"a":2,"b":1}}',
    )
  })

  it("handles arrays", () => {
    assert.equal(canonicalize([3, 1, 2]), "[3,1,2]")
  })

  it("handles null", () => {
    assert.equal(canonicalize(null), "null")
  })

  it("handles booleans", () => {
    assert.equal(canonicalize(true), "true")
    assert.equal(canonicalize(false), "false")
  })

  it("handles strings with escaping", () => {
    assert.equal(canonicalize('hello "world"'), '"hello \\"world\\""')
  })

  it("handles integers without decimal point", () => {
    assert.equal(canonicalize(100), "100")
  })

  it("skips undefined values in objects", () => {
    assert.equal(canonicalize({ a: 1, b: undefined }), '{"a":1}')
  })

  it("handles negative zero as zero", () => {
    assert.equal(canonicalize(-0), "0")
  })
})

describe("sha256hex", () => {
  it("hashes empty string correctly", () => {
    assert.equal(
      sha256hex(""),
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    )
  })

  it("hashes a known value", () => {
    assert.equal(
      sha256hex("hello"),
      "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
    )
  })
})

describe("parseChallenge", () => {
  it("parses a base64url-encoded challenge", () => {
    const encoded = encodeChallenge(SAMPLE_CHALLENGE)
    const { challenge, hash } = parseChallenge(encoded)

    assert.equal(challenge.v, "1")
    assert.equal(challenge.scheme, "bsv-tx-v1")
    assert.equal(challenge.amount_sats, 100)
    assert.equal(challenge.nonce_utxo.txid, "a".repeat(64))
    assert.equal(typeof hash, "string")
    assert.equal(hash.length, 64)
  })

  it("parses compact prefix format", () => {
    const encoded = encodeChallenge(SAMPLE_CHALLENGE)
    const prefixed = `v1.bsv-tx.${encoded}`
    const { challenge } = parseChallenge(prefixed)

    assert.equal(challenge.amount_sats, 100)
  })

  it("throws on invalid JSON", () => {
    const bad = Buffer.from("not json").toString("base64url")
    assert.throws(() => parseChallenge(bad), { name: "X402ChallengeError" })
  })

  it("throws on missing nonce_utxo", () => {
    const partial = { ...SAMPLE_CHALLENGE }
    delete (partial as Record<string, unknown>)["nonce_utxo"]
    const encoded = Buffer.from(JSON.stringify(partial)).toString("base64url")
    assert.throws(() => parseChallenge(encoded), {
      name: "X402ChallengeError",
    })
  })

  it("produces deterministic hash from canonical bytes", () => {
    const encoded = encodeChallenge(SAMPLE_CHALLENGE)
    const { hash: hash1 } = parseChallenge(encoded)
    const { hash: hash2 } = parseChallenge(encoded)
    assert.equal(hash1, hash2)
  })
})

describe("hashHeaders", () => {
  it("always includes all allowlist headers even when absent", () => {
    // Per Go spec: all 5 allowlist headers are always present (empty value for absent).
    // Canonical form: "accept:\ncontent-length:\ncontent-type:\nx402-client:\nx402-idempotency-key:\n"
    const expected = sha256hex(
      "accept:\ncontent-length:\ncontent-type:\nx402-client:\nx402-idempotency-key:\n",
    )
    assert.equal(hashHeaders({}), expected)
  })

  it("hashes allowlisted headers only", () => {
    const headers = {
      "Content-Type": "application/json",
      "Authorization": "Bearer token",
      "Accept": "text/html",
    }
    const h1 = hashHeaders(headers)

    // Authorization is not in the allowlist, so it should be ignored.
    // Same result without Authorization:
    const h2 = hashHeaders({
      "Content-Type": "application/json",
      "Accept": "text/html",
    })
    assert.equal(h1, h2)
  })

  it("normalizes whitespace", () => {
    const h1 = hashHeaders({ "Content-Type": "  application/json  " })
    const h2 = hashHeaders({ "Content-Type": "application/json" })
    assert.equal(h1, h2)
  })

  it("is case-insensitive on header names", () => {
    const h1 = hashHeaders({ "content-type": "application/json" })
    const h2 = hashHeaders({ "Content-Type": "application/json" })
    assert.equal(h1, h2)
  })

  it("produces canonical format matching Go server", () => {
    const h = hashHeaders({ "Content-Type": "application/json", "Accept": "*/*" })
    // Canonical: "accept:*/*\ncontent-length:\ncontent-type:application/json\nx402-client:\nx402-idempotency-key:\n"
    const expected = sha256hex(
      "accept:*/*\ncontent-length:\ncontent-type:application/json\nx402-client:\nx402-idempotency-key:\n",
    )
    assert.equal(h, expected)
  })
})

describe("hashBody", () => {
  it("hashes empty body correctly", () => {
    const empty = sha256hex("")
    assert.equal(hashBody(null), empty)
    assert.equal(hashBody(undefined), empty)
    assert.equal(hashBody(""), empty)
  })

  it("hashes string body", () => {
    assert.equal(hashBody("hello"), sha256hex("hello"))
  })

  it("hashes Uint8Array body", () => {
    const bytes = new TextEncoder().encode("hello")
    assert.equal(hashBody(bytes), sha256hex("hello"))
  })
})
