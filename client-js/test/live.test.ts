/**
 * Live integration test against the x402 demo gateway.
 *
 * This exercises the full payment flow end-to-end:
 *   GET /v1/expensive → 402 → parse challenge → build partial tx →
 *   delegator completion → WoC broadcast → retry with proof → 200
 *
 * Run:  npx tsx test/live.test.ts
 *
 * Cost: ~100 sats per run (~$0.003) — real mainnet transactions.
 */
import { X402Client } from "../src/client.js"
import { parseChallenge, CHALLENGE_HEADER } from "../src/challenge.js"
import { buildPartialTransaction, isTemplateMode } from "../src/transaction.js"

const DEMO_BASE = "https://demo.x402.merkleworks.io"
const EXPENSIVE_URL = `${DEMO_BASE}/v1/expensive`

// ---------------------------------------------------------------------------
// Helper: coloured output
// ---------------------------------------------------------------------------
function ok(msg: string) {
  console.log(`  \x1b[32m✓\x1b[0m ${msg}`)
}
function warn(msg: string) {
  console.log(`  \x1b[33m⚠\x1b[0m ${msg}`)
}
function fail(msg: string, err?: unknown) {
  console.error(`  \x1b[31m✗\x1b[0m ${msg}`)
  if (err) console.error("   ", err)
}
function heading(msg: string) {
  console.log(`\n\x1b[1m${msg}\x1b[0m`)
}

// ---------------------------------------------------------------------------
// Test 1: Verify the server returns 402 with a valid challenge
// ---------------------------------------------------------------------------
async function testChallengeIssuance() {
  heading("Test 1: Challenge issuance")

  const res = await fetch(EXPENSIVE_URL)

  if (res.status !== 402) {
    fail(`Expected 402, got ${res.status}`)
    return false
  }
  ok(`Server returned 402`)

  const challengeHeader = res.headers.get(CHALLENGE_HEADER)
  if (!challengeHeader) {
    fail("Missing X402-Challenge header")
    return false
  }
  ok("X402-Challenge header present")

  const { challenge, hash } = parseChallenge(challengeHeader)
  ok(`Challenge parsed — amount: ${challenge.amount_sats} sats`)
  ok(`Challenge hash: ${hash}`)
  ok(`Nonce UTXO: ${challenge.nonce_utxo.txid}:${challenge.nonce_utxo.vout}`)
  ok(`Expires at: ${new Date(challenge.expires_at * 1000).toISOString()}`)
  ok(`require_mempool_accept: ${challenge.require_mempool_accept}`)

  if (isTemplateMode(challenge)) {
    ok("Profile B (Gateway Template) — template present")
  } else {
    ok("Profile A (Client-Built) — no template")
  }

  const partialHex = buildPartialTransaction(challenge)
  ok(`Partial tx built: ${partialHex.length} hex chars`)

  return true
}

// ---------------------------------------------------------------------------
// Test 2: Full end-to-end payment flow via X402Client
// ---------------------------------------------------------------------------
async function testFullPaymentFlow() {
  heading("Test 2: Full payment flow (end-to-end)")

  // Wrap fetch to capture the X402-Proof header from the retry request
  let capturedProof: string | null = null
  const capturingFetch: typeof globalThis.fetch = async (input, init) => {
    const headers = new Headers(init?.headers)
    const proof = headers.get("X402-Proof")
    if (proof) capturedProof = proof
    return globalThis.fetch(input, init)
  }

  const client = new X402Client({
    delegatorUrl: DEMO_BASE,
    fetch: capturingFetch,
  })

  try {
    const res = await client.fetch(EXPENSIVE_URL)

    // Extract txid from the proof header the client sent
    if (capturedProof) {
      const proofJson = JSON.parse(Buffer.from(capturedProof, "base64url").toString("utf-8"))
      ok(`Txid: ${proofJson.txid}`)
      ok(`View on chain: https://whatsonchain.com/tx/${proofJson.txid}`)
    }

    if (res.status === 200 || res.status === 204) {
      ok(`Payment succeeded — server returned ${res.status}`)

      const x402Status = res.headers.get("x402-status")
      const x402Receipt = res.headers.get("x402-receipt")
      if (x402Status) ok(`X402-Status: ${x402Status}`)
      if (x402Receipt) ok(`X402-Receipt: ${x402Receipt}`)

      const contentType = res.headers.get("content-type") ?? ""
      if (contentType.includes("json")) {
        const body = await res.json()
        ok(`Response body: ${JSON.stringify(body).slice(0, 120)}`)
      } else {
        const text = await res.text()
        ok(`Response body: ${text.slice(0, 120)}`)
      }
      return true
    } else if (res.status === 202) {
      // 202 = proof accepted but tx not yet in mempool (propagation delay)
      warn(`Payment pending (202) — tx accepted but not yet visible in mempool`)
      const x402Status = res.headers.get("x402-status")
      const x402Receipt = res.headers.get("x402-receipt")
      if (x402Status) warn(`X402-Status: ${x402Status}`)
      if (x402Receipt) ok(`X402-Receipt: ${x402Receipt}`)
      warn("Protocol flow is correct — mempool propagation is in progress")
      return true
    } else {
      fail(`Expected 200/202/204 after payment, got ${res.status}`)
      const body = await res.text().catch(() => "")
      if (body) fail(`Response: ${body.slice(0, 200)}`)
      return false
    }
  } catch (err) {
    fail("Payment flow threw an error", err)
    return false
  }
}

// ---------------------------------------------------------------------------
// Test 3: Verify non-402 passthrough
// ---------------------------------------------------------------------------
async function testPassthrough() {
  heading("Test 3: Non-402 passthrough")

  const client = new X402Client({
    delegatorUrl: DEMO_BASE,
  })

  const res = await client.fetch(`${DEMO_BASE}/health`)

  if (res.status === 200) {
    ok("Health endpoint returned 200 (no payment needed)")
    return true
  } else {
    fail(`Expected 200, got ${res.status}`)
    return false
  }
}

// ---------------------------------------------------------------------------
// Run all tests
// ---------------------------------------------------------------------------
async function main() {
  console.log("═══════════════════════════════════════════════════════")
  console.log("  x402 Client — Live Integration Tests")
  console.log(`  Target: ${DEMO_BASE}`)
  console.log("  Broadcaster: WhatsOnChain (mainnet)")
  console.log("═══════════════════════════════════════════════════════")

  let passed = 0
  let failed = 0

  if (await testChallengeIssuance()) passed++; else failed++
  if (await testPassthrough()) passed++; else failed++
  if (await testFullPaymentFlow()) passed++; else failed++

  heading("Results")
  console.log(`  ${passed} passed, ${failed} failed\n`)

  if (failed > 0) process.exit(1)
}

main().catch((err) => {
  console.error("Fatal error:", err)
  process.exit(1)
})
