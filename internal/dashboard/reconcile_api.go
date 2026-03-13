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
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// staleLeaseCutoff is the minimum age before a leased UTXO whose creating TX
// has not been confirmed can be released back to available. This buffer
// accounts for Teranode's fragmented mempool where propagation can be slow.
const staleLeaseCutoff = 24 * time.Hour

// wocUnspent matches the WoC /address/{address}/unspent response item.
type wocUnspent struct {
	Height int    `json:"height"`
	TxPos  int    `json:"tx_pos"`
	TxHash string `json:"tx_hash"`
	Value  int64  `json:"value"`
}

// wocTxInfo is a minimal parse of WoC's GET /tx/{txid} response.
// We only need blockheight to determine confirmation status.
type wocTxInfo struct {
	BlockHeight int `json:"blockheight"`
}

// reconcilablePool extends pool.Pool with methods needed for full reconciliation
// (leased UTXO inspection and release). RedisPool implements these; MemoryPool
// does too for parity but reconciliation rejects mock mode anyway.
type reconcilablePool interface {
	pool.Pool
	ListLeased() ([]pool.UTXO, error)
	ReleaseLease(txid string, vout uint32)
}

// ReconcileResult is the per-pool reconciliation outcome.
type ReconcileResult struct {
	Pool            string `json:"pool"`
	Address         string `json:"address"`
	Checked         int    `json:"checked"`          // total UTXOs examined (available + leased)
	Valid           int    `json:"valid"`             // confirmed unspent on-chain
	ZombiesRemoved  int    `json:"zombies_removed"`   // Op 2: available but confirmed spent → marked spent
	LeasedConfirmed int    `json:"leased_confirmed"`  // Op 1: leased and confirmed spent → marked spent
	StaleReleased   int    `json:"stale_released"`    // Op 3: leased > 24h, unconfirmed → released to available
	Skipped         int    `json:"skipped"`           // unconfirmed TX, left alone
	Error           string `json:"error,omitempty"`
}

// handleReconcilePools checks all pool UTXOs against the blockchain (WoC)
// and performs three operations:
//
//  1. Confirm leased → spent: leased UTXOs whose creating TX is confirmed and
//     absent from the unspent set are marked spent (normal lifecycle completion).
//  2. Remove zombies: available UTXOs whose creating TX is confirmed and absent
//     from the unspent set are marked spent (they were consumed but never went
//     through the lease/spend cycle properly).
//  3. Release stale leases: leased UTXOs older than 24h whose creating TX has
//     NOT been confirmed are released back to available (broadcast likely failed
//     and Teranode's fragmented mempool means slow propagation).
//
// Key safety rule: absence from the confirmed unspent set alone is NOT enough
// to mark a UTXO as spent. We require positive confirmation that the creating
// TX was mined (blockheight > 0). Our system is the source of truth.
func (d *DashboardAPI) handleReconcilePools() http.HandlerFunc {
	logger := slog.Default().With("component", "dashboard.reconcile")

	return func(w http.ResponseWriter, r *http.Request) {
		if d.cfg.Broadcaster == "mock" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "reconciliation requires live mode (WoC broadcaster)",
			})
			return
		}

		baseURL := d.wocBaseURL
		client := &http.Client{Timeout: 15 * time.Second}

		pools := []struct {
			name string
			pool pool.Pool
		}{
			{"nonce", d.noncePool},
			{"fee", d.feePool},
			{"payment", d.paymentPool},
		}

		results := make([]ReconcileResult, 0, len(pools))

		for _, p := range pools {
			result := reconcilePool(p.name, p.pool, baseURL, client, logger)
			results = append(results, result)
		}

		totalZombies := 0
		totalLeasedConfirmed := 0
		totalStaleReleased := 0
		for _, r := range results {
			totalZombies += r.ZombiesRemoved
			totalLeasedConfirmed += r.LeasedConfirmed
			totalStaleReleased += r.StaleReleased
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success":           true,
			"pools":             results,
			"total_zombies":     totalZombies,
			"total_confirmed":   totalLeasedConfirmed,
			"total_stale_released": totalStaleReleased,
		})
	}
}

