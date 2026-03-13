// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import { useState, useCallback } from 'react'
import { getConfig, updateConfig, getBroadcasterHealth } from '../api'
import { useApi } from '../hooks/useApi'
import type { BroadcasterHealthResponse } from '../types'

export default function SettingsTab() {
  const fetcher = useCallback(() => getConfig(), [])
  const { data: config, refresh } = useApi(fetcher)
  const healthFetcher = useCallback(() => getBroadcasterHealth(), [])
  const { data: health } = useApi<BroadcasterHealthResponse>(healthFetcher, 5000)
  const [saving, setSaving] = useState(false)
  const [message, setMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)
  const [restartWarning, setRestartWarning] = useState<string | null>(null)

  // Editable fields
  const [feeRate, setFeeRate] = useState<string>('')
  const [threshold, setThreshold] = useState<string>('')
  const [optimalSize, setOptimalSize] = useState<string>('')
  const [broadcaster, setBroadcaster] = useState<string>('')

  // Initialize editable fields when config loads
  if (config && !feeRate) {
    setFeeRate(String(config.feeRate))
    setThreshold(String(config.poolReplenishThreshold))
    setOptimalSize(String(config.poolOptimalSize))
    setBroadcaster(config.broadcaster)
  }

  async function handleSave() {
    setSaving(true)
    setMessage(null)
    try {
      const updates: Record<string, unknown> = {}
      if (config) {
        const newRate = parseFloat(feeRate)
        if (!isNaN(newRate) && newRate !== config.feeRate) updates.feeRate = newRate
        const newThreshold = parseInt(threshold)
        if (!isNaN(newThreshold) && newThreshold !== config.poolReplenishThreshold) updates.poolReplenishThreshold = newThreshold
        const newOptimal = parseInt(optimalSize)
        if (!isNaN(newOptimal) && newOptimal !== config.poolOptimalSize) updates.poolOptimalSize = newOptimal
        if (broadcaster && broadcaster !== config.broadcaster) updates.broadcaster = broadcaster
      }
      if (Object.keys(updates).length === 0) {
        setMessage({ type: 'error', text: 'No changes to save' })
        return
      }
      const result = await updateConfig(updates)
      setMessage({ type: 'success', text: 'Configuration updated successfully' })

      // Check if server signalled a restart is needed (e.g. broadcaster mode switch)
      if (result.restart_required) {
        setRestartWarning(result.restart_reason || 'Restart the gateway to apply pool backend changes.')
      }

      refresh()
    } catch (err) {
      setMessage({ type: 'error', text: err instanceof Error ? err.message : String(err) })
    } finally {
      setSaving(false)
    }
  }

  function copyToClipboard(text: string) {
    navigator.clipboard.writeText(text)
  }

  if (!config) return <div className="spinner" />

  return (
    <div>
      <div className="tab-header">
        <h2 className="tab-title">Settings</h2>
      </div>

      {restartWarning && (
        <div
          style={{
            padding: '12px 16px',
            marginBottom: 16,
            borderRadius: 8,
            background: 'rgba(245, 158, 11, 0.12)',
            border: '1px solid rgba(245, 158, 11, 0.35)',
            color: '#f59e0b',
            fontSize: 13,
            lineHeight: 1.5,
          }}
        >
          <strong style={{ display: 'block', marginBottom: 4 }}>Restart Required</strong>
          {restartWarning}
        </div>
      )}

      {message && (
        <div className={`alert alert-${message.type}`}>{message.text}</div>
      )}

      {/* Operator guidance */}
      <div className="alert alert-info" style={{ marginBottom: 16 }}>
        This gateway is configured via environment variables. To change server configuration, edit the <strong>.env</strong> file and restart the container.
      </div>

      {/* Read-only server config */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Server Configuration</span>
          <span className="card-subtitle">Read-only (set via .env)</span>
        </div>
        <div className="config-row">
          <span className="config-key">Profile</span>
          <span className="config-value" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {config.profile}
            {config.templateMode && (
              <span
                style={{
                  fontSize: 10,
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                  padding: '2px 6px',
                  borderRadius: 4,
                  background: 'rgba(99, 102, 241, 0.15)',
                  color: '#818cf8',
                  border: '1px solid rgba(99, 102, 241, 0.3)',
                }}
              >
                {config.templatePriceSats} sats
              </span>
            )}
          </span>
        </div>
        <div className="config-row">
          <span className="config-key">Network</span>
          <span className="config-value">{config.network}</span>
        </div>
        <div className="config-row">
          <span className="config-key">Port</span>
          <span className="config-value">{config.port}</span>
        </div>
        <div className="config-row">
          <span className="config-key">Redis</span>
          <span className="config-value">{config.redisEnabled ? 'Enabled' : 'Disabled (in-memory)'}</span>
        </div>
        <div className="config-row">
          <span className="config-key">Key Mode</span>
          <span className="config-value">{config.keyMode === 'xpriv' ? 'HD Wallet (xPriv)' : 'Single Key (WIF)'}</span>
        </div>
        <div className="config-row">
          <span className="config-key">Pool Size</span>
          <span className="config-value">{config.poolSize}</span>
        </div>
        <div className="config-row">
          <span className="config-key">Lease TTL</span>
          <span className="config-value">{config.leaseTTLSeconds}s</span>
        </div>
      </div>

      {/* Derived addresses */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Derived Addresses</span>
        </div>
        {[
          { label: 'Payee', addr: config.payeeAddress },
          { label: 'Nonce Pool', addr: config.nonceAddress },
          { label: 'Payment Pool', addr: config.paymentAddress },
          { label: 'Fee Pool', addr: config.feeAddress },
          { label: 'Treasury', addr: config.treasuryAddress },
        ].map(({ label, addr }) => (
          <div key={label} className="config-row">
            <span className="config-key">{label}</span>
            <span className="copy-text" onClick={() => copyToClipboard(addr)} title="Click to copy">
              <span className="addr">{addr}</span>
            </span>
          </div>
        ))}
      </div>

      {/* Editable config */}
      <div className="card">
        <div className="card-header">
          <span className="card-title">Runtime Configuration</span>
          <span className="card-subtitle">Applied immediately without restart</span>
        </div>
        <div className="form-group">
          <label className="form-label" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            Broadcaster
            {broadcaster === 'mock' ? (
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
              >
                Demo
              </span>
            ) : (
              <span
                style={{
                  fontSize: 10,
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.05em',
                  padding: '2px 6px',
                  borderRadius: 4,
                  background: 'rgba(34, 197, 94, 0.15)',
                  color: '#22c55e',
                  border: '1px solid rgba(34, 197, 94, 0.3)',
                }}
              >
                Live
              </span>
            )}
          </label>
          <select
            className="form-input"
            value={broadcaster}
            onChange={(e) => setBroadcaster(e.target.value)}
          >
            <option value="mock">Mock (demo — transactions not broadcast)</option>
            <option value="woc">WhatsOnChain (live — WoC only)</option>
            <option value="composite">Composite (GorillaPool primary + WoC fallback)</option>
          </select>
          {broadcaster !== config.broadcaster && (
            <div style={{ marginTop: 4, fontSize: 12, color: '#f59e0b' }}>
              Changing broadcaster will take effect immediately for all new transactions.
            </div>
          )}
        </div>
        <div className="form-group">
          <label className="form-label">Fee Rate (sat/byte)</label>
          <input
            className="form-input"
            type="number"
            step="0.001"
            value={feeRate}
            onChange={(e) => setFeeRate(e.target.value)}
          />
        </div>
        <div className="form-group">
          <label className="form-label">Pool Replenish Threshold</label>
          <input
            className="form-input"
            type="number"
            value={threshold}
            onChange={(e) => setThreshold(e.target.value)}
          />
        </div>
        <div className="form-group">
          <label className="form-label">Pool Optimal Size</label>
          <input
            className="form-input"
            type="number"
            value={optimalSize}
            onChange={(e) => setOptimalSize(e.target.value)}
          />
        </div>
        <button className="btn btn-primary" onClick={handleSave} disabled={saving}>
          {saving ? <span className="spinner" /> : null}
          Save Changes
        </button>
      </div>

      {/* Broadcaster Health & Stats (composite mode only) */}
      {health && health.stats && (
        <div className="card">
          <div className="card-header">
            <span className="card-title">Broadcaster Health</span>
            <span className="card-subtitle" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              {health.circuitBreakerOpen ? (
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
                >
                  Circuit Breaker Active
                </span>
              ) : (
                <span
                  style={{
                    fontSize: 10,
                    fontWeight: 600,
                    textTransform: 'uppercase',
                    letterSpacing: '0.05em',
                    padding: '2px 6px',
                    borderRadius: 4,
                    background: 'rgba(34, 197, 94, 0.15)',
                    color: '#22c55e',
                    border: '1px solid rgba(34, 197, 94, 0.3)',
                  }}
                >
                  Normal
                </span>
              )}
            </span>
          </div>

          {health.circuitBreakerOpen && (
            <div
              style={{
                padding: '8px 12px',
                margin: '0 0 8px 0',
                borderRadius: 6,
                background: 'rgba(245, 158, 11, 0.08)',
                border: '1px solid rgba(245, 158, 11, 0.2)',
                color: 'rgba(245, 158, 11, 0.9)',
                fontSize: 12,
                lineHeight: 1.5,
              }}
            >
              ARC fee-policy rejection detected. All broadcasts routed directly to WoC to avoid latency.
            </div>
          )}

          <div className="config-row">
            <span className="config-key">Primary (ARC)</span>
            <span className="config-value" style={{ display: 'flex', gap: 12 }}>
              <span style={{ color: '#22c55e' }}>{health.stats.primarySuccess} ok</span>
              <span style={{ color: '#ef4444' }}>{health.stats.primaryFailed} failed</span>
            </span>
          </div>
          <div className="config-row">
            <span className="config-key">Fallback (WoC)</span>
            <span className="config-value" style={{ display: 'flex', gap: 12 }}>
              <span style={{ color: '#22c55e' }}>{health.stats.fallbackSuccess} ok</span>
              <span style={{ color: '#ef4444' }}>{health.stats.fallbackFailed} failed</span>
            </span>
          </div>
          <div className="config-row">
            <span className="config-key">Fee Policy Rejects</span>
            <span className="config-value" style={{ color: health.stats.feePolicyRejects > 0 ? '#f59e0b' : 'inherit' }}>
              {health.stats.feePolicyRejects}
            </span>
          </div>

          {/* Per-service health indicators */}
          {Object.values(health.services).length > 0 && (
            <>
              <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', margin: '8px 0' }} />
              {Object.values(health.services).map((svc) => (
                <div key={`${svc.service}:${svc.role}`} className="config-row">
                  <span className="config-key" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span
                      style={{
                        width: 8,
                        height: 8,
                        borderRadius: '50%',
                        background: svc.healthy ? '#22c55e' : '#ef4444',
                        display: 'inline-block',
                        flexShrink: 0,
                      }}
                    />
                    {svc.service} ({svc.role})
                  </span>
                  <span className="config-value" style={{ fontSize: 12, opacity: 0.7 }}>
                    {svc.lastError || 'healthy'}
                  </span>
                </div>
              ))}
            </>
          )}
        </div>
      )}
    </div>
  )
}
