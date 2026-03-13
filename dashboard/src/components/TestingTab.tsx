// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useState, useEffect, useRef, useCallback } from 'react'
import {
  testExpensiveEndpoint,
  getConfig,
  decodeChallenge,
  delegateTransaction,
  buildCompleteProof,
  broadcastTransaction,
} from '../api'
import type { ChallengeData, ConfigResponse } from '../types'
import SettlementTimeline, {
  INITIAL_TIMELINE,
  type TimelineState,
  type TimelineStage,
} from './SettlementTimeline'

type StepStatus = 'pending' | 'active' | 'success' | 'error' | 'skipped'

interface FlowStep {
  title: string
  subtitle: string
  status: StepStatus
  detail?: string
}

// Goal 6: Improved step descriptions
const INITIAL_STEPS: FlowStep[] = [
  { title: 'Request Resource', subtitle: 'GET /v1/expensive → expect 402 Payment Required', status: 'pending' },
  { title: 'Decode Challenge', subtitle: 'Extract template & nonce UTXO from X402-Challenge', status: 'pending' },
  { title: 'Call Delegator', subtitle: 'POST /delegate/x402 — delegator adds fee inputs & signs', status: 'pending' },
  { title: 'Broadcast', subtitle: 'Submit delegator-funded transaction to BSV network', status: 'pending' },
  { title: 'Build Proof', subtitle: 'Construct X402-Proof header containing txid + request binding', status: 'pending' },
  { title: 'Retry with Proof', subtitle: 'Gateway verifies mempool settlement before unlocking resource', status: 'pending' },
]

// Settlement details collected during the flow
interface SettlementDetails {
  challengeHash?: string
  nonceUtxo?: string  // txid:vout
  paymentAmount?: number
  txid?: string
  broadcastStatus?: string
  mempoolVisibility?: string
  settlementTime?: string
  delegatorInputs?: number
}

// Parse a raw transaction hex to count the number of inputs (VarInt after 4-byte version).
function countTxInputs(rawTxHex: string): number {
  try {
    // Skip version (4 bytes = 8 hex chars)
    const countByte = parseInt(rawTxHex.slice(8, 10), 16)
    if (countByte < 0xfd) return countByte
    if (countByte === 0xfd) {
      // Next 2 bytes little-endian
      const lo = parseInt(rawTxHex.slice(10, 12), 16)
      const hi = parseInt(rawTxHex.slice(12, 14), 16)
      return (hi << 8) | lo
    }
    return 0
  } catch {
    return 0
  }
}

