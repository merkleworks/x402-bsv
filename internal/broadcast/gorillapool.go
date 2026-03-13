// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package broadcast

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/transaction"
	sdkBroadcaster "github.com/bsv-blockchain/go-sdk/transaction/broadcaster"
)

// DefaultArcURL is the default GorillaPool ARC endpoint (versioned root).
// The SDK appends "/tx" for broadcast and "/tx/{txid}" for status, so
// ApiUrl must include the version prefix.
const DefaultArcURL = "https://arc.gorillapool.io/v1"

// GorillaPoolBroadcaster wraps the Go SDK's Arc broadcaster and adds
// MempoolChecker support by mapping ARC tx-status values to the
// (visible, doubleSpend) tuple expected by the gatekeeper.
//
// ARC only tracks transactions that have been submitted through its own
// pipeline. Transactions broadcast elsewhere or very old confirmed txs
// return 404 — the composite broadcaster handles this by falling back
// to WoC for status checks.
type GorillaPoolBroadcaster struct {
	arc    *sdkBroadcaster.Arc
	logger *slog.Logger
}

// NewGorillaPoolBroadcaster creates a broadcaster that submits transactions
// to GorillaPool's ARC endpoint and checks tx status via the same service.
//
// arcURL must include the version prefix (e.g. "https://arc.gorillapool.io/v1").
// apiKey is optional — GorillaPool currently does not require authentication.
func NewGorillaPoolBroadcaster(arcURL, apiKey string) *GorillaPoolBroadcaster {
	if arcURL == "" {
		arcURL = DefaultArcURL
	}
	return &GorillaPoolBroadcaster{
		arc: &sdkBroadcaster.Arc{
			ApiUrl: arcURL,
			ApiKey: apiKey,
			Client: &http.Client{Timeout: 30 * time.Second},
		},
		logger: slog.Default().With("component", "gorillapool-broadcaster"),
	}
}

// Broadcast delegates to the SDK Arc.Broadcast() which sends the raw
// transaction bytes via POST /tx with Content-Type: application/octet-stream.
func (g *GorillaPoolBroadcaster) Broadcast(tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return g.arc.Broadcast(tx)
}

// BroadcastCtx is the context-aware version.
func (g *GorillaPoolBroadcaster) BroadcastCtx(ctx context.Context, tx *transaction.Transaction) (*transaction.BroadcastSuccess, *transaction.BroadcastFailure) {
	return g.arc.BroadcastCtx(ctx, tx)
}

// CheckMempool calls ARC's GET /tx/{txid} status endpoint and maps the
// returned txStatus to the (visible, doubleSpend, error) tuple expected
// by the gatekeeper's MempoolChecker interface.
//
// Status mapping:
//
//	SEEN_ON_NETWORK, MINED, CONFIRMED          → visible=true
//	QUEUED, RECEIVED, STORED, ANNOUNCED,
//	  REQUESTED, SENT, ACCEPTED                → visible=false (pending)
//	DOUBLE_SPEND_ATTEMPTED                     → doubleSpend=true
//	SEEN_IN_ORPHAN_MEMPOOL                     → visible=false
//	REJECTED                                   → error (invalid tx)
//	HTTP 404 / status not tracked              → visible=false (not in ARC)
func (g *GorillaPoolBroadcaster) CheckMempool(txid string) (bool, bool, error) {
	resp, err := g.arc.Status(txid)
	if err != nil {
		// Check if this is a network/transport error vs a structured 404
		errStr := err.Error()
		if strings.Contains(errStr, "404") || strings.Contains(errStr, "not found") {
			// ARC doesn't track this tx — not an error, just not visible here
			g.logger.Debug("tx not tracked by ARC", "txid", txid)
			return false, false, nil
		}
		return false, false, fmt.Errorf("ARC status check failed: %w", err)
	}

	// Handle HTTP-level error status (ARC returns status codes in the JSON body)
	if resp.Status == 404 {
		g.logger.Debug("tx not found in ARC", "txid", txid)
		return false, false, nil
	}

	// Map txStatus string to mempool visibility
	if resp.TxStatus == nil {
		// No status returned — treat as not visible
		g.logger.Debug("ARC returned no txStatus", "txid", txid, "httpStatus", resp.Status)
		return false, false, nil
	}

	status := string(*resp.TxStatus)
	switch status {
	// Visible states — tx is confirmed in mempool or mined
	case "SEEN_ON_NETWORK", "MINED", "CONFIRMED":
		return true, false, nil

	// Pending states — tx received by ARC but not yet visible on network
	case "QUEUED", "RECEIVED", "STORED",
		"ANNOUNCED_TO_NETWORK", "REQUESTED_BY_NETWORK",
		"SENT_TO_NETWORK", "ACCEPTED_BY_NETWORK":
		return false, false, nil

	// Double-spend detected
	case "DOUBLE_SPEND_ATTEMPTED":
		return false, true, nil

	// Orphan mempool — not usable
	case "SEEN_IN_ORPHAN_MEMPOOL":
		return false, false, nil

	// Rejected by miner — invalid transaction
	case "REJECTED":
		detail := ""
		if resp.Detail != nil {
			detail = *resp.Detail
		}
		return false, false, fmt.Errorf("ARC rejected transaction: %s (extra: %s)", detail, resp.ExtraInfo)

	default:
		// Unknown status — log and treat as not visible
		g.logger.Warn("unknown ARC txStatus", "txid", txid, "status", status)
		return false, false, nil
	}
}
