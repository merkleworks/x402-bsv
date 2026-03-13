// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//

import { useState, useCallback } from 'react'
import type { TabId } from './types'
import { useSSE } from './hooks/useSSE'
import Layout from './components/Layout'
import MonitorTab from './components/MonitorTab'
import SettingsTab from './components/SettingsTab'
import TreasuryTab from './components/TreasuryTab'
import TestingTab from './components/TestingTab'
import AnalyticsTab from './components/AnalyticsTab'

export default function App() {
  const [tab, setTab] = useState<TabId>('monitor')
  const { events, connected, clear: clearEvents } = useSSE('/api/v1/events/stream')

  const renderTab = useCallback(() => {
    switch (tab) {
      case 'monitor':
        return <MonitorTab events={events} connected={connected} clearEvents={clearEvents} />
      case 'settings':
        return <SettingsTab />
      case 'treasury':
        return <TreasuryTab />
      case 'testing':
        return <TestingTab />
      case 'analytics':
        return <AnalyticsTab />
    }
  }, [tab, events, connected, clearEvents])

  return (
    <Layout activeTab={tab} onTabChange={setTab} connected={connected}>
      {renderTab()}
    </Layout>
  )
}
