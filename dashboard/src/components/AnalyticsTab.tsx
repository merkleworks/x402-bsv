// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useCallback } from 'react'
import { getStatsSummary, getTimeseries } from '../api'
import { useApi } from '../hooks/useApi'
import type { TimeseriesPoint } from '../types'

function formatUptime(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

function BarChart({ points, field, color, label }: {
  points: TimeseriesPoint[]
  field: keyof TimeseriesPoint
  color: string
  label: string
}) {
  const max = Math.max(...points.map((p) => p[field] as number), 1)

  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">{label}</span>
        <span className="card-subtitle">Last hour (1-min buckets)</span>
      </div>
      <div className="chart-container">
        <div className="bar-chart">
          {points.map((p, i) => {
            const val = p[field] as number
            const height = (val / max) * 100
            return (
              <div
                key={i}
                className="bar"
                style={{
                  height: `${Math.max(height, 2)}%`,
                  background: val > 0 ? color : 'var(--bg-hover)',
                }}
                title={`${new Date(p.timestamp * 1000).toLocaleTimeString()}: ${val}`}
              />
            )
          })}
        </div>
      </div>
    </div>
  )
}

export default function AnalyticsTab() {
  const statsFetcher = useCallback(() => getStatsSummary(), [])
  const tsFetcher = useCallback(() => getTimeseries(), [])
  const { data: stats } = useApi(statsFetcher, 10000)
  const { data: tsData } = useApi(tsFetcher, 10000)

  const points = tsData?.points ?? []

  return (
    <div>
      <div className="tab-header">
        <h2 className="tab-title">Analytics</h2>
      </div>

      {/* Summary cards */}
      <div className="grid grid-4" style={{ marginBottom: 16 }}>
        <div className="card">
          <div className="stat-value">{stats?.totalRequests ?? '-'}</div>
          <div className="stat-label">Total Requests</div>
        </div>
        <div className="card">
          <div className="stat-value" style={{ color: 'var(--accent-green-text)' }}>
            {stats?.payments ?? '-'}
          </div>
          <div className="stat-label">Payments</div>
        </div>
        <div className="card">
          <div className="stat-value" style={{ color: 'var(--accent-blue)' }}>
            {stats ? `${stats.avgDurationMs.toFixed(1)}ms` : '-'}
          </div>
          <div className="stat-label">Avg Latency</div>
        </div>
        <div className="card">
          <div className="stat-value">
            {stats ? formatUptime(stats.uptimeSeconds) : '-'}
          </div>
          <div className="stat-label">Uptime</div>
        </div>
      </div>

      {/* Charts */}
      <div className="grid grid-2">
        <BarChart
          points={points}
          field="requests"
          color="var(--accent-blue)"
          label="Requests / Minute"
        />
        <BarChart
          points={points}
          field="payments"
          color="var(--accent-green-text)"
          label="Payments / Minute"
        />
      </div>

      <BarChart
        points={points}
        field="errors"
        color="var(--accent-red-text)"
        label="Errors / Minute"
      />

      {/* Pool utilization */}
      {stats && (
        <div className="grid grid-3">
          <div className="card">
            <div className="card-header">
              <span className="card-title">Nonce Pool Utilization</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
              <span className="stat-value">
                {stats.noncePool.total > 0
                  ? `${((stats.noncePool.available / stats.noncePool.total) * 100).toFixed(0)}%`
                  : '0%'}
              </span>
              <span className="stat-label">available</span>
            </div>
            <div style={{ marginTop: 8, fontSize: 13, color: 'var(--text-muted)' }}>
              {stats.noncePool.available} / {stats.noncePool.total} UTXOs
            </div>
          </div>
          <div className="card">
            <div className="card-header">
              <span className="card-title">Revenue Utilization</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
              <span className="stat-value">
                {stats.paymentPool.total > 0
                  ? `${((stats.paymentPool.available / stats.paymentPool.total) * 100).toFixed(0)}%`
                  : '0%'}
              </span>
              <span className="stat-label">available</span>
            </div>
            <div style={{ marginTop: 8, fontSize: 13, color: 'var(--text-muted)' }}>
              {stats.paymentPool.available} / {stats.paymentPool.total} UTXOs
            </div>
          </div>
          <div className="card">
            <div className="card-header">
              <span className="card-title">Fee Pool Utilization</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
              <span className="stat-value">
                {stats.feePool.total > 0
                  ? `${((stats.feePool.available / stats.feePool.total) * 100).toFixed(0)}%`
                  : '0%'}
              </span>
              <span className="stat-label">available</span>
            </div>
            <div style={{ marginTop: 8, fontSize: 13, color: 'var(--text-muted)' }}>
              {stats.feePool.available} / {stats.feePool.total} UTXOs
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
