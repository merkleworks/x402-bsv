// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import type { ReactNode } from 'react'
import type { TabId } from '../types'

const tabs: { id: TabId; label: string; icon: string }[] = [
  { id: 'monitor', label: 'Monitor', icon: '\u{1F4CA}' },
  { id: 'settings', label: 'Settings', icon: '\u2699\uFE0F' },
  { id: 'treasury', label: 'Treasury', icon: '\u{1F3E6}' },
  { id: 'testing', label: 'Testing', icon: '\u{1F9EA}' },
  { id: 'analytics', label: 'Analytics', icon: '\u{1F4C8}' },
]

interface LayoutProps {
  activeTab: TabId
  onTabChange: (tab: TabId) => void
  connected: boolean
  children: ReactNode
}

export default function Layout({ activeTab, onTabChange, connected, children }: LayoutProps) {
  return (
    <div className="app">
      <aside className="sidebar">
        <div className="sidebar-header">
          <h1>x402 <span className="badge">Gateway</span></h1>
          <div className="sub">BSV Micropayment Protocol</div>
        </div>
        <nav className="sidebar-nav">
          {tabs.map((t) => (
            <button
              key={t.id}
              className={`nav-item ${activeTab === t.id ? 'active' : ''}`}
              onClick={() => onTabChange(t.id)}
            >
              <span>{t.icon}</span>
              {t.label}
            </button>
          ))}
        </nav>
        <div className="connection-status">
          <span className={`status-dot ${connected ? 'connected' : 'disconnected'}`} />
          {connected ? 'Connected' : 'Disconnected'}
        </div>
      </aside>
      <main className="main-content">
        {children}
      </main>
    </div>
  )
}
