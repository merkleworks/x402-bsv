// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
	"github.com/redis/go-redis/v9"

	"github.com/merkleworks/x402-bsv/internal/broadcast"
	"github.com/merkleworks/x402-bsv/internal/config"
	"github.com/merkleworks/x402-bsv/internal/dashboard"
	"github.com/merkleworks/x402-bsv/internal/delegator"
	"github.com/merkleworks/x402-bsv/internal/feedelegator"
	"github.com/merkleworks/x402-bsv/internal/gatekeeper"
	"github.com/merkleworks/x402-bsv/internal/hdwallet"
	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/pricing"
	"github.com/merkleworks/x402-bsv/internal/replay"
	"github.com/merkleworks/x402-bsv/internal/treasury"
)

// writeJSON encodes a value as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// wocUnspentItem matches the WoC /address/{addr}/unspent JSON response.
type wocUnspentItem struct {
	TxHash string `json:"tx_hash"`
	TxPos  int    `json:"tx_pos"`
	Value  int64  `json:"value"`
	Height int    `json:"height"`
}

// fetchWoCUnspentItems queries a WoC-compatible API for all unspent UTXOs at
// the given address and returns the raw items with full details.
func fetchWoCUnspentItems(address string, baseURL string) ([]wocUnspentItem, error) {
	url := baseURL + "/address/" + address + "/unspent"

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("WoC request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// WoC returns 404 for addresses with no history
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("WoC returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var items []wocUnspentItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("parse WoC response: %w", err)
	}
	return items, nil
}

// fetchWoCUnspentSet queries a WoC-compatible API for all unspent UTXOs at the
// given address and returns a set of "txid:vout" strings. Used for zombie nonce
// detection at startup.
func fetchWoCUnspentSet(address string, baseURL string) (map[string]bool, error) {
	items, err := fetchWoCUnspentItems(address, baseURL)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[fmt.Sprintf("%s:%d", item.TxHash, item.TxPos)] = true
	}
	return set, nil
}

