// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/merkleworks/x402-bsv/internal/broadcast"
)

// ConfigResponse is the safe configuration response (no secret keys).
type ConfigResponse struct {
	Network                string  `json:"network"`
	Port                   int     `json:"port"`
	Broadcaster            string  `json:"broadcaster"`
	FeeRate                float64 `json:"feeRate"`
	PoolReplenishThreshold int     `json:"poolReplenishThreshold"`
	PoolOptimalSize        int     `json:"poolOptimalSize"`
	RedisEnabled           bool    `json:"redisEnabled"`
	PoolSize               int     `json:"poolSize"`
	LeaseTTL               int     `json:"leaseTTLSeconds"`
	PayeeAddress           string  `json:"payeeAddress"`
	KeyMode                string  `json:"keyMode"` // "xpriv" or "wif"
	NonceAddress           string  `json:"nonceAddress"`
	FeeAddress             string  `json:"feeAddress"`
	PaymentAddress         string  `json:"paymentAddress"`
	TreasuryAddress        string  `json:"treasuryAddress"`
	TemplateMode           bool    `json:"templateMode"`
	TemplatePriceSats      uint64  `json:"templatePriceSats,omitempty"`
	FeeUTXOSats            uint64  `json:"feeUTXOSats"` // fee pool UTXO denomination (1–1000 sats)
	Profile                string  `json:"profile"`       // "A (Open Nonce)" or "B (Gateway Template)"
	DelegatorURL           string  `json:"delegatorUrl"`   // URL for the delegator service
	DelegatorEmbedded      bool    `json:"delegatorEmbedded"` // true if delegator is hosted in-process
	BroadcasterURL         string  `json:"broadcasterUrl,omitempty"` // URL for the broadcaster (if available)
	Mode                   string  `json:"mode"`                     // "mock" or "live" (runtime mode for pool namespace)
	ArcURL                 string  `json:"arcUrl,omitempty"`         // GorillaPool ARC URL (composite mode only)
}

// ConfigUpdateRequest is the subset of config that can be changed at runtime.
type ConfigUpdateRequest struct {
	FeeRate                *float64 `json:"feeRate,omitempty"`
	PoolReplenishThreshold *int     `json:"poolReplenishThreshold,omitempty"`
	PoolOptimalSize        *int     `json:"poolOptimalSize,omitempty"`
	Broadcaster            *string  `json:"broadcaster,omitempty"`
}

