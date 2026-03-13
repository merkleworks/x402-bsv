// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package dashboard

import (
	"net/http"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/merkleworks/x402-bsv/internal/broadcast"
	"github.com/merkleworks/x402-bsv/internal/config"
	"github.com/merkleworks/x402-bsv/internal/hdwallet"
	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/treasury"
)

// DashboardAPI provides HTTP handlers for the React dashboard.
type DashboardAPI struct {
	cfg           *config.Config
	keys          *hdwallet.DerivedKeys
	noncePool     pool.Pool
	feePool       pool.Pool
	paymentPool   pool.Pool
	stats         *StatsCollector
	treasuryKey   *ec.PrivateKey
	mainnet       bool
	broadcaster   *broadcast.Swappable
	startTime     time.Time
	payeeAddr     string
	watcher        *treasury.TreasuryWatcher  // may be nil if watcher not configured
	healthTracker  *broadcast.HealthTracker   // may be nil if not using composite broadcaster
	revenueTracker *RevenueTracker            // persistent settlement revenue
	wocBaseURL     string                     // WoC-compatible API base URL for UTXO lookups
}

// NewDashboardAPI creates a new dashboard API instance.
func NewDashboardAPI(
	cfg *config.Config,
	keys *hdwallet.DerivedKeys,
	noncePool, feePool, paymentPool pool.Pool,
	treasuryKey *ec.PrivateKey,
	mainnet bool,
	bcast *broadcast.Swappable,
	startTime time.Time,
	payeeAddr string,
	watcher *treasury.TreasuryWatcher,
	healthTracker *broadcast.HealthTracker,
	revenueTracker *RevenueTracker,
	wocBaseURL string,
) *DashboardAPI {
	return &DashboardAPI{
		cfg:            cfg,
		keys:           keys,
		noncePool:      noncePool,
		feePool:        feePool,
		paymentPool:    paymentPool,
		stats:          NewStatsCollector(3600, time.Minute), // 1 hour of 1-min buckets
		treasuryKey:    treasuryKey,
		mainnet:        mainnet,
		broadcaster:    bcast,
		startTime:      startTime,
		payeeAddr:      payeeAddr,
		watcher:        watcher,
		healthTracker:  healthTracker,
		revenueTracker: revenueTracker,
		wocBaseURL:     wocBaseURL,
	}
}

// Stats returns the stats collector for use by the logging middleware.
func (d *DashboardAPI) Stats() *StatsCollector {
	return d.stats
}

// RegisterRoutes adds all dashboard API routes to the provided mux.
func (d *DashboardAPI) RegisterRoutes(mux *http.ServeMux) {
	// Config endpoints
	mux.HandleFunc("GET /api/v1/config", d.handleGetConfig())
	mux.HandleFunc("PUT /api/v1/config", d.handleUpdateConfig())

	// Treasury endpoints
	mux.HandleFunc("GET /api/v1/treasury/info", d.handleTreasuryInfo())
	mux.HandleFunc("GET /api/v1/treasury/utxos", d.handleTreasuryUTXOs())
	mux.HandleFunc("POST /api/v1/treasury/fanout", d.handleTreasuryFanout())
	mux.HandleFunc("POST /api/v1/treasury/sweep", d.handleTreasurySweep())
	mux.HandleFunc("POST /api/v1/treasury/sweep-revenue", d.handleSweepRevenue())
	mux.HandleFunc("GET /api/v1/treasury/history", d.handleTreasuryHistory())

	// Broadcast endpoint (proxies to configured broadcaster for dashboard testing)
	mux.HandleFunc("POST /api/v1/broadcast", d.handleBroadcast())

	// Pool reconciliation (checks UTXOs against blockchain, marks zombies as spent)
	mux.HandleFunc("POST /api/v1/pools/reconcile", d.handleReconcilePools())

	// Broadcaster health (composite mode)
	mux.HandleFunc("GET /api/v1/health/broadcasters", d.handleBroadcasterHealth())

	// Stats endpoints
	mux.HandleFunc("GET /api/v1/stats/summary", d.handleStatsSummary())
	mux.HandleFunc("GET /api/v1/stats/timeseries", d.handleStatsTimeseries())

	// Revenue endpoint (persistent settlement counter)
	mux.HandleFunc("GET /api/v1/revenue", d.handleRevenue())
}