func main() {
	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	logger := slog.Default()

	// Load config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	logger.Info("config loaded",
		"network", cfg.BSVNetwork,
		"port", cfg.Port,
		"pool_size", cfg.PoolSize,
		"fee_rate", cfg.FeeRate,
	)

	mainnet := cfg.IsMainnet()

	// Derive keys — xPriv (HD wallet) or WIF (legacy single-key)
	var keys *hdwallet.DerivedKeys
	if cfg.XPRIV != "" {
		keys, err = hdwallet.DeriveFromXPriv(cfg.XPRIV, mainnet)
		if err != nil {
			logger.Error("invalid XPRIV", "error", err)
			os.Exit(1)
		}
		logger.Info("HD wallet mode (xPriv)",
			"nonce_address", keys.NonceAddress,
			"fee_address", keys.FeeAddress,
			"payment_address", keys.PaymentAddress,
			"treasury_address", keys.TreasuryAddress,
		)
	} else {
		keys, err = hdwallet.DeriveFromWIF(cfg.BSVPrivateKey, mainnet)
		if err != nil {
			logger.Error("invalid BSV_PRIVATE_KEY", "error", err)
			os.Exit(1)
		}
		logger.Info("single-key mode (WIF)", "address", keys.FeeAddress)
	}

	// Fee key is used by delegator and fee delegator
	key := keys.FeeKey

	// Compute WoC base URL once from config (configurable via WOC_API_URL env var)
	wocBaseURL := cfg.WocBaseURL()
	logger.Info("WoC API base URL", "url", wocBaseURL)

	// Select broadcaster based on config (wrapped in Swappable for hot-swap via dashboard)
	var inner transaction.Broadcaster
	var healthTracker *broadcast.HealthTracker
	demoMode := false
	switch cfg.Broadcaster {
	case "woc":
		inner = broadcast.NewWoCBroadcaster(wocBaseURL)
	case "composite":
		// GorillaPool ARC primary + WoC fallback
		healthTracker = broadcast.NewHealthTracker()
		primary := broadcast.NewGorillaPoolBroadcaster(cfg.ArcURL, cfg.ArcAPIKey)
		fallback := broadcast.NewWoCBroadcaster(wocBaseURL)
		inner = broadcast.NewCompositeBroadcaster(primary, fallback, healthTracker)
		logger.Info("composite broadcaster configured",
			"primary", "GorillaPool ARC",
			"fallback", "WhatsOnChain",
			"arc_url", cfg.ArcURL,
		)
	case "mock":
		inner = &broadcast.MockBroadcaster{}
		demoMode = true
	default:
		logger.Error("unsupported BROADCASTER value", "value", cfg.Broadcaster)
		os.Exit(1)
	}
	bcast := broadcast.NewSwappable(inner, cfg.Broadcaster)

	// Connect to Redis if enabled (needed for pools and/or treasury watcher)
	var rdb *redis.Client
	if cfg.RedisEnabled {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			logger.Error("invalid REDIS_URL", "error", err)
			os.Exit(1)
		}
		rdb = redis.NewClient(opts)

		if err := rdb.Ping(context.Background()).Err(); err != nil {
			logger.Error("cannot connect to Redis", "url", cfg.RedisURL, "error", err)
			os.Exit(1)
		}
		logger.Info("connected to Redis", "url", cfg.RedisURL)
	}

	// Create UTXO pools — Redis if enabled (both mock and live), else in-memory.
	// Mode-segregated namespaces ensure mock and live data never intersect in Redis.
	//
	// Three pools:
	//   - Nonce pool: 1-sat UTXOs for replay protection (each challenge binds to one)
	//   - Fee pool: 1-sat UTXOs for miner fees
	//   - Payment pool: 100-sat UTXOs for service payments
	mode := cfg.RuntimeMode()
	var noncePool, feePool, paymentPool pool.Pool

	if cfg.RedisEnabled {
		// Redis-backed pools with mode-namespaced keys (e.g. "live:nonce:", "mock:fee:")
		np, err := pool.NewRedisPool(rdb, config.PoolPrefix(mode, "nonce"), keys.NonceKey, mainnet, cfg.LeaseTTL)
		if err != nil {
			logger.Error("failed to create nonce pool", "error", err)
			os.Exit(1)
		}
		noncePool = np

		fp, err := pool.NewRedisPool(rdb, config.PoolPrefix(mode, "fee"), keys.FeeKey, mainnet, cfg.LeaseTTL)
		if err != nil {
			logger.Error("failed to create fee pool", "error", err)
			os.Exit(1)
		}
		feePool = fp

		pp, err := pool.NewRedisPool(rdb, config.PoolPrefix(mode, "payment"), keys.PaymentKey, mainnet, cfg.LeaseTTL)
		if err != nil {
			logger.Error("failed to create payment pool", "error", err)
			os.Exit(1)
		}
		paymentPool = pp

		// Profile B: generate templates for nonce UTXOs that don't have them yet
		if !demoMode && cfg.TemplateMode {
			seedPayeeAddr := cfg.PayeeAddress
			if seedPayeeAddr == "" {
				seedPayeeAddr = keys.FeeAddress
			}
			seedPayeeScript, err := addressToLockingScriptHex(seedPayeeAddr)
			if err != nil {
				logger.Error("failed to derive payee script for template generation", "error", err)
				os.Exit(1)
			}

			// List available nonce UTXOs and generate templates for any that lack one
			available, err := np.ListAvailable()
			if err != nil {
				logger.Error("failed to list nonce UTXOs for template generation", "error", err)
				os.Exit(1)
			}

			var needTemplates []pool.UTXO
			missingCount := 0
			stalePriceCount := 0
			for _, u := range available {
				if u.RawTxTemplate == "" {
					missingCount++
					needTemplates = append(needTemplates, u)
				} else if u.TemplatePriceSats != cfg.TemplatePriceSats {
					stalePriceCount++
					needTemplates = append(needTemplates, u)
				}
			}

			if len(needTemplates) > 0 {
				logger.Info("generating templates for existing nonce UTXOs",
					"total_available", len(available),
					"missing", missingCount,
					"stale_price", stalePriceCount,
					"price_sats", cfg.TemplatePriceSats)

				if err := treasury.GenerateTemplates(keys.NonceKey, needTemplates, seedPayeeScript, cfg.TemplatePriceSats); err != nil {
					logger.Error("template generation failed", "error", err)
					os.Exit(1)
				}

				// Write template metadata to Redis (without inflating pool stats)
				if err := np.UpdateTemplates(needTemplates); err != nil {
					logger.Error("failed to store templates in Redis", "error", err)
					os.Exit(1)
				}
				logger.Info("templates generated and stored in Redis",
					"count", len(needTemplates))
			} else {
				logger.Info("all nonce UTXOs already have templates",
					"count", len(available))
			}
		}

		logger.Info("Redis-backed pools initialized", "mode", mode,
			"nonce_prefix", config.PoolPrefix(mode, "nonce"),
			"fee_prefix", config.PoolPrefix(mode, "fee"),
			"payment_prefix", config.PoolPrefix(mode, "payment"))
	} else {
		// In-memory fallback (no Redis)
		np, err := pool.NewMemoryPool(keys.NonceKey, mainnet, cfg.LeaseTTL, bcast)
		if err != nil {
			logger.Error("failed to create nonce pool", "error", err)
			os.Exit(1)
		}
		noncePool = np

		fp, err := pool.NewMemoryPool(keys.FeeKey, mainnet, cfg.LeaseTTL, bcast)
		if err != nil {
			logger.Error("failed to create fee pool", "error", err)
			os.Exit(1)
		}
		feePool = fp

		pp, err := pool.NewMemoryPool(keys.PaymentKey, mainnet, cfg.LeaseTTL, bcast)
		if err != nil {
			logger.Error("failed to create payment pool", "error", err)
			os.Exit(1)
		}
		paymentPool = pp

		if demoMode {
			logger.Info("demo mode: in-memory pools (empty — demo seeding not yet implemented)")
		} else {
			logger.Info("live mode: in-memory pools (empty — use Treasury fan-out to populate)")
		}
	}

	// Run local pool integrity check (enforces mode isolation in Redis)
	if cfg.RedisEnabled {
		for _, p := range []struct {
			name   string
			prefix string
		}{
			{"nonce", config.PoolPrefix(mode, "nonce")},
			{"fee", config.PoolPrefix(mode, "fee")},
			{"payment", config.PoolPrefix(mode, "payment")},
		} {
			result := pool.CheckIntegrity(rdb, p.prefix, mode, logger)
			if result.Checked > 0 {
				logger.Info("pool integrity check",
					"pool", p.name,
					"mode", mode,
					"checked", result.Checked,
					"valid", result.Valid,
					"quarantined", result.Quarantined,
				)
			}
		}
	}

	// On-chain zombie UTXO validation (live mode only)
	// Fetches unspent UTXOs from WoC and retires any pool entries that are
	// actually spent on-chain. This cleans up "zombie" nonces/fees that were
	// reclaimed by the lease loop before the MarkSpent fix was deployed.
	if !demoMode {
		for _, pv := range []struct {
			name    string
			pool    pool.Pool
			address string
		}{
			{"nonce", noncePool, keys.NonceAddress},
			{"fee", feePool, keys.FeeAddress},
		} {
			onChain, err := fetchWoCUnspentSet(pv.address, wocBaseURL)
			if err != nil {
				logger.Warn("on-chain validation: skipped (WoC unavailable)",
					"pool", pv.name, "error", err)
				continue
			}
			result := pool.ValidateOnChain(pv.pool, onChain, logger)
			if result.Zombies > 0 {
				logger.Warn("on-chain validation: retired zombie UTXOs",
					"pool", pv.name,
					"checked", result.Checked,
					"valid", result.Valid,
					"zombies", result.Zombies,
				)
			} else if result.Checked > 0 {
				logger.Info("on-chain validation: pool clean",
					"pool", pv.name,
					"checked", result.Checked,
					"valid", result.Valid,
				)
			}
		}
	}

	// Payment pool hydration: discover on-chain settlement UTXOs at the payment
	// address that were created before the settlement-time tracking was deployed.
	// Uses Lookup() to avoid re-adding UTXOs the pool already knows about (any
	// status: available, leased, or spent).
	if !demoMode {
		items, err := fetchWoCUnspentItems(keys.PaymentAddress, wocBaseURL)
		if err != nil {
			logger.Warn("payment pool hydration: skipped (WoC unavailable)", "error", err)
		} else if len(items) > 0 {
			paymentScriptHex, _ := paymentPool.LockingScriptHex()
			var newUTXOs []pool.UTXO
			for _, item := range items {
				if paymentPool.Lookup(item.TxHash, uint32(item.TxPos)) == nil {
					newUTXOs = append(newUTXOs, pool.UTXO{
						TxID:     item.TxHash,
						Vout:     uint32(item.TxPos),
						Script:   paymentScriptHex,
						Satoshis: uint64(item.Value),
					})
				}
			}
			if len(newUTXOs) > 0 {
				paymentPool.AddExisting(newUTXOs)
				logger.Info("payment pool hydrated with on-chain settlement UTXOs",
					"added", len(newUTXOs),
					"already_tracked", len(items)-len(newUTXOs),
					"address", keys.PaymentAddress,
				)
			} else {
				logger.Info("payment pool hydration: all on-chain UTXOs already tracked",
					"count", len(items), "address", keys.PaymentAddress)
			}
		}
	}

	// Create Treasury UTXO watcher (polls WoC for unspent UTXOs)
	var watcher *treasury.TreasuryWatcher
	if cfg.TreasuryPollInterval > 0 {
		var err error
		watcher, err = treasury.NewTreasuryWatcher(
			mainnet,
			keys.TreasuryAddress,
			keys.TreasuryKey,
			time.Duration(cfg.TreasuryPollInterval)*time.Second,
			rdb, // nil if Redis not enabled
			wocBaseURL,
		)
		if err != nil {
			logger.Error("failed to create treasury watcher", "error", err)
			os.Exit(1)
		}
		logger.Info("treasury watcher configured",
			"address", keys.TreasuryAddress,
			"interval_s", cfg.TreasuryPollInterval,
		)
	}

	// Determine payee address and locking script
	payeeAddr := cfg.PayeeAddress
	if payeeAddr == "" {
		payeeAddr = keys.FeeAddress
		logger.Info("no PAYEE_ADDRESS set, using fee key address", "address", payeeAddr)
	}

	// Convert payee address to locking script hex
	payeeLockingScriptHex, err := addressToLockingScriptHex(payeeAddr)
	if err != nil {
		logger.Error("failed to derive payee locking script", "error", err)
		os.Exit(1)
	}

	// Create gateway replay cache (for gatekeeper proof verification)
	replayCache := replay.New(10*time.Minute, 10000)

	// Create bounded challenge cache (5 min TTL, 10K max)
	challengeCache := gatekeeper.NewChallengeCache(5*time.Minute, 10000)

	// Embedded delegator (optional — for simple single-process deployments)
	// When DELEGATOR_EMBEDDED=true, the gateway hosts delegation routes in-process.
	// When false (default), delegation runs as a separate service (cmd/delegator).
	var deleg *delegator.Delegator
	var feeDelegatorHandler *feedelegator.Handler
	if cfg.DelegatorEmbedded {
		replayCache := replay.New(10*time.Minute, 10000)

		var delegErr error
		deleg, delegErr = delegator.New(key, mainnet, feePool, replayCache, cfg.FeeRate)
		if delegErr != nil {
			logger.Error("failed to create embedded delegator", "error", delegErr)
			os.Exit(1)
		}

		feeDelegatorHandler, delegErr = feedelegator.NewHandler(keys.FeeKey, mainnet, feePool, cfg.FeeRate)
		if delegErr != nil {
			logger.Error("failed to create fee delegator handler", "error", delegErr)
			os.Exit(1)
		}
		logger.Info("embedded delegator enabled")
	}

	// Record server start time for uptime tracking
	startTime := time.Now()

	// Create persistent revenue tracker (Redis-backed)
	revenueTracker := dashboard.NewRevenueTracker(rdb, logger)

	// Create dashboard API (React dashboard backend)
	dashAPI := dashboard.NewDashboardAPI(
		cfg, keys, noncePool, feePool, paymentPool,
		keys.TreasuryKey, mainnet, bcast,
		startTime, payeeAddr,
		watcher, healthTracker, revenueTracker,
		wocBaseURL,
	)

	// Start pool lease reclaim loops
	stop := make(chan struct{})
	noncePool.StartReclaimLoop(30*time.Second, stop)
	feePool.StartReclaimLoop(30*time.Second, stop)
	paymentPool.StartReclaimLoop(30*time.Second, stop)

	// Start treasury watcher polling loop
	if watcher != nil {
		watcher.Start(stop)
	}

	// Profile B: start background template repair loop for Redis-backed nonce pool.
	// Periodically scans for nonce UTXOs missing templates and regenerates them.
	// Only needed for live+Redis mode — demo mode seeds templates at startup.
	if cfg.TemplateMode && cfg.RedisEnabled {
		if redisNoncePool, ok := noncePool.(*pool.RedisPool); ok {
			treasury.StartTemplateRepairLoop(treasury.TemplateRepairConfig{
				NoncePool:             redisNoncePool,
				NonceKey:              keys.NonceKey,
				PayeeLockingScriptHex: payeeLockingScriptHex,
				PriceSats:             cfg.TemplatePriceSats,
				Interval:              5 * time.Minute,
			}, stop)
			logger.Info("template repair loop started", "interval", "5m")
		}
	}

	// Setup HTTP mux
	mux := http.NewServeMux()

	// --- Unprotected endpoints ---

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		profile := "A (Open Nonce)"
		if cfg.TemplateMode {
			profile = "B (Gateway Template)"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":       "ok",
			"version":      "1.0.0",
			"network":      cfg.BSVNetwork,
			"profile":      profile,
			"nonce_pool":   noncePool.Stats(),
			"fee_pool":     feePool.Stats(),
			"payment_pool": paymentPool.Stats(),
		})
	})

	// Embedded delegator routes (only when DELEGATOR_EMBEDDED=true)
	if cfg.DelegatorEmbedded && deleg != nil {
		mux.HandleFunc("POST /delegate/x402", func(w http.ResponseWriter, r *http.Request) {
			var req delegator.DelegationRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "invalid request body: " + err.Error(),
				})
				return
			}

			// Enrich request with server-side data
			if req.ExpectedPayeeLockingScriptHex == "" {
				req.ExpectedPayeeLockingScriptHex = payeeLockingScriptHex
			}

			result, err := deleg.Accept(req)
			if err != nil {
				if delegErr, ok := err.(*delegator.DelegationError); ok {
					writeJSON(w, delegErr.Status, delegErr)
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error": err.Error(),
				})
				return
			}

			writeJSON(w, http.StatusOK, result)
		})
	}

	// --- Protected endpoint (gated by x402 middleware) ---

	gatekeeperCfg := gatekeeper.Config{
		MempoolChecker:        bcast, // Swappable implements broadcast.MempoolChecker
		NoncePool:             noncePool,
		ReplayCache:           replayCache,
		ChallengeCache:        challengeCache,
		PayeeLockingScriptHex: payeeLockingScriptHex,
		Network:               cfg.BSVNetwork,
		PricingFunc:           pricing.Fixed(100),
		ChallengeTTL:          5 * time.Minute,
		BindHeaders:           gatekeeper.HeaderAllowlist,
		SettlementRecorder:    revenueTracker,
		PaymentPool:           paymentPool,
	}

	expensive := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data":      "This response cost 100 satoshis via x402",
			"timestamp": time.Now().Unix(),
			"path":      r.URL.Path,
		})
	})

	mux.Handle("GET /v1/expensive", gatekeeper.Middleware(gatekeeperCfg)(expensive))
	mux.Handle("POST /v1/expensive", gatekeeper.Middleware(gatekeeperCfg)(expensive))

	// --- Fee delegator API (only when embedded) ---
	if cfg.DelegatorEmbedded && feeDelegatorHandler != nil {
		mux.HandleFunc("POST /api/v1/tx", feeDelegatorHandler.HandleDelegateTx())
		mux.HandleFunc("GET /api/utxo/stats", feeDelegatorHandler.HandleUTXOStats(cfg.RedisEnabled))
		mux.HandleFunc("GET /api/utxo/health", feeDelegatorHandler.HandleUTXOHealth())
		mux.HandleFunc("GET /api/health", feeDelegatorHandler.HandleHealth(startTime))
	}

	// --- Dashboard API (React dashboard backend) ---
	dashAPI.RegisterRoutes(mux)

	// --- React Dashboard SPA ---
	// Serve dashboard static files from the dashboard/dist directory if present.
	mux.Handle("GET /", http.FileServer(http.Dir("dashboard/dist")))

	// Start server
	addr := fmt.Sprintf(":%d", cfg.Port)
	server := &http.Server{
		Addr:         addr,
		Handler:      loggingMiddleware(mux, dashAPI.Stats()),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logger.Info("shutting down", "signal", sig.String())
		close(stop)
		server.Close()
	}()

	logger.Info("x402 gateway starting", "addr", addr, "payee", payeeAddr, "network", cfg.BSVNetwork)
	fmt.Printf("\n  x402 BSV Gateway v1.0\n")
	fmt.Printf("  ─────────────────────\n")
	fmt.Printf("  Network:    %s\n", cfg.BSVNetwork)
	if cfg.XPRIV != "" {
		fmt.Printf("  Key mode:   HD wallet (xPriv)\n")
		fmt.Printf("  Nonce:      %s\n", keys.NonceAddress)
		fmt.Printf("  Fee addr:   %s\n", keys.FeeAddress)
		fmt.Printf("  Payment:    %s\n", keys.PaymentAddress)
		fmt.Printf("  Treasury:   %s\n", keys.TreasuryAddress)
	} else {
		fmt.Printf("  Key mode:   single key (WIF)\n")
		fmt.Printf("  Address:    %s\n", keys.FeeAddress)
	}
	fmt.Printf("  Payee:      %s\n", payeeAddr)
	fmt.Printf("  Port:       %d\n", cfg.Port)
	fmt.Printf("  Pool size:  %d\n", cfg.PoolSize)
	if cfg.RedisEnabled {
		fmt.Printf("  Storage:    Redis (%s)\n", cfg.RedisURL)
	} else {
		fmt.Printf("  Storage:    in-memory\n")
	}
	if demoMode {
		fmt.Printf("  Mode:       demo (MockBroadcaster)\n")
	} else if cfg.Broadcaster == "composite" {
		fmt.Printf("  Mode:       live (composite: GorillaPool → WoC fallback)\n")
	} else {
		fmt.Printf("  Mode:       live (%s)\n", cfg.Broadcaster)
	}
	if cfg.TemplateMode {
		fmt.Printf("  Profile:    B (Gateway Template, price=%d sats, sighash=0xC3)\n", cfg.TemplatePriceSats)
	} else {
		fmt.Printf("  Profile:    A (Open Nonce)\n")
	}
	if watcher != nil {
		fmt.Printf("  Watcher:    every %ds at %s\n", cfg.TreasuryPollInterval, keys.TreasuryAddress)
	} else {
		fmt.Printf("  Watcher:    disabled\n")
	}
	fmt.Printf("  Dashboard:  http://localhost:%d/\n", cfg.Port)
	if cfg.DelegatorEmbedded {
		fmt.Printf("  Delegator:  embedded (in-process)\n")
	} else {
		fmt.Printf("  Delegator:  external (separate service)\n")
	}
	fmt.Printf("\n  Endpoints:\n")
	fmt.Printf("    GET  /health          Health check\n")
	fmt.Printf("    GET  /v1/expensive    Protected (100 sats)\n")
	if cfg.DelegatorEmbedded {
		fmt.Printf("    POST /delegate/x402   Delegation (embedded)\n")
		fmt.Printf("    POST /api/v1/tx       Fee delegator API\n")
		fmt.Printf("    GET  /api/utxo/stats  UTXO pool stats\n")
		fmt.Printf("    GET  /api/utxo/health UTXO pool health\n")
		fmt.Printf("    GET  /api/health      API health\n")
	}
	fmt.Printf("    GET  /api/v1/config   Dashboard config\n")
	fmt.Printf("    GET  /api/v1/stats/*  Dashboard analytics\n")
	fmt.Printf("    GET  /api/v1/treasury/* Treasury mgmt\n")
	fmt.Printf("    GET  /                Dashboard (React SPA)\n")
	fmt.Printf("\n")

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

func addressToLockingScriptHex(addr string) (string, error) {
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

func loggingMiddleware(next http.Handler, stats *dashboard.StatsCollector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", duration.Milliseconds())

		// Record stats for dashboard analytics
		if stats != nil {
			var feeSats uint64
			if amountStr := rw.Header().Get("X402-Amount-Sats"); amountStr != "" {
				if v, err := fmt.Sscanf(amountStr, "%d", &feeSats); err != nil || v != 1 {
					feeSats = 0
				}
			}
			stats.Record(dashboard.RequestStat{
				Timestamp: time.Now(),
				Path:      r.URL.Path,
				Method:    r.Method,
				Status:    rw.status,
				Duration:  duration,
				FeeSats:   feeSats,
			})
		}
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher for SSE support.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