function formatLatency(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

// WhatsOnChain explorer URL
function wocTxUrl(network: string, txid: string): string {
  const base = network === 'mainnet'
    ? 'https://whatsonchain.com/tx'
    : 'https://test.whatsonchain.com/tx'
  return `${base}/${txid}`
}

export default function TestingTab() {
  const [steps, setSteps] = useState<FlowStep[]>(INITIAL_STEPS)
  const [running, setRunning] = useState(false)
  const [responses, setResponses] = useState<Record<number, object>>({})
  const [config, setConfig] = useState<ConfigResponse | null>(null)

  // Settlement timeline state
  const [timeline, setTimeline] = useState<TimelineState>({ ...INITIAL_TIMELINE })
  const [broadcastToMempoolMs, setBroadcastToMempoolMs] = useState<number | undefined>(undefined)
  const pollingRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const pollCountRef = useRef(0)

  // Goal 1: Settlement details state
  const [settlementDetails, setSettlementDetails] = useState<SettlementDetails>({})

  // Goal 5: Settlement latency (total flow time)
  const [flowStartTime, setFlowStartTime] = useState<number | null>(null)
  const [settlementLatencyMs, setSettlementLatencyMs] = useState<number | null>(null)

  // Goal 8: Track if the flow has ever been run
  const [hasRunOnce, setHasRunOnce] = useState(false)

  useEffect(() => {
    getConfig().then(setConfig).catch(() => {})
    // Cleanup polling on unmount
    return () => {
      if (pollingRef.current) clearTimeout(pollingRef.current)
    }
  }, [])

  function updateStep(idx: number, update: Partial<FlowStep>) {
    setSteps((prev) => prev.map((s, i) => i === idx ? { ...s, ...update } : s))
  }

  function updateTimeline(key: keyof TimelineState, update: Partial<TimelineStage>) {
    setTimeline((prev) => ({
      ...prev,
      [key]: { ...prev[key], ...update },
    }))
  }

  const stopPolling = useCallback(() => {
    if (pollingRef.current) {
      clearTimeout(pollingRef.current)
      pollingRef.current = null
    }
    pollCountRef.current = 0
  }, [])

  async function runFlow() {
    setRunning(true)
    setHasRunOnce(true)
    setResponses({})
    const startTime = Date.now()
    setFlowStartTime(startTime)
    setSettlementLatencyMs(null)
    setSettlementDetails({})
    setSteps(INITIAL_STEPS.map((s, i) => {
      if (i === 3 && config?.broadcaster === 'mock') {
        return { ...s, subtitle: 'Skipped in demo mode (mock broadcaster)' }
      }
      return { ...s }
    }))
    setTimeline({ ...INITIAL_TIMELINE })
    setBroadcastToMempoolMs(undefined)
    stopPolling()

    // Refresh config to get delegator URL
    let cfg = config
    if (!cfg) {
      try {
        cfg = await getConfig()
        setConfig(cfg)
      } catch (err) {
        updateStep(0, { status: 'error', detail: `Failed to load config: ${err}` })
        setRunning(false)
        return
      }
    }

    let challengeEncoded = ''
    let challenge: ChallengeData | null = null
    let completedTxHex = ''
    let txid = ''

    try {
      // ── Step 1: Request resource → 402 + X402-Challenge ──
      updateStep(0, { status: 'active' })
      const step1 = await testExpensiveEndpoint()
      setResponses((prev) => ({ ...prev, 0: step1 }))

      if (step1.status !== 402) {
        updateStep(0, { status: 'error', detail: `Expected 402, got ${step1.status}` })
        updateTimeline('challenge', { status: 'error', timestamp: Date.now(), details: `Expected 402, got ${step1.status}` })
        return
      }

      challengeEncoded = step1.headers['x402-challenge']
      if (!challengeEncoded) {
        updateStep(0, { status: 'error', detail: 'No X402-Challenge header in response' })
        updateTimeline('challenge', { status: 'error', timestamp: Date.now(), details: 'No X402-Challenge header' })
        return
      }
      updateStep(0, { status: 'success', detail: `402 Payment Required` })
      updateTimeline('challenge', {
        status: 'success',
        timestamp: Date.now(),
        details: '402 Payment Required',
        meta: { endpoint: '/v1/expensive', mode: cfg.broadcaster === 'mock' ? 'demo' : 'live' },
      })

      // ── Step 2: Decode challenge (client-side only) ──
      updateStep(1, { status: 'active' })
      challenge = decodeChallenge(challengeEncoded)
      setResponses((prev) => ({ ...prev, 1: challenge as object }))

      if (!challenge.template || !challenge.template.rawtx_hex) {
        updateStep(1, { status: 'error', detail: 'No template in challenge (Profile B required)' })
        updateTimeline('template', { status: 'error', timestamp: Date.now(), details: 'No template (Profile B required)' })
        return
      }

      // Goal 1: Capture settlement details
      const nonceUtxoStr = `${challenge!.nonce_utxo.txid.slice(0, 12)}...:${challenge!.nonce_utxo.vout}`
      setSettlementDetails((prev) => ({
        ...prev,
        nonceUtxo: `${challenge!.nonce_utxo.txid}:${challenge!.nonce_utxo.vout}`,
        paymentAmount: challenge!.amount_sats,
      }))

      updateStep(1, {
        status: 'success',
        detail: `Template: ${challenge.template.rawtx_hex.slice(0, 32)}... (${challenge.amount_sats} sats)`,
      })
      updateTimeline('template', {
        status: 'success',
        timestamp: Date.now(),
        details: `${challenge.amount_sats} sats · nonce ${nonceUtxoStr}`,
        meta: { price: `${challenge.amount_sats} sats`, nonce: nonceUtxoStr },
      })

      // ── Step 3: Call delegator → completed_tx ──
      updateStep(2, { status: 'active' })
      const delegatorUrl = cfg.delegatorUrl
      const delegResult = await delegateTransaction(delegatorUrl, challenge.template.rawtx_hex)
      completedTxHex = delegResult.completed_tx
      txid = delegResult.txid
      setResponses((prev) => ({ ...prev, 2: delegResult }))

      // Capture txid and delegator funding input count (UI-derived from raw tx hex)
      const totalInputs = countTxInputs(completedTxHex)
      const delegatorInputs = Math.max(totalInputs - 1, 0) // input[0] = nonce, rest = delegator
      setSettlementDetails((prev) => ({ ...prev, txid, delegatorInputs }))

      updateStep(2, { status: 'success', detail: `txid: ${txid}` })
      updateTimeline('delegator', {
        status: 'success',
        timestamp: Date.now(),
        details: `txid: ${txid.slice(0, 12)}...`,
        meta: { delegatorUrl, fee_inputs: String(delegatorInputs) },
      })

      // ── Step 4: Broadcast ──
      updateStep(3, { status: 'active' })
      const broadcastTime = Date.now()
      if (cfg.broadcaster === 'mock') {
        setResponses((prev) => ({ ...prev, 3: { skipped: true, reason: 'Demo mode — mock broadcaster, tx not sent to BSV network' } }))
        updateStep(3, { status: 'skipped', detail: 'Broadcast step skipped (mock broadcaster)' })
        setSettlementDetails((prev) => ({
          ...prev,
          broadcastStatus: 'Skipped (demo mode)',
          mempoolVisibility: 'Mock — always visible',
        }))
        updateTimeline('broadcast', {
          status: 'success',
          timestamp: broadcastTime,
          details: 'Demo mode (mock)',
          meta: { mode: 'mock', note: 'Tx accepted without broadcast' },
        })
      } else {
        // Live mode — broadcast via gateway proxy (avoids CORS issues with WoC)
        try {
          const broadcastResult = await broadcastTransaction('/api/v1/broadcast', completedTxHex)
          setResponses((prev) => ({ ...prev, 3: broadcastResult }))
          updateStep(3, { status: 'success', detail: `Broadcast OK: ${broadcastResult.txid}` })
          setSettlementDetails((prev) => ({
            ...prev,
            broadcastStatus: 'Broadcast to BSV network',
            mempoolVisibility: 'Pending — polling...',
          }))
          updateTimeline('broadcast', {
            status: 'success',
            timestamp: broadcastTime,
            details: `txid: ${broadcastResult.txid.slice(0, 12)}...`,
            meta: { mode: 'woc', txid: broadcastResult.txid },
          })
        } catch (err) {
          // Broadcast failed — continue the flow instead of stopping.
          // The proof submission (Step 6) will likely return 202 (pending)
          // since the tx isn't in mempool, but we let the user see the full flow.
          const errMsg = err instanceof Error ? err.message : String(err)
          setResponses((prev) => ({ ...prev, 3: { error: errMsg } }))
          updateStep(3, { status: 'error', detail: `Broadcast failed: ${errMsg}. Continuing flow...` })
          setSettlementDetails((prev) => ({
            ...prev,
            broadcastStatus: 'Broadcast failed (continuing)',
            mempoolVisibility: 'Unlikely — tx not broadcast',
          }))
          updateTimeline('broadcast', {
            status: 'error',
            timestamp: broadcastTime,
            details: errMsg,
            meta: { mode: 'woc', note: 'Continuing to proof step despite broadcast failure' },
          })
        }
      }

      // ── Step 5: Build X402-Proof header (client-side only) ──
      updateStep(4, { status: 'active' })
      const proofHeader = await buildCompleteProof(completedTxHex, txid, challengeEncoded, challenge)
      setResponses((prev) => ({ ...prev, 4: { proof_header: proofHeader.slice(0, 80) + '...' } }))

      // Compute and store challenge hash for settlement details
      const challengeHashHex = extractChallengeHash(proofHeader)
      setSettlementDetails((prev) => ({ ...prev, challengeHash: challengeHashHex }))

      updateStep(4, { status: 'success', detail: `Proof: ${proofHeader.slice(0, 40)}...` })

      // ── Step 6: Retry with proof → 200 OK or 202 payment_pending ──
      updateStep(5, { status: 'active' })
      const step6 = await testExpensiveEndpoint(proofHeader)
      setResponses((prev) => ({ ...prev, 5: step6 }))

      if (step6.status === 200) {
        // Immediate success — mempool confirmed
        const now = Date.now()
        const totalLatency = now - startTime
        updateStep(5, { status: 'success', detail: '200 OK - Payment accepted!' })
        // UI-derived verification method based on broadcaster config
        const verifyLabel = cfg.broadcaster === 'mock' ? 'immediate (demo)' : 'mempool'
        updateTimeline('mempool', { status: 'success', timestamp: now, details: 'Confirmed', meta: { verification: verifyLabel } })
        updateTimeline('settlement', { status: 'success', timestamp: now, details: 'Transaction settled', meta: { latencyMs: String(totalLatency) } })
        updateTimeline('unlock', { status: 'success', timestamp: now, details: '200 OK', meta: { endpoint: '/v1/expensive' } })
        setBroadcastToMempoolMs(now - broadcastTime)
        setSettlementLatencyMs(totalLatency)
        setSettlementDetails((prev) => ({
          ...prev,
          mempoolVisibility: 'Confirmed',
          settlementTime: formatLatency(totalLatency),
        }))
      } else if (step6.status === 202) {
        // Payment pending — start polling
        updateStep(5, { status: 'active', detail: '202 Payment Pending — polling for mempool...' })
        updateTimeline('mempool', { status: 'pending', timestamp: Date.now(), details: 'Waiting for mempool...', meta: { polling: 'every 2s' } })
        setSettlementDetails((prev) => ({ ...prev, mempoolVisibility: 'Pending — polling...' }))

        // Progressive backoff polling: 2s for first 30s, then 10s up to 5min total.
        // Low-fee transactions (1 sat/KB) can take time to propagate into the mempool.
        const TIMEOUT_MS = 5 * 60 * 1000 // 5 minutes
        const FAST_INTERVAL = 2000       // 2s for first phase
        const SLOW_INTERVAL = 10000      // 10s after first 30s
        const FAST_PHASE_MS = 30000      // first 30s is fast polling
        const pollProof = proofHeader
        const pollStartTime = Date.now()
        pollCountRef.current = 0

        const schedulePoll = () => {
          const elapsed = Date.now() - pollStartTime
          const interval = elapsed < FAST_PHASE_MS ? FAST_INTERVAL : SLOW_INTERVAL
          pollingRef.current = setTimeout(doPoll, interval)
        }

        const doPoll = async () => {
          pollCountRef.current++
          const elapsed = Date.now() - pollStartTime
          try {
            // Timeout: stop after 5 minutes
            if (elapsed > TIMEOUT_MS) {
              stopPolling()
              const mins = Math.round(elapsed / 60000)
              updateStep(5, { status: 'error', detail: `Mempool poll timeout (${mins}min) — transaction may not have been accepted` })
              updateTimeline('mempool', { status: 'error', timestamp: Date.now(), details: `Poll timeout (${mins}min)`, meta: { polls: String(pollCountRef.current) } })
              setSettlementDetails((prev) => ({ ...prev, mempoolVisibility: 'Timeout — not detected' }))
              setRunning(false)
              return
            }

            // Update step detail with elapsed time
            const elapsedSec = Math.round(elapsed / 1000)
            updateStep(5, { status: 'active', detail: `202 Payment Pending — polling for mempool... (${elapsedSec}s)` })

            const retry = await testExpensiveEndpoint(pollProof)
            setResponses((prev) => ({ ...prev, 5: retry }))

            if (retry.status === 200) {
              const now = Date.now()
              const totalLatency = now - startTime
              stopPolling()
              updateStep(5, { status: 'success', detail: '200 OK - Payment accepted!' })
              const verifyLabelPolled = cfg.broadcaster === 'mock' ? 'immediate (demo)' : 'mempool'
              updateTimeline('mempool', { status: 'success', timestamp: now, details: 'Confirmed in mempool', meta: { verification: verifyLabelPolled } })
              updateTimeline('settlement', { status: 'success', timestamp: now, details: 'Transaction settled', meta: { latencyMs: String(totalLatency) } })
              updateTimeline('unlock', { status: 'success', timestamp: now, details: '200 OK', meta: { endpoint: '/v1/expensive' } })
              setBroadcastToMempoolMs(now - broadcastTime)
              setSettlementLatencyMs(totalLatency)
              setSettlementDetails((prev) => ({
                ...prev,
                mempoolVisibility: 'Confirmed (polled)',
                settlementTime: formatLatency(totalLatency),
              }))
              setRunning(false)
            } else if (retry.status !== 202) {
              // Unexpected status — stop polling
              stopPolling()
              updateStep(5, { status: 'error', detail: `Unexpected status: ${retry.status}` })
              updateTimeline('mempool', { status: 'error', timestamp: Date.now(), details: `Status ${retry.status}` })
              setRunning(false)
            } else {
              // 202 → schedule next poll (progressive backoff)
              schedulePoll()
            }
          } catch (pollErr) {
            stopPolling()
            const msg = pollErr instanceof Error ? pollErr.message : String(pollErr)
            updateStep(5, { status: 'error', detail: `Polling error: ${msg}` })
            updateTimeline('mempool', { status: 'error', timestamp: Date.now(), details: msg })
            setRunning(false)
          }
        }

        // Start first poll
        schedulePoll()
        return // don't set running=false yet — polling continues
      } else {
        updateStep(5, { status: 'error', detail: `Expected 200, got ${step6.status}` })
        updateTimeline('mempool', { status: 'error', timestamp: Date.now(), details: `Status ${step6.status}` })
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setSteps((prev) => prev.map((s) => s.status === 'active' ? { ...s, status: 'error', detail: msg } : s))
    } finally {
      setRunning(false)
    }
  }

  // Extract challenge_sha256 from the base64url-encoded proof header
  function extractChallengeHash(proofHeader: string): string {
    try {
      let b64 = proofHeader.replace(/-/g, '+').replace(/_/g, '/')
      while (b64.length % 4) b64 += '='
      const json = atob(b64)
      const proof = JSON.parse(json)
      return proof.challenge_sha256 || ''
    } catch {
      return ''
    }
  }

  // ── Architecture diagram ──
  const architectureDiagram = `
Client (Dashboard)
  1. GET /v1/expensive          -> Gateway (port ${config?.port ?? 8402})
  <- 402 + X402-Challenge
  2. Decode challenge (local)
  3. POST /delegate/x402        -> Delegator (${config?.delegatorUrl ?? 'localhost:8403'})
  <- { completed_tx }
  4. Broadcast completed_tx     -> BSV Network
  5. Build X402-Proof (local)
  6. GET /v1/expensive + Proof  -> Gateway
  <- 200 OK
`.trim()

  // Custom request builder state
  const [customMethod, setCustomMethod] = useState('GET')
  const [customPath, setCustomPath] = useState('/v1/expensive')
  const [customHeaders, setCustomHeaders] = useState('')
  const [customBody, setCustomBody] = useState('')
  const [customResponse, setCustomResponse] = useState<object | null>(null)
  const [customRunning, setCustomRunning] = useState(false)

  async function runCustom() {
    setCustomRunning(true)
    setCustomResponse(null)
    try {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' }
      if (customHeaders.trim()) {
        for (const line of customHeaders.split('\n')) {
          const idx = line.indexOf(':')
          if (idx > 0) {
            headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
          }
        }
      }
      const res = await fetch(customPath, {
        method: customMethod,
        headers,
        body: customMethod !== 'GET' && customBody ? customBody : undefined,
      })
      const body = await res.json().catch(() => res.text())
      const respHeaders: Record<string, string> = {}
      res.headers.forEach((v, k) => { respHeaders[k] = v })
      setCustomResponse({ status: res.status, headers: respHeaders, body })
    } catch (err) {
      setCustomResponse({ error: err instanceof Error ? err.message : String(err) })
    } finally {
      setCustomRunning(false)
    }
  }

  const isDemo = config?.broadcaster === 'mock'
  const allSuccess = steps.every((s) => s.status === 'success' || s.status === 'skipped')

  return (
    <div>
      <div className="tab-header">
        <h2 className="tab-title">Testing</h2>
      </div>

      {/* Broadcaster mode banner */}
      {config && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '10px 14px',
          marginBottom: 16,
          borderRadius: 'var(--radius)',
          border: `1px solid ${isDemo ? 'var(--accent-yellow)' : 'var(--accent-green)'}`,
          background: isDemo
            ? 'rgba(210, 153, 34, 0.08)'
            : 'rgba(35, 134, 54, 0.08)',
        }}>
          <span style={{
            display: 'inline-block',
            padding: '2px 8px',
            borderRadius: 4,
            fontSize: 11,
            fontWeight: 700,
            letterSpacing: '0.5px',
            textTransform: 'uppercase',
            color: '#fff',
            background: isDemo ? 'var(--accent-yellow)' : 'var(--accent-green)',
          }}>
            {isDemo ? 'DEMO' : 'LIVE'}
          </span>
          <span style={{ fontSize: 13, color: 'var(--text-primary)' }}>
            {isDemo
              ? 'Mock Broadcaster — transactions are not broadcast to the BSV network.'
              : config?.broadcaster === 'composite'
                ? 'Composite — GorillaPool ARC primary, WoC fallback. Transactions are broadcast to the BSV network.'
                : 'Live Network — transactions are broadcast to the BSV network and verified via mempool detection.'}
          </span>
        </div>
      )}

      {/* x402 Protocol Flow Tester */}
      <div className="card">
        <div className="card-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span className="card-title">x402 Settlement Flow (6-Step Protocol)</span>
            {/* Goal 7: Protocol diagram tooltip */}
            <span
              title="x402 is a client-orchestrated BSV micropayment protocol. The gateway issues a challenge (402), the client extracts a pre-signed template, sends it to a delegator for fee funding, broadcasts the completed transaction, then retries with cryptographic proof of settlement."
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                width: 18,
                height: 18,
                borderRadius: '50%',
                background: 'var(--bg-tertiary)',
                border: '1px solid var(--border)',
                color: 'var(--accent-blue)',
                fontSize: 11,
                fontWeight: 700,
                cursor: 'help',
                flexShrink: 0,
              }}
            >
              ?
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            {/* Goal 5: Settlement latency badge */}
            {settlementLatencyMs != null && (
              <span style={{
                display: 'inline-flex',
                alignItems: 'center',
                gap: 5,
                padding: '3px 10px',
                borderRadius: 10,
                fontSize: 12,
                fontWeight: 600,
                fontFamily: 'var(--font-mono)',
                color: settlementLatencyMs < 2000 ? 'var(--accent-green-text)' : settlementLatencyMs < 5000 ? 'var(--accent-yellow)' : 'var(--accent-red-text)',
                background: settlementLatencyMs < 2000 ? 'rgba(35, 134, 54, 0.12)' : settlementLatencyMs < 5000 ? 'rgba(210, 153, 34, 0.12)' : 'rgba(218, 54, 51, 0.12)',
                border: `1px solid ${settlementLatencyMs < 2000 ? 'rgba(35, 134, 54, 0.3)' : settlementLatencyMs < 5000 ? 'rgba(210, 153, 34, 0.3)' : 'rgba(218, 54, 51, 0.3)'}`,
              }}>
                ⏱ {formatLatency(settlementLatencyMs)}
              </span>
            )}
            <button className="btn btn-primary" onClick={runFlow} disabled={running}>
              {running ? <><span className="spinner" /> Running...</> : 'Run Full Flow'}
            </button>
          </div>
        </div>

        {/* Architecture overview */}
        <details style={{ marginBottom: 12 }}>
          <summary style={{ fontSize: 12, color: 'var(--accent-blue)', cursor: 'pointer' }}>
            Architecture: Client-Orchestrated Flow
          </summary>
          <pre className="json-view" style={{ marginTop: 4, fontSize: 11 }}>
            {architectureDiagram}
          </pre>
        </details>

        {/* Goal 8: Empty state hint before first run */}
        {!hasRunOnce && !running && (
          <div style={{
            padding: '20px 16px',
            marginBottom: 16,
            borderRadius: 'var(--radius)',
            border: '1px dashed var(--border)',
            background: 'var(--bg-primary)',
            textAlign: 'center',
          }}>
            <div style={{ fontSize: 24, marginBottom: 8 }}>⚡</div>
            <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-primary)', marginBottom: 4 }}>
              Ready to Test
            </div>
            <div style={{ fontSize: 13, color: 'var(--text-secondary)', maxWidth: 480, margin: '0 auto' }}>
              Run the flow to simulate a full x402 settlement lifecycle. The dashboard will walk through all 6 protocol steps — from challenge issuance to resource unlock — showing real-time progress and settlement details.
            </div>
          </div>
        )}

        {/* Two-column layout: steps + timeline */}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 280px', gap: 20 }}>
          {/* Flow steps */}
          <div className="flow-steps">
            {steps.map((step, i) => (
              <div key={i} className={`flow-step ${step.status}`}>
                <div className="step-number">
                  {/* Goal 3: Spinner on active steps */}
                  {step.status === 'active' ? (
                    <span className="spinner" style={{ width: 14, height: 14, borderWidth: 2 }} />
                  ) : (
                    i + 1
                  )}
                </div>
                <div className="step-content">
                  <div className="step-title">
                    {step.title}
                    {/* Goal 3: Processing text */}
                    {step.status === 'active' && (
                      <span style={{ fontSize: 11, fontWeight: 400, color: 'var(--accent-blue)', marginLeft: 8 }}>
                        Processing...
                      </span>
                    )}
                  </div>
                  <div style={{ fontSize: 11, color: 'var(--text-secondary)', marginBottom: 2 }}>
                    {step.subtitle}
                  </div>
                  {step.detail && (
                    <div className="step-detail">
                      {/* For steps with txid (delegator=2, broadcast=3), make the txid a clickable WoC link */}
                      {(i === 2 || i === 3) && settlementDetails.txid && !isDemo && config?.network ? (
                        <>
                          <div>{step.detail.replace(settlementDetails.txid, '').replace(/txid:\s*/, 'txid:').replace(/Broadcast OK:\s*/, 'Broadcast OK:')}</div>
                          <div style={{ marginTop: 2 }}>
                            <a
                              href={wocTxUrl(config.network, settlementDetails.txid)}
                              target="_blank"
                              rel="noopener noreferrer"
                              style={{ color: 'var(--accent-blue)', textDecoration: 'none' }}
                            >
                              {settlementDetails.txid} ↗
                            </a>
                          </div>
                        </>
                      ) : step.detail}
                    </div>
                  )}
                  {responses[i] && (
                    <details style={{ marginTop: 8 }}>
                      <summary style={{ fontSize: 12, color: 'var(--accent-blue)', cursor: 'pointer' }}>
                        View Response
                      </summary>
                      <div className="json-view" style={{ marginTop: 4 }}>
                        {JSON.stringify(responses[i], null, 2)}
                      </div>
                    </details>
                  )}
                </div>
              </div>
            ))}
          </div>

          {/* Settlement Timeline */}
          <div style={{
            background: 'var(--bg-primary)',
            borderRadius: 'var(--radius)',
            border: '1px solid var(--border)',
            padding: 16,
            alignSelf: 'start',
          }}>
            <div style={{
              fontSize: 12,
              fontWeight: 600,
              color: 'var(--text-secondary)',
              textTransform: 'uppercase',
              letterSpacing: '0.5px',
              marginBottom: 16,
            }}>
              Settlement Timeline
            </div>
            <SettlementTimeline
              timeline={timeline}
              broadcastToMempoolMs={broadcastToMempoolMs}
            />
          </div>
        </div>
      </div>

      {/* Goal 1: Settlement Details panel */}
      {hasRunOnce && (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Settlement Details</span>
            {allSuccess && (
              <span style={{
                fontSize: 11,
                fontWeight: 600,
                padding: '2px 8px',
                borderRadius: 4,
                color: 'var(--accent-green-text)',
                background: 'rgba(35, 134, 54, 0.12)',
                border: '1px solid rgba(35, 134, 54, 0.3)',
                textTransform: 'uppercase',
                letterSpacing: '0.3px',
              }}>
                Settled
              </span>
            )}
          </div>

          <div style={{
            display: 'grid',
            gridTemplateColumns: '1fr 1fr',
            gap: '1px',
            background: 'var(--border)',
            borderRadius: 'var(--radius)',
            overflow: 'hidden',
            border: '1px solid var(--border)',
          }}>
            {/* Challenge Hash */}
            <DetailCell
              label="Challenge Hash"
              value={settlementDetails.challengeHash || '—'}
              mono
            />

            {/* Nonce UTXO */}
            <DetailCell
              label="Nonce UTXO"
              value={settlementDetails.nonceUtxo || '—'}
              mono
            />

            {/* Payment Amount */}
            <DetailCell
              label="Payment Amount"
              value={settlementDetails.paymentAmount != null
                ? `${settlementDetails.paymentAmount} sats`
                : '—'}
            />

            {/* Transaction ID */}
            <DetailCell
              label="Transaction ID"
              value={settlementDetails.txid || '—'}
              mono
              link={settlementDetails.txid && !isDemo && config?.network
                ? wocTxUrl(config.network, settlementDetails.txid)
                : undefined}
            />

            {/* Broadcast Status */}
            <DetailCell
              label="Broadcast Status"
              value={settlementDetails.broadcastStatus || '—'}
              color={settlementDetails.broadcastStatus?.includes('Skipped')
                ? 'var(--accent-yellow)'
                : 'var(--accent-green-text)'}
            />

            {/* Mempool Visibility */}
            <DetailCell
              label="Mempool Visibility"
              value={settlementDetails.mempoolVisibility || '—'}
              color={settlementDetails.mempoolVisibility?.includes('Confirmed')
                ? 'var(--accent-green-text)'
                : settlementDetails.mempoolVisibility?.includes('Pending')
                  ? 'var(--accent-yellow)'
                  : undefined}
            />

            {/* Settlement Time */}
            <DetailCell
              label="Settlement Time"
              value={settlementDetails.settlementTime || (running ? 'In progress...' : '—')}
              color={settlementLatencyMs != null
                ? settlementLatencyMs < 2000 ? 'var(--accent-green-text)' : settlementLatencyMs < 5000 ? 'var(--accent-yellow)' : 'var(--accent-red-text)'
                : undefined}
            />

            {/* Mode */}
            <DetailCell
              label="Broadcaster Mode"
              value={config?.broadcaster === 'mock' ? 'Demo (mock)' : config?.broadcaster === 'composite' ? 'Composite (GorillaPool + WoC)' : config?.broadcaster === 'woc' ? 'Live (WoC)' : config?.broadcaster || '—'}
              color={isDemo ? 'var(--accent-yellow)' : 'var(--accent-green-text)'}
            />

            {/* Settlement Method — UI-derived from config.broadcaster */}
            <DetailCell
              label="Settlement Method"
              value={config?.broadcaster === 'mock' ? 'mock broadcaster' : 'mempool (0-conf)'}
              color={isDemo ? 'var(--accent-yellow)' : 'var(--accent-green-text)'}
            />

            {/* Verification Method — UI-derived from config.broadcaster */}
            <DetailCell
              label="Verification Method"
              value={config?.broadcaster === 'mock' ? 'immediate (demo)' : 'mempool'}
              color={isDemo ? 'var(--accent-yellow)' : 'var(--accent-green-text)'}
            />

            {/* Delegator Funding Inputs — parsed client-side from raw tx hex */}
            <DetailCell
              label="Delegator Funding Inputs"
              value={settlementDetails.delegatorInputs != null
                ? String(settlementDetails.delegatorInputs)
                : '—'}
              mono
            />
          </div>
        </div>
      )}

      {/* Custom Request Builder */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Custom Request</span>
        </div>

        <div className="grid grid-2">
          <div className="form-group">
            <label className="form-label">Method</label>
            <select className="form-input" value={customMethod} onChange={(e) => setCustomMethod(e.target.value)}>
              <option>GET</option>
              <option>POST</option>
              <option>PUT</option>
              <option>DELETE</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label">Path</label>
            <input className="form-input" value={customPath} onChange={(e) => setCustomPath(e.target.value)} />
          </div>
        </div>
        <div className="form-group">
          <label className="form-label">Headers (one per line, key: value)</label>
          <textarea
            className="form-input"
            rows={3}
            value={customHeaders}
            onChange={(e) => setCustomHeaders(e.target.value)}
            placeholder="X-Custom-Header: value"
            style={{ fontFamily: 'var(--font-mono)', resize: 'vertical' }}
          />
        </div>
        {customMethod !== 'GET' && (
          <div className="form-group">
            <label className="form-label">Body (JSON)</label>
            <textarea
              className="form-input"
              rows={4}
              value={customBody}
              onChange={(e) => setCustomBody(e.target.value)}
              placeholder='{"key": "value"}'
              style={{ fontFamily: 'var(--font-mono)', resize: 'vertical' }}
            />
          </div>
        )}
        <button className="btn btn-primary" onClick={runCustom} disabled={customRunning}>
          {customRunning ? <span className="spinner" /> : null}
          Send Request
        </button>

        {customResponse && (
          <div className="json-view" style={{ marginTop: 12 }}>
            {JSON.stringify(customResponse, null, 2)}
          </div>
        )}
      </div>
    </div>
  )
}

// ── Settlement Detail Cell component ──
function DetailCell({ label, value, mono, color, link }: {
  label: string
  value: string
  mono?: boolean
  color?: string
  link?: string
}) {
  return (
    <div style={{ padding: '10px 14px', background: 'var(--bg-primary)' }}>
      <div style={{
        fontSize: 11,
        color: 'var(--text-muted)',
        textTransform: 'uppercase',
        letterSpacing: '0.3px',
        marginBottom: 4,
      }}>
        {label}
      </div>
      <div style={{
        fontSize: 13,
        fontWeight: 600,
        fontFamily: mono ? 'var(--font-mono)' : undefined,
        color: color || 'var(--text-primary)',
        wordBreak: 'break-all',
      }}>
        {link ? (
          <a
            href={link}
            target="_blank"
            rel="noopener noreferrer"
            style={{ color: 'var(--accent-blue)', textDecoration: 'none' }}
          >
            {value} ↗
          </a>
        ) : value}
      </div>
    </div>
  )
}
