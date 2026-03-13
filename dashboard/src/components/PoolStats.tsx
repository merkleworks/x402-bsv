// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import type { PoolStats as PoolStatsType } from '../types'

interface Props {
  label: string
  stats: PoolStatsType
  configuredUtxoValue?: number // override utxo_value from config (e.g. FEE_UTXO_SATS)
}

function formatSats(sats: number): string {
  if (sats >= 100_000_000) return `${(sats / 100_000_000).toFixed(2)} BSV`
  if (sats >= 1_000_000) return `${(sats / 1_000_000).toFixed(1)}M sats`
  if (sats >= 1_000) return `${(sats / 1_000).toFixed(1)}K sats`
  return `${sats} sats`
}

function healthBadge(available: number): { text: string; color: string; bg: string; border: string } {
  if (available <= 10) return { text: 'Critical', color: 'var(--accent-red-text)', bg: 'rgba(218, 54, 51, 0.12)', border: 'rgba(218, 54, 51, 0.3)' }
  if (available <= 25) return { text: 'Low', color: 'var(--accent-yellow)', bg: 'rgba(210, 153, 34, 0.12)', border: 'rgba(210, 153, 34, 0.3)' }
  return { text: 'Healthy', color: 'var(--accent-green-text)', bg: 'rgba(35, 134, 54, 0.12)', border: 'rgba(35, 134, 54, 0.3)' }
}

export default function PoolStats({ label, stats, configuredUtxoValue }: Props) {
  const total = stats.total || 1 // avoid div by zero
  const availPct = (stats.available / total) * 100
  const leasedPct = (stats.leased / total) * 100
  const spentPct = (stats.spent / total) * 100

  const utxoValue = configuredUtxoValue || stats.utxo_value || 0
  const totalValue = stats.available * utxoValue
  const capacityLabel = label === 'Revenue' ? 'payments received' : label === 'Fee' ? 'delegations' : 'challenges'
  const health = healthBadge(stats.available)

  return (
    <div className="card">
      <div className="card-header">
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span className="card-title">{label} Pool</span>
          <span style={{
            fontSize: 10,
            fontWeight: 600,
            padding: '1px 6px',
            borderRadius: 4,
            color: health.color,
            background: health.bg,
            border: `1px solid ${health.border}`,
            textTransform: 'uppercase',
            letterSpacing: '0.3px',
          }}>
            {health.text}
          </span>
        </div>
        <span className="card-subtitle">{stats.available} / {stats.total} available</span>
      </div>

      {/* UTXO denomination & pool value grid */}
      {utxoValue > 0 && (
        <div style={{
          display: 'grid',
          gridTemplateColumns: '1fr 1fr 1fr',
          gap: 8,
          marginBottom: 12,
          padding: '10px 12px',
          background: 'var(--bg-primary)',
          borderRadius: 'var(--radius)',
          border: '1px solid var(--border)',
        }}>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px' }}>
              UTXO Value
            </div>
            <div style={{ fontSize: 16, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--text-primary)', marginTop: 2 }}>
              {utxoValue === 1 ? '1 sat' : formatSats(utxoValue)}
            </div>
          </div>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px' }}>
              Total Value
            </div>
            <div style={{ fontSize: 16, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-green-text)', marginTop: 2 }}>
              {formatSats(totalValue)}
            </div>
          </div>
          <div>
            <div style={{ fontSize: 11, color: 'var(--text-muted)', textTransform: 'uppercase', letterSpacing: '0.3px' }}>
              Capacity
            </div>
            <div style={{ fontSize: 16, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--accent-blue)', marginTop: 2 }}>
              {stats.available} {capacityLabel}
            </div>
          </div>
        </div>
      )}

      <div className="pool-bar-container">
        <div className="pool-bar">
          <div className="pool-bar-segment available" style={{ width: `${availPct}%` }} />
          <div className="pool-bar-segment leased" style={{ width: `${leasedPct}%` }} />
          <div className="pool-bar-segment spent" style={{ width: `${spentPct}%` }} />
        </div>
        <div className="pool-bar-legend">
          <span><span className="legend-dot" style={{ background: 'var(--accent-green-text)' }} /> Available: {stats.available}</span>
          <span><span className="legend-dot" style={{ background: 'var(--accent-yellow)' }} /> Leased: {stats.leased}</span>
          <span><span className="legend-dot" style={{ background: 'var(--accent-red-text)' }} /> Spent: {stats.spent}</span>
        </div>
      </div>
    </div>
  )
}