// reconcilePool performs the three reconciliation operations for a single pool.
func reconcilePool(name string, p pool.Pool, baseURL string, client *http.Client, logger *slog.Logger) ReconcileResult {
	addr := p.Address()
	if addr == "" {
		return ReconcileResult{Pool: name, Error: "pool has no address"}
	}

	result := ReconcileResult{Pool: name, Address: addr}

	// ── Gather pool state ───────────────────────────────────────────────

	available, err := p.ListAvailable()
	if err != nil {
		result.Error = fmt.Sprintf("ListAvailable failed: %s", err)
		logger.Error("reconcile: ListAvailable failed", "pool", name, "error", err)
		return result
	}

	// Try to get leased UTXOs (requires reconcilablePool)
	var leased []pool.UTXO
	rp, hasLeaseSupport := p.(reconcilablePool)
	if hasLeaseSupport {
		leased, err = rp.ListLeased()
		if err != nil {
			logger.Warn("reconcile: ListLeased failed, skipping lease operations",
				"pool", name, "error", err)
			leased = nil
		}
	}

	result.Checked = len(available) + len(leased)
	if result.Checked == 0 {
		logger.Info("reconcile: pool empty", "pool", name)
		return result
	}

	// ── Fetch confirmed unspent set from WoC ────────────────────────────

	onChain, err := fetchUnspent(baseURL, addr, client)
	if err != nil {
		result.Error = fmt.Sprintf("WoC fetch failed: %s", err)
		logger.Error("reconcile: WoC fetch failed", "pool", name, "address", addr, "error", err)
		return result
	}

	onChainSet := make(map[string]bool, len(onChain))
	for _, u := range onChain {
		key := fmt.Sprintf("%s:%d", u.TxHash, u.TxPos)
		onChainSet[key] = true
	}

	// ── Collect txids that need confirmation check ──────────────────────
	// Only UTXOs NOT in the confirmed unspent set need a TX status lookup.

	txidsToCheck := make(map[string]bool)

	type pendingUTXO struct {
		utxo     pool.UTXO
		isLeased bool
	}
	var pending []pendingUTXO

	for _, u := range available {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if onChainSet[key] {
			result.Valid++
		} else {
			txidsToCheck[u.TxID] = true
			pending = append(pending, pendingUTXO{utxo: u, isLeased: false})
		}
	}

	for _, u := range leased {
		key := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
		if onChainSet[key] {
			result.Valid++
		} else {
			txidsToCheck[u.TxID] = true
			pending = append(pending, pendingUTXO{utxo: u, isLeased: true})
		}
	}

	// ── Check TX confirmation status ────────────────────────────────────

	txids := make([]string, 0, len(txidsToCheck))
	for txid := range txidsToCheck {
		txids = append(txids, txid)
	}

	confirmed := fetchTxConfirmations(baseURL, txids, client, logger)

	// ── Apply the three operations ──────────────────────────────────────

	now := time.Now()

	for _, pu := range pending {
		txConfirmed := confirmed[pu.utxo.TxID]

		if txConfirmed {
			// Creating TX is confirmed AND UTXO is not in unspent set
			// → the UTXO has been consumed on-chain.
			p.MarkSpent(pu.utxo.TxID, pu.utxo.Vout)

			outpoint := fmt.Sprintf("%s:%d", pu.utxo.TxID, pu.utxo.Vout)
			if pu.isLeased {
				// Op 1: leased → spent (normal lifecycle completion)
				result.LeasedConfirmed++
				logger.Info("reconcile: confirmed leased UTXO spent",
					"pool", name, "outpoint", outpoint)
			} else {
				// Op 2: available → spent (zombie)
				result.ZombiesRemoved++
				logger.Info("reconcile: removed zombie UTXO",
					"pool", name, "outpoint", outpoint)
			}
		} else {
			// Creating TX is NOT confirmed — still in mempool or unknown.
			if pu.isLeased && hasLeaseSupport && !pu.utxo.LeasedAt.IsZero() &&
				now.Sub(pu.utxo.LeasedAt) > staleLeaseCutoff {
				// Op 3: stale lease — leased > 24h, TX never confirmed
				rp.ReleaseLease(pu.utxo.TxID, pu.utxo.Vout)
				result.StaleReleased++
				outpoint := fmt.Sprintf("%s:%d", pu.utxo.TxID, pu.utxo.Vout)
				logger.Warn("reconcile: released stale lease",
					"pool", name, "outpoint", outpoint,
					"leased_at", pu.utxo.LeasedAt)
			} else {
				// Unconfirmed, not stale — leave alone
				result.Skipped++
			}
		}
	}

	logger.Info("reconcile complete",
		"pool", name,
		"address", addr,
		"checked", result.Checked,
		"valid", result.Valid,
		"zombies_removed", result.ZombiesRemoved,
		"leased_confirmed", result.LeasedConfirmed,
		"stale_released", result.StaleReleased,
		"skipped", result.Skipped,
		"on_chain_utxos", len(onChain),
		"txids_checked", len(txids),
	)

	return result
}

