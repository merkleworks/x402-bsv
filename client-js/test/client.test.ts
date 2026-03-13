import { describe, it } from "node:test"
import assert from "node:assert/strict"
import { X402Client } from "../src/client.js"
import { canonicalize, sha256hex } from "../src/challenge.js"
import type { Challenge } from "../src/types.js"

// ---------------------------------------------------------------------------
// Mock gateway helpers
// ---------------------------------------------------------------------------

const NONCE_TXID = "ab".repeat(32)

function makeChallengeObj(): Challenge {
  return {
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
    req_headers_sha256: sha256hex(""),
    req_body_sha256: sha256hex(""),
    nonce_utxo: {
      txid: NONCE_TXID,
      vout: 0,
      satoshis: 1,
      locking_script_hex:
        "76a91489abcdefabbaabbaabbaabbaabbaabbaabbaabba88ac",
    },
    require_mempool_accept: true,
    confirmations_required: 0,
  }
}

function encodeChallengeHeader(challenge: Challenge): string {
  return Buffer.from(canonicalize(challenge), "utf-8").toString("base64url")
}

/**
 * Build a mock fetch function that simulates:
 * 1. First call to the resource → 402 with challenge
 * 2. POST to delegator → delegation result
 * 3. POST to broadcaster → broadcast result
 * 4. Retry to the resource → 200 OK
 */
