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
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/treasury"
)

// TreasuryInfoResponse provides treasury address and derivation details.
type TreasuryInfoResponse struct {
	Address        string `json:"address"`
	Network        string `json:"network"`
	Broadcaster    string `json:"broadcaster"`
	KeyMode        string `json:"keyMode"` // "xpriv" or "wif"
	DerivationPath string `json:"derivationPath,omitempty"`
	NoncePool      any    `json:"noncePool"`
	FeePool        any    `json:"feePool"`
	PaymentPool    any    `json:"paymentPool"`
}

// FanoutRequest is the request body for POST /api/v1/treasury/fanout.
type FanoutRequest struct {
	Pool            string `json:"pool"`            // "nonce", "fee", or "payment"
	Count           int    `json:"count"`           // number of UTXOs to create
	FundingTxID     string `json:"fundingTxid"`     // txid of funding UTXO
	FundingVout     uint32 `json:"fundingVout"`     // vout of funding UTXO
	FundingScript   string `json:"fundingScript"`   // locking script hex of funding UTXO
	FundingSatoshis uint64 `json:"fundingSatoshis"` // value of funding UTXO
	SigningKey      string `json:"signingKey"`      // optional: "treasury", "fee", or "payment" (default: treasury)
}

// FanoutResponse is the response from a successful fan-out operation.
type FanoutResponse struct {
	Success   bool   `json:"success"`
	TxID      string `json:"txid"`
	UTXOCount int    `json:"utxoCount"`
	Pool      string `json:"pool"`
}

// FanoutHistoryEntry records a past fan-out operation.
type FanoutHistoryEntry struct {
	TxID      string    `json:"txid"`
	Pool      string    `json:"pool"`
	Count     int       `json:"count"`
	Timestamp time.Time `json:"timestamp"`
}

// fanoutHistory is a thread-safe, bounded history of fan-out operations.
type fanoutHistory struct {
	mu      sync.Mutex
	entries []FanoutHistoryEntry
	maxSize int
}

var history = &fanoutHistory{maxSize: 100}

func (fh *fanoutHistory) add(entry FanoutHistoryEntry) {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	fh.entries = append(fh.entries, entry)
	if len(fh.entries) > fh.maxSize {
		fh.entries = fh.entries[len(fh.entries)-fh.maxSize:]
	}
}

func (fh *fanoutHistory) list() []FanoutHistoryEntry {
	fh.mu.Lock()
	defer fh.mu.Unlock()
	out := make([]FanoutHistoryEntry, len(fh.entries))
	copy(out, fh.entries)
	return out
}

