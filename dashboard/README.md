# x402 Dashboard

React-based operational dashboard for the x402 Settlement Gateway. Built with Vite and TypeScript.

## Development

```bash
# From the dashboard/ directory
npm ci
npm run dev        # Start dev server with HMR (proxies API to localhost:8402)

# Or from the project root
make dashboard-dev
```

The dev server proxies API requests to the gateway at `http://localhost:8402`. Make sure the gateway is running first (`make demo` or `make run`).

## Production Build

```bash
npm run build
```

Vite outputs to `../cmd/server/static/`, which the Go server embeds and serves at `/`. The `make build` target handles this automatically.

## Architecture

```
dashboard/src/
├── main.tsx              # Entry point, renders App
├── App.tsx               # Tab router (Monitor, Testing, Treasury, Analytics, Settings)
├── api.ts                # API client — all fetch calls to the Go backend
├── types.ts              # TypeScript interfaces matching Go JSON responses
├── hooks/
│   ├── useApi.ts         # Generic fetch hook with loading/error state
│   ├── useInterval.ts    # Polling hook for periodic data refresh
│   └── useSSE.ts         # Server-Sent Events hook for real-time updates
├── components/
│   ├── Layout.tsx         # Page shell, navigation tabs, dark theme
│   ├── MonitorTab.tsx     # Live pool stats, broadcaster health, event log
│   ├── TestingTab.tsx     # Interactive settlement flow runner (end-to-end 402 test)
│   ├── TreasuryTab.tsx    # Treasury UTXOs, fan-out, sweep, revenue tracking
│   ├── AnalyticsTab.tsx   # Request statistics, time-series charts
│   ├── SettingsTab.tsx    # Runtime configuration editor (fee rate, broadcaster, pool sizes)
│   ├── PoolStats.tsx      # Reusable pool statistics card (nonce/fee/payment)
│   ├── EventLog.tsx       # SSE event stream display
│   └── SettlementTimeline.tsx  # Step-by-step settlement flow visualization
└── styles/
    └── global.css         # CSS variables, dark theme, layout primitives
```

## Key Components

### MonitorTab
Displays real-time gateway health: pool availability (nonce, fee, payment), broadcaster status (circuit breaker state for composite mode), and a live event log fed by SSE from `/api/v1/events/stream`.

### TestingTab
Runs a complete 402 settlement flow from the browser: requests a protected endpoint, receives a challenge, builds a partial transaction, delegates fees, broadcasts, and retries with proof. Displays each step in a `SettlementTimeline` with timing and transaction links (WhatsOnChain).

### TreasuryTab
Treasury management: view treasury UTXOs (from on-chain polling), fan-out new UTXOs into pools, sweep spent pools back to treasury, and sweep settlement revenue. Shows pool balances and fan-out history.

### SettingsTab
Runtime configuration editor. Allows changing fee rate, broadcaster mode, pool sizes, and replenish thresholds without restarting the server (via PUT `/api/v1/config`). Displays a restart-required warning when the broadcaster mode changes between mock and live.

### AnalyticsTab
Aggregate statistics (total requests, payments, challenges, errors, average duration) and time-series charts from `/api/v1/stats/timeseries`.

## Data Flow

1. **Polling** — `useInterval` fetches `/api/v1/stats/summary`, `/api/v1/config`, and pool data every few seconds
2. **SSE** — `useSSE` connects to `/api/v1/events/stream` for real-time event notifications (new challenges, payments, errors)
3. **API calls** — User actions (fan-out, sweep, config update, settlement test) go through `api.ts` which wraps `fetch` with error handling

## Type Safety

All API response types are defined in `types.ts` and match the Go backend's JSON serialization. Key types:

- `ConfigResponse` — GET `/api/v1/config`
- `PoolStats` — pool availability (available, leased, spent, quarantined)
- `BroadcasterHealthResponse` — broadcaster status and circuit breaker state
- `StatsSummary` — aggregate request statistics
- `TreasuryUTXO` — individual treasury UTXO (txid, vout, satoshis, status)

## Adding a New Tab

1. Create `components/NewTab.tsx` with your component
2. Add the tab to the `tabs` array in `App.tsx`
3. Import any new API types in `types.ts`
4. Add fetch functions to `api.ts` if new endpoints are needed
