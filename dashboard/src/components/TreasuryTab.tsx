// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useState, useCallback } from 'react'
import { getTreasuryInfo, triggerFanout, getFanoutHistory, getTreasuryUTXOs, getConfig, sweepRevenue, reconcilePools, getRevenue } from '../api'
import { useApi } from '../hooks/useApi'
import type { TreasuryUTXO, ConfigResponse } from '../types'
import PoolStats from './PoolStats'

export default function TreasuryTab() {
  const infoFetcher = useCallback(() => getTreasuryInfo(), [])
  const historyFetcher = useCallback(() => getFanoutHistory(), [])
  const utxoFetcher = useCallback(() => getTreasuryUTXOs(), [])
  const configFetcher = useCallback(() => getConfig(), [])
  const revenueFetcher = useCallback(() => getRevenue(), [])
  const { data: info, refresh: refreshInfo } = useApi(infoFetcher, 10000)
  const { data: historyData, refresh: refreshHistory } = useApi(historyFetcher, 10000)
  const { data: utxoData, refresh: refreshUTXOs } = useApi(utxoFetcher, 10000)
  const { data: config } = useApi(configFetcher, 30000)
  const { data: revenue, refresh: refreshRevenue } = useApi(revenueFetcher, 5000)

  const [pool, setPool] = useState('fee')
  const [count, setCount] = useState('100')
  const [fundingTxid, setFundingTxid] = useState('')
  const [fundingVout, setFundingVout] = useState('0')
  const [fundingScript, setFundingScript] = useState('')
  const [fundingSatoshis, setFundingSatoshis] = useState('')
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [inputMode, setInputMode] = useState<'select' | 'manual'>('select')
  const [sweeping, setSweeping] = useState(false)
  const [sweepResult, setSweepResult] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [reconciling, setReconciling] = useState(false)
  const [reconcileResult, setReconcileResult] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  async function handleReconcile() {
    setReconciling(true)
    setReconcileResult(null)
    try {
      const res = await reconcilePools()
      const details = res.pools.map(p =>
        p.error ? `${p.pool}: error (${p.error})` : `${p.pool}: ${p.valid} valid, ${p.zombies_removed} zombies removed | ${p.leased_confirmed} leased confirmed | ${p.stale_released} stale released | ${p.skipped} skipped (unconfirmed)`
      ).join(' | ')
      const totalActions = res.total_zombies + res.total_confirmed + res.total_stale_released
      if (totalActions > 0) {
        setReconcileResult({ type: 'success', text: `Reconciled: ${res.total_zombies} zombies removed, ${res.total_confirmed} leased confirmed, ${res.total_stale_released} stale released. ${details}` })
      } else {
        setReconcileResult({ type: 'success', text: `All pools clean — no actions needed. ${details}` })
      }
      refreshInfo()
    } catch (err) {
      setReconcileResult({ type: 'error', text: err instanceof Error ? err.message : String(err) })
    } finally {
      setReconciling(false)
    }
  }

  async function handleSweepRevenue() {
    setSweeping(true)
    setSweepResult(null)
    try {
      const res = await sweepRevenue()
      setSweepResult({ type: 'success', text: `Swept ${res.inputCount} UTXOs (${res.inputSats} sats) to treasury (txid: ${res.txid})` })
      refreshInfo()
      refreshUTXOs()
      refreshRevenue()
    } catch (err) {
      setSweepResult({ type: 'error', text: err instanceof Error ? err.message : String(err) })
    } finally {
      setSweeping(false)
    }
  }

  function selectUTXO(utxo: TreasuryUTXO) {
    setFundingTxid(utxo.txid)
    setFundingVout(String(utxo.vout))
    setFundingScript(utxo.script)
    setFundingSatoshis(String(utxo.satoshis))
  }

  async function handleFanout() {
    setRunning(true)
    setResult(null)
    try {
      const res = await triggerFanout({
        pool,
        count: parseInt(count),
        fundingTxid,
        fundingVout: parseInt(fundingVout),
        fundingScript,
        fundingSatoshis: parseInt(fundingSatoshis),
      })
      setResult({ type: 'success', text: `Fan-out complete: ${res.utxoCount} UTXOs created (txid: ${res.txid})` })
      refreshInfo()
      refreshHistory()
      refreshUTXOs()
      // Clear selection after successful fan-out
      setFundingTxid('')
      setFundingVout('0')
      setFundingScript('')
      setFundingSatoshis('')
    } catch (err) {
      setResult({ type: 'error', text: err instanceof Error ? err.message : String(err) })
    } finally {
      setRunning(false)
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text)
  }

  function formatSats(sats: number): string {
    if (sats >= 100_000_000) return `${(sats / 100_000_000).toFixed(8)} BSV`
    if (sats >= 100_000) return `${(sats / 100_000).toFixed(0)}k sats`
    return `${sats.toLocaleString()} sats`
  }

  function wocBase(): string {
    return info?.network === 'testnet'
      ? 'https://test.whatsonchain.com'
      : 'https://whatsonchain.com'
  }

  function txUrl(txid: string): string {
    return `${wocBase()}/tx/${txid}`
  }

  function addressUrl(addr: string): string {
    return `${wocBase()}/address/${addr}`
  }

  if (!info) return <div className="spinner" />

  const demoMode = info.broadcaster === 'mock'
  const utxos = utxoData?.utxos ?? []
  const hasUTXOs = utxos.length > 0

  return (
    <div>
      <div className="tab-header">
        <h2 className="tab-title">Treasury</h2>
      </div>

      {/* Treasury address */}
      <div className="card">
        <div className="card-header">
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span className="card-title">Treasury Address</span>
            {demoMode && (
              <span
                style={{
                  fontSize: 10,
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                  padding: '2px 6px',
                  borderRadius: 4,
                  background: 'rgba(245, 158, 11, 0.15)',
                  color: '#f59e0b',
                  border: '1px solid rgba(245, 158, 11, 0.3)',
                }}
                title="Transactions are not broadcast to the network. Switch broadcaster in Settings."
              >
                Demo Mode
              </span>
            )}
          </div>
          <span className="card-subtitle">{info.network} | {info.keyMode === 'xpriv' ? info.derivationPath : 'single key'}</span>
        </div>
        <div style={{ marginBottom: 12, display: 'flex', alignItems: 'center', gap: 10 }}>
          <a
            href={addressUrl(info.address)}
            target="_blank"
            rel="noopener noreferrer"
            className="addr"
            style={{ fontSize: 15, color: 'var(--accent-blue)', textDecoration: 'none' }}
            title="View on WhatsonChain"
          >
            {info.address}
          </a>
          <button
            className="btn btn-sm"
            style={{ fontSize: 11, padding: '2px 8px' }}
            onClick={() => copyToClipboard(info.address)}
            title="Copy to clipboard"
          >
            Copy
          </button>
        </div>
        <div className="alert alert-info">
          Fund this address to create new UTXOs via fan-out. Each fan-out splits one funding UTXO into many small UTXOs for pool replenishment.
        </div>
      </div>

      {/* Settlement capacity summary */}
      {info.noncePool && info.paymentPool && (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Settlement Capacity</span>
          </div>
          <div style={{
            display: 'grid',
            gridTemplateColumns: '1fr 1fr 1fr',
            gap: 16,
          }}>
            <div style={{
              padding: '12px 14px',
              background: 'var(--bg-primary)',
              borderRadius: 'var(--radius)',
              border: '1px solid var(--border)',
            }}>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 4 }}>
                Requests Possible
              </div>
              <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-blue)' }}>
                {info.noncePool.available.toLocaleString()}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>
                nonce UTXOs available
              </div>
            </div>
            <div style={{
              padding: '12px 14px',
              background: 'var(--bg-primary)',
              borderRadius: 'var(--radius)',
              border: '1px solid var(--border)',
            }}>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 4 }}>
                Price per Payment
              </div>
              <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--text-primary)' }}>
                {formatSats(config?.templatePriceSats || info.paymentPool.utxo_value || 0)}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>
                per settlement
              </div>
            </div>
            <div style={{
              padding: '12px 14px',
              background: 'var(--bg-primary)',
              borderRadius: 'var(--radius)',
              border: '1px solid var(--border)',
            }}>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 4 }}>
                Total Revenue
              </div>
              <div style={{ fontSize: 22, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-green-text)' }}>
                {formatSats(revenue?.totalSats ?? 0)}
              </div>
              <div style={{ fontSize: 11, color: 'var(--text-muted)', marginTop: 2 }}>
                {revenue?.payments ?? 0} payments settled
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Pool status */}
      <div className="grid grid-3">
        {info.noncePool && <PoolStats label="Nonce" stats={info.noncePool} />}
        {info.feePool && <PoolStats label="Fee" stats={info.feePool} configuredUtxoValue={config?.feeUTXOSats} />}
        <div className="card">
            <div className="card-header">
              <span className="card-title">Revenue</span>
              <span className="card-subtitle">incoming payments</span>
            </div>
            <div style={{
              display: 'grid',
              gridTemplateColumns: '1fr 1fr',
              gap: 8,
              marginBottom: 12,
              padding: '10px 12px',
              background: 'var(--bg-primary)',
              borderRadius: 'var(--radius)',
              border: '1px solid var(--border)',
            }}>
              <div>
                <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px' }}>
                  Payments
                </div>
                <div style={{ fontSize: 16, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-blue)', marginTop: 2 }}>
                  {revenue?.payments ?? 0}
                </div>
              </div>
              <div>
                <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px' }}>
                  Total Revenue
                </div>
                <div style={{ fontSize: 16, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-green-text)', marginTop: 2 }}>
                  {formatSats(revenue?.totalSats ?? 0)}
                </div>
              </div>
            </div>
            {(revenue?.unsweptCount ?? 0) > 0 && (
              <div style={{
                fontSize: 12,
                color: 'var(--text-secondary)',
                marginBottom: 8,
                padding: '6px 10px',
                background: 'rgba(16, 185, 129, 0.06)',
                borderRadius: 'var(--radius)',
                border: '1px solid rgba(16, 185, 129, 0.15)',
              }}>
                <strong>{revenue?.unsweptCount}</strong> UTXOs ({formatSats(revenue?.unsweptSats ?? 0)}) ready to sweep
              </div>
            )}
            {sweepResult && <div className={`alert alert-${sweepResult.type}`} style={{ marginBottom: 8 }}>{sweepResult.text}</div>}
            <button
              className="btn btn-primary"
              style={{ width: '100%', fontSize: 12 }}
              onClick={handleSweepRevenue}
              disabled={sweeping || (revenue?.unsweptCount ?? 0) === 0}
            >
              {sweeping ? <span className="spinner" /> : null}
              Sweep to Treasury
            </button>
          </div>
      </div>

      {/* Pool Reconciliation */}
      {!demoMode && (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Pool Integrity</span>
            <span className="card-subtitle">check UTXOs against blockchain</span>
          </div>
          <div style={{ fontSize: 13, color: 'var(--text-secondary)', marginBottom: 12 }}>
            Verifies all pool UTXOs still exist on-chain. Removes &quot;zombie&quot; UTXOs that were spent
            on-chain but not properly tracked — these cause broadcast failures (&quot;Missing inputs&quot;).
          </div>
          {reconcileResult && (
            <div className={`alert alert-${reconcileResult.type}`} style={{ marginBottom: 12 }}>
              {reconcileResult.text}
            </div>
          )}
          <button className="btn btn-primary" onClick={handleReconcile} disabled={reconciling}>
            {reconciling ? <><span className="spinner" /> Checking...</> : 'Reconcile Pools'}
          </button>
        </div>
      )}

      {/* Available Treasury UTXOs */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Available Treasury UTXOs</span>
          <span className="card-subtitle">
            {utxoData?.lastPoll
              ? `Last polled: ${new Date(utxoData.lastPoll).toLocaleTimeString()}`
              : 'Polling...'}
          </span>
        </div>
        {utxoData?.error && (
          <div className="alert alert-error" style={{ marginBottom: 12 }}>{utxoData.error}</div>
        )}
        {hasUTXOs ? (
          <table className="table">
            <thead>
              <tr>
                <th>TxID</th>
                <th>Vout</th>
                <th>Amount</th>
                <th>Status</th>
                <th>Action</th>
              </tr>
            </thead>
            <tbody>
              {utxos.map((utxo, i) => {
                const isSelected = fundingTxid === utxo.txid && fundingVout === String(utxo.vout)
                return (
                  <tr
                    key={`${utxo.txid}:${utxo.vout}`}
                    style={{
                      cursor: 'pointer',
                      background: isSelected ? 'var(--accent-bg, rgba(99,102,241,0.08))' : undefined,
                    }}
                    onClick={() => selectUTXO(utxo)}
                  >
                    <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
                      <a
                        href={txUrl(utxo.txid)}
                        target="_blank"
                        rel="noopener noreferrer"
                        onClick={(e) => e.stopPropagation()}
                        style={{ color: 'var(--accent-blue)', textDecoration: 'none' }}
                        title={demoMode ? `${utxo.txid} (demo — may not exist on-chain)` : utxo.txid}
                      >
                        {utxo.txid}
                      </a>
                    </td>
                    <td>{utxo.vout}</td>
                    <td>{formatSats(utxo.satoshis)}</td>
                    <td>
                      <span
                        style={{
                          fontSize: 10,
                          fontWeight: 600,
                          textTransform: 'uppercase',
                          letterSpacing: '0.05em',
                          padding: '2px 6px',
                          borderRadius: 4,
                          background: utxo.status === 'mempool'
                            ? 'rgba(245, 158, 11, 0.15)'
                            : 'rgba(16, 185, 129, 0.15)',
                          color: utxo.status === 'mempool'
                            ? '#f59e0b'
                            : '#10b981',
                          border: `1px solid ${utxo.status === 'mempool'
                            ? 'rgba(245, 158, 11, 0.3)'
                            : 'rgba(16, 185, 129, 0.3)'}`,
                        }}
                        title={utxo.status === 'mempool'
                          ? 'Locally tracked — not yet confirmed on-chain'
                          : 'Confirmed on-chain'}
                      >
                        {utxo.status === 'mempool' ? 'Mempool' : 'Confirmed'}
                      </span>
                    </td>
                    <td>
                      <button
                        className="btn btn-sm"
                        style={{ fontSize: 11, padding: '2px 8px' }}
                        onClick={(e) => {
                          e.stopPropagation()
                          selectUTXO(utxo)
                        }}
                      >
                        {isSelected ? '✓ Selected' : 'Use'}
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        ) : (
          <div style={{ padding: 16, color: 'var(--text-muted)', textAlign: 'center', fontSize: 13 }}>
            No UTXOs found at treasury address. Fund the address above to get started.
          </div>
        )}
      </div>

      {/* Fan-out form */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Create Fan-Out</span>
          <span className="card-subtitle">Split a funding UTXO into pool UTXOs</span>
        </div>

        {result && <div className={`alert alert-${result.type}`}>{result.text}</div>}

        <div className="grid grid-2">
          <div className="form-group">
            <label className="form-label">Target Pool</label>
            <select className="form-input" value={pool} onChange={(e) => setPool(e.target.value)}>
              <option value="nonce">Nonce Pool ({formatSats(info.noncePool?.utxo_value || 1)}/UTXO)</option>
              <option value="fee">Fee Pool ({formatSats(config?.feeUTXOSats || info.feePool?.utxo_value || 100)}/UTXO)</option>
            </select>
          </div>
          <div className="form-group">
            <label className="form-label">Output Count</label>
            <input className="form-input" type="number" value={count} onChange={(e) => setCount(e.target.value)} />
          </div>
        </div>

        {/* Funding source toggle */}
        <div className="form-group">
          <label className="form-label">Funding Source</label>
          <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
            <button
              className={`btn btn-sm ${inputMode === 'select' ? 'btn-primary' : ''}`}
              style={{ fontSize: 12 }}
              onClick={() => setInputMode('select')}
            >
              Select from Wallet
            </button>
            <button
              className={`btn btn-sm ${inputMode === 'manual' ? 'btn-primary' : ''}`}
              style={{ fontSize: 12 }}
              onClick={() => setInputMode('manual')}
            >
              Manual Entry
            </button>
          </div>
        </div>

        {inputMode === 'select' ? (
          <div className="form-group">
            <label className="form-label">Select UTXO</label>
            {hasUTXOs ? (
              <select
                className="form-input"
                value={fundingTxid ? `${fundingTxid}:${fundingVout}` : ''}
                onChange={(e) => {
                  const selected = utxos.find(
                    (u) => `${u.txid}:${u.vout}` === e.target.value
                  )
                  if (selected) selectUTXO(selected)
                }}
              >
                <option value="">-- Select a UTXO --</option>
                {utxos.map((utxo) => (
                  <option key={`${utxo.txid}:${utxo.vout}`} value={`${utxo.txid}:${utxo.vout}`}>
                    {utxo.txid} : {utxo.vout} ({formatSats(utxo.satoshis)})
                  </option>
                ))}
              </select>
            ) : (
              <div className="alert alert-info" style={{ fontSize: 13 }}>
                No UTXOs available. Fund the treasury address or switch to Manual Entry.
              </div>
            )}
            {fundingTxid && inputMode === 'select' && (
              <div style={{ marginTop: 8, fontSize: 12, color: 'var(--text-muted)', fontFamily: 'var(--font-mono)' }}>
                TXID: {fundingTxid}<br />
                Vout: {fundingVout} | Sats: {fundingSatoshis} | Script: {fundingScript.slice(0, 20)}...
              </div>
            )}
          </div>
        ) : (
          <>
            <div className="form-group">
              <label className="form-label">Funding TXID</label>
              <input className="form-input" placeholder="64-char hex txid" value={fundingTxid} onChange={(e) => setFundingTxid(e.target.value)} />
            </div>
            <div className="grid grid-2">
              <div className="form-group">
                <label className="form-label">Funding Vout</label>
                <input className="form-input" type="number" value={fundingVout} onChange={(e) => setFundingVout(e.target.value)} />
              </div>
              <div className="form-group">
                <label className="form-label">Funding Satoshis</label>
                <input className="form-input" type="number" value={fundingSatoshis} onChange={(e) => setFundingSatoshis(e.target.value)} />
              </div>
            </div>
            <div className="form-group">
              <label className="form-label">Funding Locking Script (hex)</label>
              <input className="form-input" placeholder="76a914...88ac" value={fundingScript} onChange={(e) => setFundingScript(e.target.value)} />
            </div>
          </>
        )}

        {/* Fan-out preview */}
        {fundingSatoshis && parseInt(count) > 0 && (() => {
          const poolStats = pool === 'nonce' ? info.noncePool : info.feePool
          const outputValue = pool === 'fee'
            ? (config?.feeUTXOSats || poolStats?.utxo_value || 100)
            : (poolStats?.utxo_value || 1)
          const outputCount = parseInt(count)
          const inputSats = parseInt(fundingSatoshis)
          const totalOutput = outputCount * outputValue
          // Estimate tx size: 10 overhead + 148/input + 34/output (including change output)
          const estimatedTxSize = 10 + 148 + (outputCount + 1) * 34
          // Fee rate from config (sat/byte), default 0.001 (1 sat/KB)
          const feeRate = config?.feeRate || 0.001
          const estimatedFee = Math.max(1, Math.ceil(estimatedTxSize * feeRate))
          const estimatedChange = inputSats - totalOutput - estimatedFee
          const insufficient = estimatedChange < 0

          return (
            <div style={{
              padding: '12px 14px',
              marginBottom: 12,
              borderRadius: 'var(--radius)',
              border: `1px solid ${insufficient ? 'var(--accent-red)' : 'var(--border)'}`,
              background: insufficient ? 'rgba(218, 54, 51, 0.06)' : 'var(--bg-primary)',
            }}>
              <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.3px', marginBottom: 8 }}>
                Fan-Out Preview
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 1fr 1fr', gap: 12 }}>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>Input Value</div>
                  <div style={{ fontSize: 14, fontWeight: 600, fontFamily: 'var(--font-mono)', color: 'var(--text-primary)' }}>
                    {formatSats(inputSats)}
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>Output Count</div>
                  <div style={{ fontSize: 14, fontWeight: 600, fontFamily: 'var(--font-mono)', color: 'var(--text-primary)' }}>
                    {outputCount.toLocaleString()}
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>Per UTXO</div>
                  <div style={{ fontSize: 14, fontWeight: 600, fontFamily: 'var(--font-mono)', color: 'var(--text-primary)' }}>
                    {formatSats(outputValue)}
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>Est. Fee</div>
                  <div style={{ fontSize: 14, fontWeight: 600, fontFamily: 'var(--font-mono)', color: insufficient ? 'var(--accent-red-text)' : 'var(--text-primary)' }}>
                    {insufficient ? '—' : formatSats(estimatedFee)}
                  </div>
                </div>
                <div>
                  <div style={{ fontSize: 11, color: 'var(--text-muted)' }}>Est. Change</div>
                  <div style={{ fontSize: 14, fontWeight: 600, fontFamily: 'var(--font-mono)', color: insufficient ? 'var(--accent-red-text)' : 'var(--accent-green-text)' }}>
                    {insufficient ? 'Insufficient' : formatSats(estimatedChange)}
                  </div>
                </div>
              </div>
              {insufficient && (
                <div style={{ marginTop: 8, fontSize: 12, color: 'var(--accent-red-text)' }}>
                  Need at least {formatSats(totalOutput + estimatedFee)} to create {outputCount} UTXOs at {formatSats(outputValue)} each.
                </div>
              )}
            </div>
          )
        })()}

        <button className="btn btn-primary" onClick={handleFanout} disabled={running || !fundingTxid || !fundingScript || !fundingSatoshis}>
          {running ? <span className="spinner" /> : null}
          Execute Fan-Out
        </button>
      </div>

      {/* History */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Fan-Out History</span>
        </div>
        {historyData?.history && historyData.history.length > 0 ? (
          <table className="table">
            <thead>
              <tr>
                <th>TxID</th>
                <th>Pool</th>
                <th>Count</th>
                <th>Time</th>
              </tr>
            </thead>
            <tbody>
              {historyData.history.map((entry, i) => (
                <tr key={i}>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>
                    <a
                      href={txUrl(entry.txid)}
                      target="_blank"
                      rel="noopener noreferrer"
                      style={{ color: 'var(--accent-blue)', textDecoration: 'none' }}
                      title={demoMode ? `${entry.txid} (demo — may not exist on-chain)` : entry.txid}
                    >
                      {entry.txid}
                    </a>
                  </td>
                  <td>{entry.pool}</td>
                  <td>{entry.count}</td>
                  <td>{new Date(entry.timestamp).toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ) : (
          <div style={{ padding: 16, color: 'var(--text-muted)', textAlign: 'center', fontSize: 13 }}>
            No fan-out transactions yet
          </div>
        )}
      </div>
    </div>
  )
}
