// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

import React from 'react'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type TimelineStageStatus = 'pending' | 'success' | 'error'

export interface TimelineStage {
  status: TimelineStageStatus
  timestamp?: number
  details?: string
  /** Optional key-value metadata shown below the details line */
  meta?: Record<string, string>
}

export interface TimelineState {
  challenge: TimelineStage
  template: TimelineStage
  delegator: TimelineStage
  broadcast: TimelineStage
  mempool: TimelineStage
  settlement: TimelineStage
  unlock: TimelineStage
}

export const INITIAL_TIMELINE: TimelineState = {
  challenge:  { status: 'pending' },
  template:   { status: 'pending' },
  delegator:  { status: 'pending' },
  broadcast:  { status: 'pending' },
  mempool:    { status: 'pending' },
  settlement: { status: 'pending' },
  unlock:     { status: 'pending' },
}

// ---------------------------------------------------------------------------
// Stage metadata (ordered)
// ---------------------------------------------------------------------------

interface StageMeta {
  key: keyof TimelineState
  label: string
  /** Short description shown in muted text when stage is pending */
  hint: string
}

const STAGES: StageMeta[] = [
  { key: 'challenge',  label: 'Challenge Issued',                hint: 'Gateway returns 402' },
  { key: 'template',   label: 'Template Extracted',              hint: 'Decode nonce & price' },
  { key: 'delegator',  label: 'Delegator Completed Transaction', hint: 'Fee inputs added' },
  { key: 'broadcast',  label: 'Transaction Broadcast',           hint: 'Submit to network' },
  { key: 'mempool',    label: 'Mempool Confirmed',               hint: 'Await visibility' },
  { key: 'settlement', label: 'Settlement Detected',             hint: 'Payment verified' },
  { key: 'unlock',     label: 'Resource Unlocked',               hint: '200 OK' },
]

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const CIRCLE_SIZE = 12
const LINE_LEFT = CIRCLE_SIZE / 2 // centre of circle

function circleColor(status: TimelineStageStatus, isReached: boolean): string {
  if (!isReached) return 'var(--text-muted)'  // #6e7681 — inactive
  switch (status) {
    case 'success': return 'var(--accent-green-text)' // #3fb950
    case 'error':   return 'var(--accent-red-text)'   // #f85149
    case 'pending': return 'var(--accent-yellow)'     // #d29922
  }
}

function formatTimestamp(epoch: number): string {
  const d = new Date(epoch)
  const hms = d.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
  const ms = String(d.getMilliseconds()).padStart(3, '0')
  return `${hms}.${ms}`
}