// TreasuryUTXOResponse is the response from GET /api/v1/treasury/utxos.
type TreasuryUTXOResponse struct {
	UTXOs    []TreasuryUTXO `json:"utxos"`
	LastPoll string         `json:"lastPoll,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// TreasuryUTXO represents an unspent UTXO at the treasury address.
type TreasuryUTXO struct {
	TxID     string `json:"txid"`
	Vout     uint32 `json:"vout"`
	Script   string `json:"script"`
	Satoshis uint64 `json:"satoshis"`
	Status   string `json:"status,omitempty"` // "confirmed" or "mempool"
}

// handleTreasuryUTXOs returns unspent UTXOs at the treasury address,
// including both confirmed and mempool UTXOs with their status.
func (d *DashboardAPI) handleTreasuryUTXOs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.watcher == nil {
			writeJSON(w, http.StatusOK, TreasuryUTXOResponse{
				UTXOs: []TreasuryUTXO{},
				Error: "treasury watcher not configured",
			})
			return
		}

		utxos := d.watcher.GetUTXOsWithStatus()
		lastPoll, lastErr := d.watcher.LastPoll()

		resp := TreasuryUTXOResponse{
			UTXOs: make([]TreasuryUTXO, len(utxos)),
		}
		if !lastPoll.IsZero() {
			resp.LastPoll = lastPoll.Format(time.RFC3339)
		}
		if lastErr != nil {
			resp.Error = lastErr.Error()
		}
		for i, u := range utxos {
			resp.UTXOs[i] = TreasuryUTXO{
				TxID:     u.TxID,
				Vout:     u.Vout,
				Script:   u.Script,
				Satoshis: u.Satoshis,
				Status:   string(u.Status),
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleTreasuryInfo returns treasury address and pool status.
func (d *DashboardAPI) handleTreasuryInfo() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keyMode := "wif"
		derivationPath := ""
		if d.cfg.XPRIV != "" {
			keyMode = "xpriv"
			derivationPath = "m/402'/0'/2'"
		}

		resp := TreasuryInfoResponse{
			Address:        d.keys.TreasuryAddress,
			Network:        d.cfg.BSVNetwork,
			Broadcaster:    d.broadcaster.Mode(),
			KeyMode:        keyMode,
			DerivationPath: derivationPath,
			NoncePool:      d.noncePool.Stats(),
			FeePool:        d.feePool.Stats(),
			PaymentPool:    d.paymentPool.Stats(),
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// handleTreasuryFanout triggers a manual fan-out operation.
func (d *DashboardAPI) handleTreasuryFanout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req FanoutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}

		// Validate pool selection
		if req.Pool != "nonce" && req.Pool != "fee" && req.Pool != "payment" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "pool must be 'nonce', 'fee', or 'payment'",
			})
			return
		}
		if req.Count < 1 || req.Count > 10000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "count must be between 1 and 10000",
			})
			return
		}
		if req.FundingTxID == "" || req.FundingScript == "" || req.FundingSatoshis == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "fundingTxid, fundingScript, and fundingSatoshis are required",
			})
			return
		}

		// Determine signing key (default: treasury)
		signingKey := d.keys.TreasuryKey
		switch req.SigningKey {
		case "nonce":
			signingKey = d.keys.NonceKey
		case "fee":
			signingKey = d.keys.FeeKey
		case "payment":
			signingKey = d.keys.PaymentKey
		case "treasury", "":
			signingKey = d.keys.TreasuryKey
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "signingKey must be 'treasury', 'nonce', 'fee', or 'payment'",
			})
			return
		}

		// Determine target address and output denomination for each pool
		var targetAddr string
		var outputSats uint64 = 1 // default for nonce pool
		switch req.Pool {
		case "nonce":
			targetAddr = d.keys.NonceAddress
		case "fee":
			targetAddr = d.keys.FeeAddress
			outputSats = d.cfg.FeeUTXOSats // from FEE_UTXO_SATS env var (1–1000)
		case "payment":
			targetAddr = d.keys.PaymentAddress
			outputSats = 100 // payment pool uses 100-sat UTXOs
		}

		// Lease the funding UTXO to prevent double-spend
		if d.watcher != nil {
			if err := d.watcher.LeaseFundingExplicit(req.FundingTxID, req.FundingVout, "fanout"); err != nil {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error": fmt.Sprintf("cannot lease funding UTXO: %s", err),
				})
				return
			}
		}

		// Build and broadcast fan-out transaction.
		// Change returns to the treasury address so it remains accessible
		// for future fan-outs (not stranded in the pool address).
		result, err := treasury.BuildFanout(
			signingKey,
			d.mainnet,
			treasury.FanoutRequest{
				FundingTxID:     req.FundingTxID,
				FundingVout:     req.FundingVout,
				FundingScript:   req.FundingScript,
				FundingSatoshis: req.FundingSatoshis,
				OutputCount:     req.Count,
				FeeRate:         d.cfg.FeeRate,
				TargetAddress:   targetAddr,
				ChangeAddress:   d.keys.TreasuryAddress,
				OutputSatoshis:  outputSats,
			},
			d.broadcaster,
		)
		if err != nil {
			// Broadcast failed — release the lease so the UTXO is available again
			if d.watcher != nil {
				d.watcher.ReleaseLease(req.FundingTxID, req.FundingVout)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": fmt.Sprintf("fan-out failed: %s", err),
			})
			return
		}

		// Successful broadcast — consume the lease and register change output
		if d.watcher != nil {
			d.watcher.ConsumeLease(req.FundingTxID, req.FundingVout)
			d.watcher.RegisterMempool(result.ChangeUTXO)
		}

		// Profile B: generate templates for nonce pool UTXOs before adding to pool.
		// Templates are derived artifacts — they must be created whenever new nonce
		// UTXOs are minted, so the pool always contains complete UTXO+template pairs.
		if req.Pool == "nonce" && d.cfg.TemplateMode {
			payeeScript, err := derivePayeeLockingScriptHex(d.payeeAddr)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": fmt.Sprintf("failed to derive payee script for templates: %s", err),
				})
				return
			}

			if err := treasury.GenerateTemplates(
				d.keys.NonceKey, result.UTXOs, payeeScript, d.cfg.TemplatePriceSats,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": fmt.Sprintf("template generation failed: %s", err),
				})
				return
			}
			slog.Info("generated templates for fanout nonce UTXOs",
				"count", len(result.UTXOs),
				"price_sats", d.cfg.TemplatePriceSats)
		}

		// Add new UTXOs to the appropriate pool
		var targetPool pool.Pool
		switch req.Pool {
		case "nonce":
			targetPool = d.noncePool
		case "fee":
			targetPool = d.feePool
		case "payment":
			targetPool = d.paymentPool
		}
		targetPool.AddExisting(result.UTXOs)

		// Record in history
		history.add(FanoutHistoryEntry{
			TxID:      result.TxID,
			Pool:      req.Pool,
			Count:     len(result.UTXOs),
			Timestamp: time.Now(),
		})

		writeJSON(w, http.StatusOK, FanoutResponse{
			Success:   true,
			TxID:      result.TxID,
			UTXOCount: len(result.UTXOs),
			Pool:      req.Pool,
		})
	}
}

// handleTreasuryHistory returns the list of past fan-out operations.
func (d *DashboardAPI) handleTreasuryHistory() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries := history.list()
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"history": entries,
		})
	}
}

// SweepRequest is the request body for POST /api/v1/treasury/sweep.
type SweepRequestAPI struct {
	SigningKey string              `json:"signingKey"` // "nonce", "fee", "payment", or "treasury"
	Inputs     []treasury.SweepInput `json:"inputs"`   // UTXOs to sweep
}

// handleTreasurySweep sweeps UTXOs from a pool address back to the treasury.
func (d *DashboardAPI) handleTreasurySweep() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req SweepRequestAPI
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}

		if len(req.Inputs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "at least one input is required",
			})
			return
		}

		// Determine signing key
		var signingKey = d.keys.TreasuryKey
		switch req.SigningKey {
		case "nonce":
			signingKey = d.keys.NonceKey
		case "fee":
			signingKey = d.keys.FeeKey
		case "payment":
			signingKey = d.keys.PaymentKey
		case "treasury", "":
			signingKey = d.keys.TreasuryKey
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "signingKey must be 'treasury', 'nonce', 'fee', or 'payment'",
			})
			return
		}

		// Lease all sweep inputs to prevent double-spend
		if d.watcher != nil {
			for _, inp := range req.Inputs {
				if err := d.watcher.LeaseFundingExplicit(inp.TxID, inp.Vout, "sweep"); err != nil {
					// Release any leases already acquired for this sweep
					for _, prev := range req.Inputs {
						if prev.TxID == inp.TxID && prev.Vout == inp.Vout {
							break // reached the failed one
						}
						d.watcher.ReleaseLease(prev.TxID, prev.Vout)
					}
					writeJSON(w, http.StatusConflict, map[string]any{
						"error": fmt.Sprintf("cannot lease sweep input %s:%d: %s", inp.TxID, inp.Vout, err),
					})
					return
				}
			}
		}

		result, err := treasury.BuildSweep(
			signingKey,
			d.mainnet,
			treasury.SweepRequest{
				Inputs:      req.Inputs,
				Destination: d.keys.TreasuryAddress,
				FeeRate:     d.cfg.FeeRate,
			},
			d.broadcaster,
		)
		if err != nil {
			// Broadcast failed — release all leases
			if d.watcher != nil {
				for _, inp := range req.Inputs {
					d.watcher.ReleaseLease(inp.TxID, inp.Vout)
				}
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": fmt.Sprintf("sweep failed: %s", err),
			})
			return
		}

		// Successful broadcast — consume all leases and register output
		if d.watcher != nil {
			for _, inp := range req.Inputs {
				d.watcher.ConsumeLease(inp.TxID, inp.Vout)
			}
			d.watcher.RegisterMempool(result.OutputUTXO)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"txid":       result.TxID,
			"inputSats":  result.InputSats,
			"outputSats": result.OutputSats,
			"fee":        result.Fee,
		})
	}
}

// handleSweepRevenue sweeps settlement revenue UTXOs back to the treasury address.
//
// Settlement payments land at the payee address (PAYEE_ADDRESS or the fee address
// by default). This handler reads tracked settlement UTXOs directly from the
// RevenueTracker — no WoC/indexer query needed.
//
// Flow:
//  1. Pull unswept UTXOs from the revenue tracker (primary — always works)
//  2. If none tracked, fall back to WoC query (handles pre-upgrade settlements)
//  3. Sign with the correct key for the payee address
//  4. After successful broadcast, mark UTXOs as swept in the tracker
func (d *DashboardAPI) handleSweepRevenue() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Determine the signing key for the payee address
		signingKey := d.keys.PaymentKey // default
		switch d.payeeAddr {
		case d.keys.FeeAddress:
			signingKey = d.keys.FeeKey
		case d.keys.PaymentAddress:
			signingKey = d.keys.PaymentKey
		case d.keys.NonceAddress:
			signingKey = d.keys.NonceKey
		case d.keys.TreasuryAddress:
			signingKey = d.keys.TreasuryKey
		}

		// Primary: get unswept settlement UTXOs from the revenue tracker.
		// These are recorded at settlement time — no indexer query needed.
		var inputs []treasury.SweepInput
		var trackedOutpoints []string // for MarkSwept after success

		if d.revenueTracker != nil {
			unswept := d.revenueTracker.ListUnsweptUTXOs()
			for _, u := range unswept {
				inputs = append(inputs, treasury.SweepInput{
					TxID:     u.TxID,
					Vout:     u.Vout,
					Script:   u.Script,
					Satoshis: u.Satoshis,
				})
				trackedOutpoints = append(trackedOutpoints, fmt.Sprintf("%s:%d", u.TxID, u.Vout))
			}
		}

		// Fallback: if no tracked UTXOs, try WoC for pre-upgrade settlements.
		// This path is only needed for settlements recorded before UTXO tracking
		// was added. Once all old settlements are swept, this path is never hit.
		if len(inputs) == 0 {
			minSats := uint64(d.cfg.TemplatePriceSats)
			if minSats == 0 {
				minSats = 10
			}

			wocUTXOs, err := fetchPayeeUnspent(d.payeeAddr, d.wocBaseURL)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "no tracked settlement UTXOs to sweep (pre-upgrade settlements require WoC, which is currently unavailable)",
				})
				return
			}

			payeeScriptHex, err := derivePayeeLockingScriptHex(d.payeeAddr)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": fmt.Sprintf("failed to derive payee script: %s", err),
				})
				return
			}

			for _, u := range wocUTXOs {
				if uint64(u.Value) >= minSats {
					inputs = append(inputs, treasury.SweepInput{
						TxID:     u.TxHash,
						Vout:     uint32(u.TxPos),
						Script:   payeeScriptHex,
						Satoshis: uint64(u.Value),
					})
				}
			}
		}

		if len(inputs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no settlement UTXOs available to sweep",
			})
			return
		}

		result, err := treasury.BuildSweep(
			signingKey,
			d.mainnet,
			treasury.SweepRequest{
				Inputs:      inputs,
				Destination: d.keys.TreasuryAddress,
				FeeRate:     d.cfg.FeeRate,
			},
			d.broadcaster,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": fmt.Sprintf("sweep failed: %s", err),
			})
			return
		}

		// Register the sweep output as a mempool UTXO in the treasury watcher
		if d.watcher != nil {
			d.watcher.RegisterMempool(result.OutputUTXO)
		}

		// Mark swept UTXOs in the revenue tracker so they're not swept again
		if d.revenueTracker != nil && len(trackedOutpoints) > 0 {
			d.revenueTracker.MarkSwept(trackedOutpoints)
		}

		// Also mark in payment pool if applicable
		if d.payeeAddr == d.keys.PaymentAddress {
			for _, inp := range inputs {
				d.paymentPool.MarkSpent(inp.TxID, inp.Vout)
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":    true,
			"txid":       result.TxID,
			"inputCount": len(inputs),
			"inputSats":  result.InputSats,
			"outputSats": result.OutputSats,
			"fee":        result.Fee,
		})
	}
}

// wocUnspentItem matches the WoC /address/{addr}/unspent JSON response.
type wocUnspentItem struct {
	TxHash string `json:"tx_hash"`
	TxPos  int    `json:"tx_pos"`
	Value  int64  `json:"value"`
	Height int    `json:"height"`
}

// fetchPayeeUnspent queries WoC for unspent UTXOs at the given address.
func fetchPayeeUnspent(address string, baseURL string) ([]wocUnspentItem, error) {
	url := baseURL + "/address/" + address + "/unspent"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("WoC request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WoC returned HTTP %d", resp.StatusCode)
	}

	var items []wocUnspentItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return items, nil
}

// derivePayeeLockingScriptHex converts a BSV address to a hex P2PKH locking script.
func derivePayeeLockingScriptHex(addr string) (string, error) {
	a, err := script.NewAddressFromString(addr)
	if err != nil {
		return "", fmt.Errorf("parse address %q: %w", addr, err)
	}
	s, err := p2pkh.Lock(a)
	if err != nil {
		return "", fmt.Errorf("create locking script: %w", err)
	}
	return hex.EncodeToString(*s), nil
}
