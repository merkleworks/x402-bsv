// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useCallback } from 'react'
import type { GatewayEvent } from '../types'
import { getStatsSummary, getConfig, getRevenue } from '../api'
import { useApi } from '../hooks/useApi'
import PoolStats from './PoolStats'
import EventLog from './EventLog'

interface Props {
  events: GatewayEvent[]
  connected: boolean
  clearEvents: () => void
}

function formatUptime(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  const s = Math.floor(seconds % 60)
  if (h > 0) return `${h}h ${m}m ${s}s`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}

export default function MonitorTab({ events, connected, clearEvents }: Props) {
  const fetcher = useCallback(() => getStatsSummary(), [])
  const configFetcher = useCallback(() => getConfig(), [])
  const revenueFetcher = useCallback(() => getRevenue(), [])
  const { data: stats } = useApi(fetcher, 5000)
  const { data: config } = useApi(configFetcher, 30000)
  const { data: revenue } = useApi(revenueFetcher, 5000)

  return (
    <div>
      <div className="tab-header">
        <h2 className="tab-title">Monitor</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <span className={`status-dot ${connected ? 'connected' : 'disconnected'}`} />
          <span style={{ fontSize: 13, color: 'var(--text-muted)' }}>
            {connected ? 'Live' : 'Reconnecting...'}
          </span>
        </div>
      </div>

      {/* Summary stats */}
      <div className="grid grid-4" style={{ marginBottom: 16 }}>
        <div className="card">
          <div className="stat-value">{stats?.totalRequests ?? '-'}</div>
          <div className="stat-label">Requests (1h)</div>
        </div>
        <div className="card">
          <div className="stat-value" style={{ color: 'var(--accent-green-text)' }}>
            {stats?.payments ?? '-'}
          </div>
          <div className="stat-label">Payments</div>
        </div>
        <div className="card">
          <div className="stat-value" style={{ color: 'var(--accent-yellow)' }}>
            {stats?.challenges ?? '-'}
          </div>
          <div className="stat-label">Challenges</div>
        </div>
        <div className="card">
          <div className="stat-value" style={{ color: 'var(--accent-red-text)' }}>
            {stats?.errors ?? '-'}
          </div>
          <div className="stat-label">Errors</div>
        </div>
      </div>

      {/* Server info + Pool stats */}
      <div className="grid grid-2">
        <div>
          {stats?.noncePool && <PoolStats label="Nonce" stats={stats.noncePool} />}
          {stats?.feePool && <PoolStats label="Fee" stats={stats.feePool} configuredUtxoValue={config?.feeUTXOSats} />}
          <div className="card">
              <div className="card-header">
                <span className="card-title">Revenue</span>
                <span className="card-subtitle">lifetime settlements</span>
              </div>
              <div style={{
                display: 'grid',
                gridTemplateColumns: '1fr 1fr',
                gap: 8,
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
                    {(revenue?.totalSats ?? 0).toLocaleString()} sats
                  </div>
                </div>
              </div>
            </div>
        </div>
        <div>
          <div className="card">
            <div className="card-header">
              <span className="card-title">Server Info</span>
            </div>
            <div className="config-row">
              <span className="config-key">Uptime</span>
              <span className="config-value">{stats ? formatUptime(stats.uptimeSeconds) : '-'}</span>
            </div>
            <div className="config-row">
              <span className="config-key">Avg Latency</span>
              <span className="config-value">{stats ? `${stats.avgDurationMs.toFixed(1)}ms` : '-'}</span>
            </div>
            <div className="config-row">
              <span className="config-key">Total Fee Sats</span>
              <span className="config-value">{stats?.totalFeeSats ?? '-'}</span>
            </div>
          </div>
        </div>
      </div>

      {/* Event log */}
      <EventLog events={events} onClear={clearEvents} />
    </div>
  )
}