function formatElapsed(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

/**
 * Determine whether a stage has been "reached" — meaning it is no longer in
 * its default-pending state waiting to start.  A stage is reached when it has
 * a non-pending status OR when any stage *after* it has already resolved.
 */
function buildReachedSet(timeline: TimelineState): Set<keyof TimelineState> {
  const reached = new Set<keyof TimelineState>()
  let lastReached = -1

  // Forward pass: mark stages that are explicitly non-pending.
  for (let i = 0; i < STAGES.length; i++) {
    const stage = timeline[STAGES[i].key]
    if (stage.status !== 'pending' || stage.timestamp != null) {
      reached.add(STAGES[i].key)
      lastReached = i
    }
  }

  // All stages up to the last reached one are considered reached so that the
  // connecting line draws through them even when they are still pending.
  for (let i = 0; i <= lastReached; i++) {
    reached.add(STAGES[i].key)
  }

  return reached
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface SettlementTimelineProps {
  timeline: TimelineState
  broadcastToMempoolMs?: number
}

export default function SettlementTimeline({
  timeline,
  broadcastToMempoolMs,
}: SettlementTimelineProps) {
  const reached = buildReachedSet(timeline)

  return (
    <div style={{ position: 'relative', paddingLeft: 28 }}>
      {STAGES.map((meta, idx) => {
        const stage = timeline[meta.key]
        const isReached = reached.has(meta.key)
        const isLast = idx === STAGES.length - 1
        const color = circleColor(stage.status, isReached)

        // Show elapsed badge between broadcast and mempool
        const showElapsed =
          meta.key === 'broadcast' &&
          broadcastToMempoolMs != null &&
          broadcastToMempoolMs > 0

        return (
          <React.Fragment key={meta.key}>
            {/* Row */}
            <div style={{ position: 'relative', paddingBottom: isLast ? 0 : 24 }}>
              {/* Connecting line to next stage */}
              {!isLast && (
                <div
                  style={{
                    position: 'absolute',
                    left: -28 + LINE_LEFT,
                    top: CIRCLE_SIZE,
                    bottom: 0,
                    width: 2,
                    background: 'var(--border)',
                  }}
                />
              )}

              {/* Circle indicator */}
              <div
                style={{
                  position: 'absolute',
                  left: -28,
                  top: 1,
                  width: CIRCLE_SIZE,
                  height: CIRCLE_SIZE,
                  borderRadius: '50%',
                  background: color,
                  flexShrink: 0,
                  boxShadow:
                    stage.status === 'success' && isReached
                      ? `0 0 6px ${color}`
                      : undefined,
                }}
              />

              {/* Label */}
              <div
                style={{
                  fontSize: 13,
                  fontWeight: 600,
                  color: isReached
                    ? 'var(--text-primary)'
                    : 'var(--text-muted)',
                  lineHeight: `${CIRCLE_SIZE}px`,
                }}
              >
                {meta.label}
              </div>

              {/* Timestamp */}
              {stage.timestamp != null && (
                <div
                  style={{
                    fontSize: 11,
                    color: 'var(--text-muted)',
                    marginTop: 2,
                  }}
                >
                  {formatTimestamp(stage.timestamp)}
                </div>
              )}

              {/* Details */}
              {stage.details && (
                <div
                  style={{
                    fontSize: 12,
                    fontFamily: 'var(--font-mono)',
                    color: 'var(--text-secondary)',
                    marginTop: 4,
                    wordBreak: 'break-all',
                  }}
                >
                  {stage.details}
                </div>
              )}

              {/* Goal 2: Richer metadata tags */}
              {stage.meta && Object.keys(stage.meta).length > 0 && (
                <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginTop: 4 }}>
                  {Object.entries(stage.meta).map(([k, v]) => (
                    <span
                      key={k}
                      style={{
                        display: 'inline-block',
                        fontSize: 10,
                        fontFamily: 'var(--font-mono)',
                        padding: '1px 6px',
                        borderRadius: 3,
                        background: 'var(--bg-tertiary)',
                        border: '1px solid var(--border)',
                        color: 'var(--text-muted)',
                      }}
                    >
                      {k}: {v}
                    </span>
                  ))}
                </div>
              )}

              {/* Hint for unreached stages */}
              {!isReached && !stage.details && (
                <div
                  style={{
                    fontSize: 11,
                    color: 'var(--text-muted)',
                    marginTop: 2,
                    fontStyle: 'italic',
                  }}
                >
                  {meta.hint}
                </div>
              )}
            </div>

            {/* Elapsed badge between broadcast and mempool */}
            {showElapsed && (
              <div
                style={{
                  position: 'relative',
                  paddingBottom: 24,
                }}
              >
                {/* Continuation line behind badge */}
                <div
                  style={{
                    position: 'absolute',
                    left: -28 + LINE_LEFT,
                    top: 0,
                    bottom: 0,
                    width: 2,
                    background: 'var(--border)',
                  }}
                />

                {/* Badge */}
                <div
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 6,
                    marginLeft: 4,
                    padding: '2px 10px',
                    borderRadius: 10,
                    background: 'rgba(88,166,255,0.12)',
                    border: '1px solid rgba(88,166,255,0.25)',
                    fontSize: 11,
                    fontWeight: 500,
                    color: 'var(--accent-blue)',
                    fontFamily: 'var(--font-mono)',
                  }}
                >
                  Elapsed: {formatElapsed(broadcastToMempoolMs!)}
                </div>
              </div>
            )}
          </React.Fragment>
        )
      })}
    </div>
  )
}