// handleGetConfig returns the current (safe) configuration.
func (d *DashboardAPI) handleGetConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyMode := "wif"
		if d.cfg.XPRIV != "" {
			keyMode = "xpriv"
		}

		profile := "A (Open Nonce)"
		if d.cfg.TemplateMode {
			profile = "B (Gateway Template)"
		}

		// Determine delegator URL for the dashboard.
		// When embedded, use "" so the browser fetches from the same origin.
		delegatorURL := d.cfg.DelegatorURL
		if delegatorURL == "" && !d.cfg.DelegatorEmbedded {
			// External: default delegator port
			delegatorURL = fmt.Sprintf("http://localhost:%d", d.cfg.DelegatorPort)
		}

		resp := ConfigResponse{
			Network:                d.cfg.BSVNetwork,
			Port:                   d.cfg.Port,
			Broadcaster:            d.broadcaster.Mode(),
			FeeRate:                d.cfg.FeeRate,
			PoolReplenishThreshold: d.cfg.PoolReplenishThreshold,
			PoolOptimalSize:        d.cfg.PoolOptimalSize,
			RedisEnabled:           d.cfg.RedisEnabled,
			PoolSize:               d.cfg.PoolSize,
			LeaseTTL:               int(d.cfg.LeaseTTL.Seconds()),
			PayeeAddress:           d.payeeAddr,
			KeyMode:                keyMode,
			NonceAddress:           d.keys.NonceAddress,
			FeeAddress:             d.keys.FeeAddress,
			PaymentAddress:         d.keys.PaymentAddress,
			TreasuryAddress:        d.keys.TreasuryAddress,
			TemplateMode:           d.cfg.TemplateMode,
			TemplatePriceSats:      d.cfg.TemplatePriceSats,
			FeeUTXOSats:            d.cfg.FeeUTXOSats,
			Profile:                profile,
			DelegatorURL:           delegatorURL,
			DelegatorEmbedded:      d.cfg.DelegatorEmbedded,
			Mode:                   d.cfg.RuntimeMode(),
		}

		// Include ARC URL when running in composite mode
		if d.broadcaster.Mode() == "composite" {
			resp.ArcURL = d.cfg.ArcURL
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// handleUpdateConfig applies runtime-adjustable configuration changes.
func (d *DashboardAPI) handleUpdateConfig() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ConfigUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}

		updated := make(map[string]any)

		if req.FeeRate != nil {
			if *req.FeeRate <= 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "feeRate must be positive",
				})
				return
			}
			d.cfg.FeeRate = *req.FeeRate
			updated["feeRate"] = d.cfg.FeeRate
		}

		if req.PoolReplenishThreshold != nil {
			if *req.PoolReplenishThreshold < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "poolReplenishThreshold must be non-negative",
				})
				return
			}
			d.cfg.PoolReplenishThreshold = *req.PoolReplenishThreshold
			updated["poolReplenishThreshold"] = d.cfg.PoolReplenishThreshold
		}

		if req.PoolOptimalSize != nil {
			if *req.PoolOptimalSize < 1 {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "poolOptimalSize must be at least 1",
				})
				return
			}
			d.cfg.PoolOptimalSize = *req.PoolOptimalSize
			updated["poolOptimalSize"] = d.cfg.PoolOptimalSize
		}

		restartRequired := false
		restartReason := ""

		if req.Broadcaster != nil {
			newMode := *req.Broadcaster
			if newMode != "mock" && newMode != "woc" && newMode != "composite" {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "broadcaster must be \"mock\", \"woc\", or \"composite\"",
				})
				return
			}
			if newMode != d.broadcaster.Mode() {
				switch newMode {
				case "woc":
					d.broadcaster.Swap(broadcast.NewWoCBroadcaster(d.wocBaseURL), "woc")
					d.healthTracker = nil
				case "composite":
					ht := broadcast.NewHealthTracker()
					primary := broadcast.NewGorillaPoolBroadcaster(d.cfg.ArcURL, d.cfg.ArcAPIKey)
					fallback := broadcast.NewWoCBroadcaster(d.wocBaseURL)
					d.broadcaster.Swap(broadcast.NewCompositeBroadcaster(primary, fallback, ht), "composite")
					d.healthTracker = ht
				case "mock":
					d.broadcaster.Swap(&broadcast.MockBroadcaster{}, "mock")
					d.healthTracker = nil
				}
				d.cfg.Broadcaster = newMode
				updated["broadcaster"] = newMode

				// Pool backends differ between demo (in-memory) and live (Redis) mode.
				// The broadcaster hot-swap takes effect immediately, but pools remain
				// on the original backend until restart.
				restartRequired = true
				restartReason = "Pool storage differs between demo and live mode. Restart the gateway to switch pool backends and clean up synthetic UTXOs."
			}
		}

		if len(updated) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no fields to update",
			})
			return
		}

		resp := map[string]any{
			"success": true,
			"updated": updated,
		}
		if restartRequired {
			resp["restart_required"] = true
			resp["restart_reason"] = restartReason
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleBroadcasterHealth returns the health status of broadcaster services.
// In composite mode, this reports per-service health, broadcast statistics,
// and circuit breaker state for GorillaPool and WoC.
// In non-composite modes, returns a minimal status.
func (d *DashboardAPI) handleBroadcasterHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mode := d.broadcaster.Mode()

		resp := map[string]any{
			"mode": mode,
		}

		if d.healthTracker != nil {
			resp["services"] = d.healthTracker.All()
			resp["stats"] = d.healthTracker.Stats()
			resp["circuitBreakerOpen"] = d.broadcaster.SkipPrimary()
		} else {
			// Non-composite mode — single service, no granular health tracking
			resp["services"] = map[string]any{}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// handleRevenue returns persistent settlement revenue statistics.
// Backed by Redis for persistence across restarts, with in-memory fallback.
func (d *DashboardAPI) handleRevenue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.revenueTracker == nil {
			writeJSON(w, http.StatusOK, RevenueStats{})
			return
		}
		writeJSON(w, http.StatusOK, d.revenueTracker.Stats())
	}
}