function createMockFetch() {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  const challenge = makeChallengeObj()
  const challengeHeader = encodeChallengeHeader(challenge)

  // Wire response from the delegator (server returns completed_tx, not rawtx_hex)
  const delegationResult = {
    txid: "cd".repeat(32),
    completed_tx: "01000000" + "00".repeat(50), // minimal fake tx
  }

  const mockFetch: typeof globalThis.fetch = async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const url = typeof input === "string" ? input : input.toString()
    calls.push({ url, init })

    // Resource endpoint — initial 402
    if (
      url === "https://api.example.com/v1/resource" &&
      !init?.headers?.toString().includes("X402-Proof")
    ) {
      // Check if this is the retry (has proof header)
      const headers = new Headers(init?.headers)
      if (headers.has("X402-Proof")) {
        return new Response(JSON.stringify({ data: "paid content" }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }

      return new Response("Payment Required", {
        status: 402,
        headers: {
          "x402-challenge": challengeHeader,
          "x402-accept": "bsv-tx-v1",
        },
      })
    }

    // Resource endpoint — retry with proof
    if (url === "https://api.example.com/v1/resource") {
      return new Response(JSON.stringify({ data: "paid content" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }

    // Delegator endpoint
    if (url.includes("/delegate/x402")) {
      return new Response(JSON.stringify(delegationResult), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })
    }

    // Broadcaster endpoint (WoC)
    if (url.includes("whatsonchain.com")) {
      // WoC returns txid as a quoted string
      return new Response(
        JSON.stringify(delegationResult.txid),
        {
          status: 200,
          headers: { "Content-Type": "application/json" },
        },
      )
    }

    return new Response("Not Found", { status: 404 })
  }

  return { mockFetch, calls, challenge, delegationResult }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("X402Client", () => {
  it("passes through non-402 responses", async () => {
    const mockFetch: typeof globalThis.fetch = async () =>
      new Response("OK", { status: 200 })

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    const res = await client.fetch("https://api.example.com/v1/free")
    assert.equal(res.status, 200)
    assert.equal(await res.text(), "OK")
  })

  it("handles the full 402 payment flow", async () => {
    const { mockFetch, calls } = createMockFetch()

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    const res = await client.fetch("https://api.example.com/v1/resource")

    // Should have made 4 calls:
    // 1. Original request → 402
    // 2. POST to delegator
    // 3. POST to broadcaster
    // 4. Retry with proof
    assert.equal(calls.length, 4)

    // Call 1: original request
    assert.equal(calls[0].url, "https://api.example.com/v1/resource")

    // Call 2: delegator
    assert.ok(calls[1].url.includes("/delegate/x402"))
    assert.equal(calls[1].init?.method, "POST")

    // Verify delegation request body
    const delegationBody = JSON.parse(calls[1].init?.body as string)
    assert.ok(delegationBody.partial_tx)
    assert.ok(delegationBody.challenge_hash)
    assert.equal(delegationBody.nonce_outpoint.txid, NONCE_TXID)

    // Call 3: broadcaster
    assert.ok(calls[2].url.includes("whatsonchain.com"))

    // Call 4: retry with proof
    assert.equal(calls[3].url, "https://api.example.com/v1/resource")
    const retryHeaders = new Headers(calls[3].init?.headers)
    assert.ok(retryHeaders.has("X402-Proof"))

    // Verify the proof header is valid base64url
    const proofValue = retryHeaders.get("X402-Proof")!
    const proofJson = Buffer.from(proofValue, "base64url").toString("utf-8")
    const proof = JSON.parse(proofJson)
    assert.equal(proof.v, "1")
    assert.equal(proof.scheme, "bsv-tx-v1")
    assert.ok(proof.txid)
    assert.ok(proof.rawtx_b64)
    assert.ok(proof.challenge_sha256)
    assert.ok(proof.request)

    // Final response should be 200
    assert.equal(res.status, 200)
  })

  it("throws X402ChallengeError for 402 without challenge header", async () => {
    const mockFetch: typeof globalThis.fetch = async () =>
      new Response("Payment Required", { status: 402 })

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    await assert.rejects(
      () => client.fetch("https://api.example.com/v1/resource"),
      { name: "X402ChallengeError" },
    )
  })

  it("throws X402ChallengeError for expired challenge", async () => {
    const challenge = makeChallengeObj()
    challenge.expires_at = Math.floor(Date.now() / 1000) - 60 // expired 1 min ago
    const header = encodeChallengeHeader(challenge)

    const mockFetch: typeof globalThis.fetch = async () =>
      new Response("Payment Required", {
        status: 402,
        headers: { "x402-challenge": header },
      })

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    await assert.rejects(
      () => client.fetch("https://api.example.com/v1/resource"),
      { name: "X402ChallengeError" },
    )
  })

  it("throws DelegatorError when delegator returns error", async () => {
    const challenge = makeChallengeObj()
    const header = encodeChallengeHeader(challenge)

    const mockFetch: typeof globalThis.fetch = async (
      input: RequestInfo | URL,
    ) => {
      const url = typeof input === "string" ? input : input.toString()

      if (url.includes("/delegate/x402")) {
        return new Response(
          JSON.stringify({ code: "insufficient_amount", message: "Not enough" }),
          { status: 402 },
        )
      }

      return new Response("Payment Required", {
        status: 402,
        headers: { "x402-challenge": header },
      })
    }

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    await assert.rejects(
      () => client.fetch("https://api.example.com/v1/resource"),
      { name: "DelegatorError" },
    )
  })

  it("throws BroadcastError when broadcast fails", async () => {
    const challenge = makeChallengeObj()
    const header = encodeChallengeHeader(challenge)

    const delegationResult: DelegationResult = {
      txid: "cd".repeat(32),
      rawtx_hex: "01000000" + "00".repeat(50),
      accepted: true,
    }

    const mockFetch: typeof globalThis.fetch = async (
      input: RequestInfo | URL,
    ) => {
      const url = typeof input === "string" ? input : input.toString()

      if (url.includes("/delegate/x402")) {
        return new Response(JSON.stringify(delegationResult), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
      }

      if (url.includes("whatsonchain.com")) {
        return new Response(
          "Transaction rejected",
          { status: 400 },
        )
      }

      return new Response("Payment Required", {
        status: 402,
        headers: { "x402-challenge": header },
      })
    }

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      fetch: mockFetch,
    })

    await assert.rejects(
      () => client.fetch("https://api.example.com/v1/resource"),
      { name: "BroadcastError" },
    )
  })

  it("merges default headers into requests", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = []

    const mockFetch: typeof globalThis.fetch = async (
      input: RequestInfo | URL,
      init?: RequestInit,
    ) => {
      calls.push({
        url: typeof input === "string" ? input : input.toString(),
        init,
      })
      return new Response("OK", { status: 200 })
    }

    const client = new X402Client({
      delegatorUrl: "https://delegator.example.com",
      defaultHeaders: { "X-Custom": "value" },
      fetch: mockFetch,
    })

    await client.fetch("https://api.example.com/v1/free")

    const sentHeaders = new Headers(calls[0].init?.headers)
    assert.equal(sentHeaders.get("X-Custom"), "value")
  })
})
