// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package treasury

import (
	"log/slog"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// TemplateRepairConfig configures the background template repair loop.
type TemplateRepairConfig struct {
	NoncePool             TemplateRepairable // nonce pool supporting ListAvailable + UpdateTemplates
	NonceKey              *ec.PrivateKey     // signing key for template generation
	PayeeLockingScriptHex string             // hex P2PKH locking script for template output
	PriceSats             uint64             // price embedded in each template
	Interval              time.Duration      // how often to scan (e.g. 5 * time.Minute)
}

// TemplateRepairable is the subset of pool operations needed by the repair loop.
// Only Redis-backed pools implement ListAvailable, UpdateTemplates, and IsAvailable.
type TemplateRepairable interface {
	ListAvailable() ([]pool.UTXO, error)
	UpdateTemplates(utxos []pool.UTXO) error
	IsAvailable(txid string, vout uint32) bool
}

// StartTemplateRepairLoop starts a background goroutine that periodically
// scans the nonce pool for available UTXOs with missing or stale templates
// and regenerates them. This catches edge cases where UTXOs may have been
// added without templates (e.g. manual Redis imports, partial failures, or
// pre-existing UTXOs that predate Profile B activation) and handles price
// drift when TEMPLATE_PRICE_SATS changes between restarts.
//
// The repair loop only touches available UTXOs — leased UTXOs are left
// untouched to avoid interfering with in-flight challenges.
func StartTemplateRepairLoop(cfg TemplateRepairConfig, stop <-chan struct{}) {
	logger := slog.Default().With("component", "template-repair")

	logger.Info("starting template repair loop",
		"interval", cfg.Interval,
		"price_sats", cfg.PriceSats,
	)

	go func() {
		ticker := time.NewTicker(cfg.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				doTemplateRepair(cfg, logger)
			case <-stop:
				logger.Info("template repair loop stopped")
				return
			}
		}
	}()
}

// doTemplateRepair performs a single scan-and-repair cycle.
func doTemplateRepair(cfg TemplateRepairConfig, logger *slog.Logger) {
	available, err := cfg.NoncePool.ListAvailable()
	if err != nil {
		logger.Error("failed to list available nonce UTXOs", "error", err)
		return
	}

	// Collect UTXOs that need templates: missing OR stale price
	var needRepair []pool.UTXO
	missing := 0
	stalePrice := 0
	for _, u := range available {
		if u.RawTxTemplate == "" {
			missing++
			needRepair = append(needRepair, u)
		} else if u.TemplatePriceSats != cfg.PriceSats {
			stalePrice++
			needRepair = append(needRepair, u)
		}
	}

	if len(needRepair) == 0 {
		return // all healthy, no log noise
	}

	logger.Info("repairing nonce UTXO templates",
		"total_available", len(available),
		"missing", missing,
		"stale_price", stalePrice,
	)

	// Generate templates — deterministic (RFC 6979), so re-generating
	// an already-correct template produces the same bytes.
	if err := GenerateTemplates(
		cfg.NonceKey, needRepair, cfg.PayeeLockingScriptHex, cfg.PriceSats,
	); err != nil {
		logger.Error("template repair generation failed", "error", err)
		return
	}

	// Re-check availability before writing: filter out UTXOs that were
	// leased between the scan and now. This avoids unnecessary writes
	// to UTXOs involved in in-flight challenges.
	stillAvailable := needRepair[:0] // reuse backing array
	skipped := 0
	for _, u := range needRepair {
		if cfg.NoncePool.IsAvailable(u.TxID, u.Vout) {
			stillAvailable = append(stillAvailable, u)
		} else {
			skipped++
		}
	}

	if len(stillAvailable) == 0 {
		logger.Info("all repair candidates became unavailable, skipping update",
			"skipped", skipped)
		return
	}

	// Persist templates to Redis without modifying pool stats
	if err := cfg.NoncePool.UpdateTemplates(stillAvailable); err != nil {
		logger.Error("failed to store repaired templates", "error", err)
		return
	}

	logger.Info("template repair complete",
		"updated", len(stillAvailable),
		"skipped_unavailable", skipped,
	)
}