// fetchTxConfirmations checks whether each txid has been confirmed (mined).
// Returns a map where true = blockheight > 0 (confirmed), false = unconfirmed/unknown.
// Deduplication should be done by the caller; this function checks each txid once.
func fetchTxConfirmations(baseURL string, txids []string, client *http.Client, logger *slog.Logger) map[string]bool {
	result := make(map[string]bool, len(txids))

	for i, txid := range txids {
		// Rate limit: small delay between requests to avoid 429
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}

		confirmed, err := fetchTxConfirmed(baseURL, txid, client)
		if err != nil {
			logger.Warn("reconcile: TX status check failed, treating as unconfirmed",
				"txid", txid, "error", err)
			result[txid] = false
			continue
		}
		result[txid] = confirmed
	}

	return result
}

// fetchTxConfirmed queries WoC GET /tx/{txid} and returns true if the
// transaction has been mined (blockheight > 0).
func fetchTxConfirmed(baseURL, txid string, client *http.Client) (bool, error) {
	url := baseURL + "/tx/" + txid

	const maxRetries = 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		resp, err := client.Get(url)
		if err != nil {
			return false, fmt.Errorf("WoC request failed: %w", err)
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return false, fmt.Errorf("read response body: %w", err)
		}

		switch resp.StatusCode {
		case http.StatusOK:
			var info wocTxInfo
			if err := json.Unmarshal(body, &info); err != nil {
				return false, fmt.Errorf("parse TX response: %w", err)
			}
			// blockheight > 0 means confirmed; 0 or -1 means unconfirmed/mempool
			return info.BlockHeight > 0, nil

		case http.StatusNotFound:
			// TX not known to WoC at all — treat as unconfirmed
			return false, nil

		case http.StatusTooManyRequests:
			// Rate limited — back off and retry
			if attempt < maxRetries-1 {
				time.Sleep(2 * time.Second)
				continue
			}
			return false, fmt.Errorf("WoC rate limited (429) after %d retries", maxRetries)

		default:
			return false, fmt.Errorf("WoC returned HTTP %d: %s", resp.StatusCode, string(body))
		}
	}

	return false, fmt.Errorf("exhausted retries")
}

// fetchUnspent queries WoC for all confirmed unspent UTXOs at an address.
func fetchUnspent(baseURL, address string, client *http.Client) ([]wocUnspent, error) {
	url := baseURL + "/address/" + address + "/unspent"

	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("WoC request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// 404 = address with no history → empty
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("WoC rate limited (429) — try again in a few seconds")
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WoC returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var items []wocUnspent
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("parse WoC response: %w", err)
	}

	return items, nil
}
