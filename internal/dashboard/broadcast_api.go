// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package dashboard

import (
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/bsv-blockchain/go-sdk/transaction"
)

// BroadcastRequest is the request body for POST /api/v1/broadcast.
type BroadcastRequest struct {
	RawTxHex string `json:"rawtx"`
}

// handleBroadcast proxies a raw transaction through the gateway's configured broadcaster.
// This allows the dashboard testing flow to broadcast in both mock and live mode
// without the browser needing direct access to WoC or dealing with CORS.
func (d *DashboardAPI) handleBroadcast() http.HandlerFunc {
	logger := slog.Default().With("component", "dashboard.broadcast")

	return func(w http.ResponseWriter, r *http.Request) {
		var req BroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}

		if req.RawTxHex == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "rawtx is required",
			})
			return
		}

		// Decode hex to bytes
		txBytes, err := hex.DecodeString(req.RawTxHex)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid hex: " + err.Error(),
			})
			return
		}

		// Parse into transaction
		tx, err := transaction.NewTransactionFromBytes(txBytes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid transaction: " + err.Error(),
			})
			return
		}

		// Broadcast via the configured broadcaster (mock or WoC)
		success, failure := d.broadcaster.Broadcast(tx)
		if failure != nil {
			logger.Warn("broadcast failed",
				"code", failure.Code,
				"description", failure.Description,
			)
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": failure.Description,
				"code":  failure.Code,
			})
			return
		}

		logger.Info("broadcast success", "txid", success.Txid)
		writeJSON(w, http.StatusOK, map[string]any{
			"txid": success.Txid,
		})
	}
}
