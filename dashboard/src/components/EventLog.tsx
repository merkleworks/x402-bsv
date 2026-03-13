// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import type { GatewayEvent } from '../types'

interface Props {
  events: GatewayEvent[]
  onClear: () => void
}

function statusClass(status: number): string {
  if (status >= 500) return 's5xx'
  if (status === 402) return 's402'
  if (status >= 400) return 's4xx'
  return 's200'
}

function formatTime(ts: string): string {
  try {
    const d = new Date(ts)
    return d.toLocaleTimeString('en-US', { hour12: false })
  } catch {
    return ts
  }
}

export default function EventLog({ events, onClear }: Props) {
  return (
    <div className="card">
      <div className="card-header">
        <span className="card-title">Event Stream</span>
        <button className="btn btn-sm" onClick={onClear}>Clear</button>
      </div>
      <div className="event-log">
        {events.length === 0 && (
          <div style={{ padding: '16px', color: 'var(--text-muted)', textAlign: 'center', fontSize: '13px' }}>
            Waiting for events...
          </div>
        )}
        {events.map((e, i) => (
          <div key={i} className="event-row">
            <span className="event-time">{formatTime(e.timestamp)}</span>
            <span className={`event-badge ${statusClass(e.status)}`}>{e.status}</span>
            <span className="event-method">{e.method}</span>
            <span className="event-path">{e.path}</span>
            <span className="event-duration">{e.duration_ms}ms</span>
          </div>
        ))}
      </div>
    </div>
  )
}
