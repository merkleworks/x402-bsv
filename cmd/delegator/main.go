// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

	"github.com/merkleworks/x402-bsv/internal/config"
	"github.com/merkleworks/x402-bsv/internal/delegator"
	"github.com/merkleworks/x402-bsv/internal/feedelegator"
	"github.com/merkleworks/x402-bsv/internal/hdwallet"
	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/replay"
	"github.com/merkleworks/x402-bsv/internal/treasury"
)

func main() {
	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	logger := slog.Default()

	// Load config (shared with gateway — reuses same env vars)
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	port := cfg.DelegatorPort
	mainnet := cfg.IsMainnet()

	// Derive keys — only the fee key is needed by the delegator
	var keys *hdwallet.DerivedKeys
	if cfg.XPRIV != "" {
		keys, err = hdwallet.DeriveFromXPriv(cfg.XPRIV, mainnet)
		if err != nil {
			logger.Error("invalid XPRIV", "error", err)
			os.Exit(1)
		}
		logger.Info("HD wallet mode (xPriv)", "fee_address", keys.FeeAddress)
	} else {
		keys, err = hdwallet.DeriveFromWIF(cfg.BSVPrivateKey, mainnet)
		if err != nil {
			logger.Error("invalid BSV_PRIVATE_KEY", "error", err)
			os.Exit(1)
		}
		logger.Info("single-key mode (WIF)", "address", keys.FeeAddress)
	}

	// Connect to Redis if enabled
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

	// Create fee pool — the delegator only needs fee UTXOs.
	// Mode-segregated namespaces ensure mock and live data never intersect in Redis.
	var feePool pool.Pool
	demoMode := cfg.Broadcaster == "mock"
	mode := cfg.RuntimeMode()

	if cfg.RedisEnabled {
		fp, err := pool.NewRedisPool(rdb, config.PoolPrefix(mode, "fee"), keys.FeeKey, mainnet, cfg.LeaseTTL)
		if err != nil {
			logger.Error("failed to create fee pool", "error", err)
			os.Exit(1)
		}
		feePool = fp
		if demoMode {
			seedFeePool(feePool, cfg.PoolSize, cfg.FeeRate, cfg.FeeUTXOSats, logger)
		}
		logger.Info("Redis-backed fee pool initialized", "mode", mode,
			"prefix", config.PoolPrefix(mode, "fee"))
	} else {
		fp, err := pool.NewMemoryPool(keys.FeeKey, mainnet, cfg.LeaseTTL, nil)
		if err != nil {
			logger.Error("failed to create fee pool", "error", err)
			os.Exit(1)
		}
		feePool = fp
		if demoMode {
			seedFeePool(feePool, cfg.PoolSize, cfg.FeeRate, cfg.FeeUTXOSats, logger)
			logger.Info("demo mode: in-memory fee pool with synthetic UTXOs")
		} else {
			logger.Info("live mode: in-memory fee pool (empty — use Treasury fan-out to populate)")
		}
	}

	// Run local pool integrity check (enforces mode isolation in Redis)
	if cfg.RedisEnabled {
		feePrefix := config.PoolPrefix(mode, "fee")
		result := pool.CheckIntegrity(rdb, feePrefix, mode, logger)
		if result.Checked > 0 {
			logger.Info("pool integrity check",
				"pool", "fee",
				"mode", mode,
				"checked", result.Checked,
				"valid", result.Valid,
				"quarantined", result.Quarantined,
			)
		}
	}

	// Create replay cache (10 minute TTL, 10K entries)
	replayCache := replay.New(10*time.Minute, 10000)

	// Create delegator — adds fee inputs and signs only those
	deleg, err := delegator.New(keys.FeeKey, mainnet, feePool, replayCache, cfg.FeeRate)
	if err != nil {
		logger.Error("failed to create delegator", "error", err)
		os.Exit(1)
	}

	// Determine payee locking script for validation
	payeeAddr := cfg.PayeeAddress
	if payeeAddr == "" {
		payeeAddr = feePool.Address()
	}
	payeeLockingScriptHex, err := addressToLockingScriptHex(payeeAddr)
	if err != nil {
		logger.Error("failed to derive payee locking script", "error", err)
		os.Exit(1)
	}

	// Create fee delegator handler (Node.js-compatible POST /api/v1/tx)
	feeDelegatorHandler, err := feedelegator.NewHandler(keys.FeeKey, mainnet, feePool, cfg.FeeRate)
	if err != nil {
		logger.Error("failed to create fee delegator handler", "error", err)
		os.Exit(1)
	}

	// Record start time for uptime tracking
	startTime := time.Now()

	// Start lease reclaim loop
	stop := make(chan struct{})
	feePool.StartReclaimLoop(30*time.Second, stop)

	// Setup HTTP mux
	mux := http.NewServeMux()

	// --- CORS middleware ---
	corsHandler := corsMiddleware(mux)

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"service":  "x402-delegator",
			"version":  "1.0.0",
			"network":  cfg.BSVNetwork,
			"fee_pool": feePool.Stats(),
		})
	})

	// --- Simplified delegation endpoint ---
	// POST /delegate/x402
	// Accepts: {"partial_tx":"<hex>"}
	// Returns: {"completed_tx":"<hex>", "txid":"<hex>"}
	// The delegator infers nonce outpoint, payee, amount, and template mode
	// from the transaction itself. NO challenge hash, NO HTTP context required.
	mux.HandleFunc("POST /delegate/x402", handleDelegateX402(deleg, payeeLockingScriptHex))

	// --- Fee delegator API (Node.js-compatible) ---
	mux.HandleFunc("POST /api/v1/tx", feeDelegatorHandler.HandleDelegateTx())
	mux.HandleFunc("GET /api/utxo/stats", feeDelegatorHandler.HandleUTXOStats(cfg.RedisEnabled))
	mux.HandleFunc("GET /api/utxo/health", feeDelegatorHandler.HandleUTXOHealth())
	mux.HandleFunc("GET /api/health", feeDelegatorHandler.HandleHealth(startTime))

	// Start server
	addr := fmt.Sprintf(":%d", port)
	server := &http.Server{
		Addr:         addr,
		Handler:      corsHandler,
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

	logger.Info("x402 delegator starting", "addr", addr, "payee", payeeAddr, "network", cfg.BSVNetwork)
	fmt.Printf("\n  x402 BSV Delegator v1.0\n")
	fmt.Printf("  ───────────────────────\n")
	fmt.Printf("  Network:    %s\n", cfg.BSVNetwork)
	fmt.Printf("  Fee addr:   %s\n", keys.FeeAddress)
	fmt.Printf("  Payee:      %s\n", payeeAddr)
	fmt.Printf("  Port:       %d\n", port)
	if cfg.RedisEnabled {
		fmt.Printf("  Storage:    Redis (%s)\n", cfg.RedisURL)
	} else {
		fmt.Printf("  Storage:    in-memory\n")
	}
	if demoMode {
		fmt.Printf("  Mode:       demo (mock)\n")
	} else {
		fmt.Printf("  Mode:       live\n")
	}
	fmt.Printf("\n  Endpoints:\n")
	fmt.Printf("    GET  /health          Health check\n")
	fmt.Printf("    POST /delegate/x402   Delegation (simplified)\n")
	fmt.Printf("    POST /api/v1/tx       Fee delegator API (Node.js-compat)\n")
	fmt.Printf("    GET  /api/utxo/stats  UTXO pool stats\n")
	fmt.Printf("    GET  /api/utxo/health UTXO pool health\n")
	fmt.Printf("    GET  /api/health      API health\n")
	fmt.Printf("\n")

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// POST /delegate/x402 — Simplified delegation handler
// ---------------------------------------------------------------------------

// delegateRequest is the simplified request body.
type delegateRequest struct {
	PartialTx string `json:"partial_tx"`
}

// delegateResponse is the simplified response body.
type delegateResponse struct {
	CompletedTx string `json:"completed_tx"`
	TxID        string `json:"txid"`
}

// handleDelegateX402 returns an HTTP handler for the simplified delegation endpoint.
// The handler parses the partial tx, infers all required parameters from the
// transaction itself (nonce outpoint, payee, amount, template mode), and calls
// the existing delegator.Accept() method.
//
// Per architecture invariants:
//   - The delegator MUST NOT require challenge hash, nonce txid/vout parameters, or HTTP context
//   - All service policy validation happens at the gateway
//   - The delegator validates transaction structure, adds fees, signs fee inputs only
func handleDelegateX402(deleg *delegator.Delegator, payeeLockingScriptHex string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req delegateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid request body: " + err.Error(),
			})
			return
		}

		if req.PartialTx == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "partial_tx is required",
			})
			return
		}

		// Decode the partial transaction
		txBytes, err := hex.DecodeString(req.PartialTx)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid partial_tx hex: " + err.Error(),
			})
			return
		}

		tx, err := transaction.NewTransactionFromBytes(txBytes)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "cannot parse partial transaction: " + err.Error(),
			})
			return
		}

		// Extract nonce outpoint from input 0
		if tx.InputCount() < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "partial transaction has no inputs",
			})
			return
		}

		input0 := tx.Inputs[0]
		if input0.SourceTXID == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "input 0 has no source txid",
			})
			return
		}
		nonceTxID := input0.SourceTXID.String()
		nonceVout := input0.SourceTxOutIndex

		// Validate input[0] has an unlocking script (must be a signed template,
		// not an unsigned/raw transaction)
		if input0.UnlockingScript == nil || len(*input0.UnlockingScript) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid template transaction: input[0] has no unlocking script (unsigned)",
			})
			return
		}

		// Enforce sighash 0xC3 on input[0] — the /delegate/x402 endpoint only
		// accepts x402 template transactions (Profile B). The gateway pre-signs
		// input[0] with SIGHASH_SINGLE|ANYONECANPAY|FORKID (0xC3), which locks
		// output[0] while allowing fee inputs to be appended.
		sighashByte, err := extractSighashByte(*input0.UnlockingScript)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid template transaction: cannot extract sighash from input[0]: " + err.Error(),
			})
			return
		}
		if sighashByte != treasury.TemplateSigHashByte {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("invalid template transaction: input[0] sighash 0x%02X, required 0xC3 (SIGHASH_SINGLE|ANYONECANPAY|FORKID)", sighashByte),
			})
			return
		}
		templateMode := true

		// Extract payee amount from output 0
		if len(tx.Outputs) < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "partial transaction has no outputs",
			})
			return
		}

		// Output[0] must have a positive value — a zero-value output is not a
		// valid payment and would drain the fee pool without revenue.
		if tx.Outputs[0].Satoshis == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "invalid template transaction: output[0] has zero value",
			})
			return
		}
		expectedAmount := int64(tx.Outputs[0].Satoshis)

		// Use a deterministic hash of the nonce outpoint as the challenge hash
		// for replay protection. The actual challenge hash is a gateway concern —
		// the delegator only needs a unique identifier per nonce.
		challengeHash := nonceOutpointHash(nonceTxID, nonceVout)

		// Build the DelegationRequest from inferred values.
		// Nonce UTXOs are always 1 sat (nonce pool design). Passing the value
		// allows the delegator to account for it when calculating the fee deficit —
		// the nonce's 1 sat covers the miner fee, so the fee pool only needs to
		// cover the payment output.
		delegReq := delegator.DelegationRequest{
			PartialTxHex:                  req.PartialTx,
			ChallengeHash:                 challengeHash,
			ExpectedPayeeLockingScriptHex: payeeLockingScriptHex,
			ExpectedAmount:                expectedAmount,
			NonceOutpoint: &delegator.NonceOutpointRef{
				TxID:     nonceTxID,
				Vout:     nonceVout,
				Satoshis: 1, // nonce pool UTXOs are always 1 sat
			},
			TemplateMode: templateMode,
		}

		result, err := deleg.Accept(delegReq)
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

		writeJSON(w, http.StatusOK, delegateResponse{
			CompletedTx: result.RawTxHex,
			TxID:        result.TxID,
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractSighashByte parses a P2PKH scriptSig to extract the sighash flag byte.
// The sighash byte is the last byte of the signature data chunk.
func extractSighashByte(scriptSig script.Script) (byte, error) {
	if len(scriptSig) < 2 {
		return 0, fmt.Errorf("scriptSig too short (%d bytes)", len(scriptSig))
	}
	sigPushLen := int(scriptSig[0])
	if sigPushLen < 1 || sigPushLen > 75 {
		return 0, fmt.Errorf("unexpected signature push opcode: 0x%02X", scriptSig[0])
	}
	if len(scriptSig) < 1+sigPushLen {
		return 0, fmt.Errorf("scriptSig truncated")
	}
	return scriptSig[sigPushLen], nil
}

// nonceOutpointHash returns a deterministic SHA-256 hash of a nonce outpoint
// for use as a replay protection key. The actual challenge hash is a gateway
// concern — this is simply a unique identifier per nonce for the delegator's
// internal replay cache.
func nonceOutpointHash(txid string, vout uint32) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", txid, vout)))
	return hex.EncodeToString(h[:])
}

// addressToLockingScriptHex converts a BSV address to its P2PKH locking script hex.
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

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// corsMiddleware adds CORS headers so the dashboard (served from :8402) can
// call the delegator on :8403.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X402-Challenge, X402-Proof")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// seedFeePool seeds the fee pool with synthetic fee UTXOs for demo mode.
// Each UTXO uses the configured FEE_UTXO_SATS denomination (1–1000 sats).
func seedFeePool(feePool pool.Pool, poolSize int, feeRate float64, feeUTXOSats uint64, logger *slog.Logger) {
	feeKey := "fee"
	utxos := make([]pool.UTXO, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		txid := syntheticTxID(feeKey, i)
		utxos = append(utxos, pool.UTXO{
			TxID:       txid,
			Vout:       0,
			Script:     "76a914000000000000000000000000000000000000000088ac", // dummy P2PKH (50 hex chars)
			Satoshis:   feeUTXOSats,
			Status:     pool.StatusAvailable,
			Synthetic:  true,
			OriginMode: "mock",
		})
	}
	feePool.AddExisting(utxos)
	logger.Info("seeded fee pool", "count", len(utxos), "utxo_sats", feeUTXOSats,
		"total_sats", uint64(len(utxos))*feeUTXOSats)
}

// syntheticTxID generates a deterministic fake txid for demo mode.
func syntheticTxID(prefix string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("synthetic:%s:%d", prefix, index)))
	return hex.EncodeToString(h[:])
}
