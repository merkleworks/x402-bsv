// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package feedelegator

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// x402SigHash is SIGHASH_ALL | ANYONECANPAY | FORKID = 0x41 | 0x80 = 0xC1.
// Fee inputs are signed so that the client's existing signatures remain valid.
var x402SigHash = sighash.Flag(sighash.AllForkID | sighash.AnyOneCanPay)

// Handler handles fee delegation requests.
// API-compatible with the Node.js fee delegator POST /api/v1/tx.
type Handler struct {
	key     *ec.PrivateKey
	address *script.Address
	mainnet bool
	feePool pool.Pool
	feeRate float64
	logger  *slog.Logger
}

// NewHandler creates a new fee delegator handler.
func NewHandler(key *ec.PrivateKey, mainnet bool, feePool pool.Pool, feeRate float64) (*Handler, error) {
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}

	return &Handler{
		key:     key,
		address: addr,
		mainnet: mainnet,
		feePool: feePool,
		feeRate: feeRate,
		logger:  slog.Default().With("component", "fee-delegator"),
	}, nil
}

// ---------------------------------------------------------------------------
// Request / Response types (Node.js-compatible JSON shapes)
// ---------------------------------------------------------------------------

// TxInput matches the Node.js fee delegator input format.
type TxInput struct {
	TxID      string `json:"txid"`
	Vout      uint32 `json:"vout"`
	Satoshis  uint64 `json:"satoshis"`
	ScriptSig string `json:"scriptSig"`
}

// TxOutput matches the Node.js fee delegator output format.
type TxOutput struct {
	Satoshis uint64 `json:"satoshis"`
	Script   string `json:"script"`
}

// TxJSON is the partial transaction structure in the request body.
type TxJSON struct {
	Inputs  []TxInput  `json:"inputs"`
	Outputs []TxOutput `json:"outputs"`
}

// DelegateRequest is the POST /api/v1/tx request body.
type DelegateRequest struct {
	TxJSON TxJSON `json:"txJson"`
}

// DelegateResponse is the successful POST /api/v1/tx response.
type DelegateResponse struct {
	Success bool   `json:"success"`
	TxID    string `json:"txid"`
	RawTx   string `json:"rawtx"`
	Fee     uint64 `json:"fee"`
	Mode    string `json:"mode"`
}

// ErrorResponse is the error response format.
type ErrorResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

// ---------------------------------------------------------------------------
// POST /api/v1/tx — Fee Delegation
// ---------------------------------------------------------------------------

// HandleDelegateTx returns an http.HandlerFunc for POST /api/v1/tx.
// The handler:
//  1. Validates the request (inputs, outputs, scripts)
//  2. Builds a BSV transaction from the provided inputs/outputs
//  3. Calculates the miner fee: ceil(txSizeKB) sats (1 sat/KB, min 1 sat)
//  4. Leases N × 1-sat fee UTXOs via feePool.LeaseN(feeSats)
//  5. Adds fee UTXOs as new inputs and signs them with 0xC1
//  6. Returns the completed rawtx for the client to broadcast
func (h *Handler) HandleDelegateTx() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse request body
		var req DelegateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}

		// Validate request
		if err := validateRequest(&req); err != nil {
			h.writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// Build the transaction from client-provided inputs and outputs
		tx := transaction.NewTransaction()

		// Add client inputs — these come with existing scriptSig (already signed by client)
		for i, inp := range req.TxJSON.Inputs {
			// Parse txid to chainhash
			txidHash, err := chainhash.NewHashFromHex(inp.TxID)
			if err != nil {
				h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid txid in input %d: %s", i, err))
				return
			}

			if inp.ScriptSig != "" {
				// Client-signed input — add with existing scriptSig
				scriptBytes, err := hex.DecodeString(inp.ScriptSig)
				if err != nil {
					h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid scriptSig hex in input %d: %s", i, err))
					return
				}

				unlockScript := script.Script(scriptBytes)
				input := &transaction.TransactionInput{
					SourceTXID:       txidHash,
					SourceTxOutIndex: inp.Vout,
					UnlockingScript:  &unlockScript,
					SequenceNumber:   0xFFFFFFFF,
				}

				// Set source output for fee calculation (satoshis needed for signing context)
				input.SetSourceTxOutput(&transaction.TransactionOutput{
					Satoshis: inp.Satoshis,
				})

				tx.AddInput(input)
			} else {
				// Unsigned input
				input := &transaction.TransactionInput{
					SourceTXID:       txidHash,
					SourceTxOutIndex: inp.Vout,
					SequenceNumber:   0xFFFFFFFF,
				}
				input.SetSourceTxOutput(&transaction.TransactionOutput{
					Satoshis: inp.Satoshis,
				})
				tx.AddInput(input)
			}
		}

		// Add client outputs
		for i, out := range req.TxJSON.Outputs {
			scriptBytes, err := hex.DecodeString(out.Script)
			if err != nil {
				h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid script hex in output %d: %s", i, err))
				return
			}
			lockScript := script.Script(scriptBytes)
			tx.AddOutput(&transaction.TransactionOutput{
				Satoshis:      out.Satoshis,
				LockingScript: &lockScript,
			})
		}

		// Calculate miner fee: 1 sat/KB, rounded up, minimum 1 sat
		feeSats := h.calculateFee(tx)

		h.logger.Info("fee delegation request",
			"client_inputs", len(req.TxJSON.Inputs),
			"client_outputs", len(req.TxJSON.Outputs),
			"fee_sats", feeSats,
		)

		// Lease fee UTXOs — each is 1 sat, so we need feeSats of them
		feeUTXOs, err := h.feePool.LeaseN(int(feeSats))
		if err != nil {
			h.logger.Error("failed to lease fee UTXOs",
				"needed", feeSats,
				"available", h.feePool.Available(),
				"error", err,
			)
			h.writeError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("insufficient fee UTXOs: need %d, have %d available", feeSats, h.feePool.Available()))
			return
		}

		// Add each fee UTXO as an input with 0xC1 sighash unlocker
		for _, feeUTXO := range feeUTXOs {
			unlocker, err := p2pkh.Unlock(h.key, &x402SigHash)
			if err != nil {
				// Rollback: in production this should return UTXOs to pool
				h.writeError(w, http.StatusInternalServerError, "create fee unlocker: "+err.Error())
				return
			}

			if err := tx.AddInputFrom(feeUTXO.TxID, feeUTXO.Vout, feeUTXO.Script, feeUTXO.Satoshis, unlocker); err != nil {
				h.writeError(w, http.StatusInternalServerError, "add fee input: "+err.Error())
				return
			}
		}

		// Sign only the fee inputs (SignUnsigned skips inputs that already have UnlockingScript)
		if err := tx.SignUnsigned(); err != nil {
			h.writeError(w, http.StatusInternalServerError, "sign fee inputs: "+err.Error())
			return
		}

		txid := tx.TxID().String()
		rawtx := tx.Hex()

		// Mark all fee UTXOs as spent
		for _, feeUTXO := range feeUTXOs {
			h.feePool.MarkSpent(feeUTXO.TxID, feeUTXO.Vout)
		}

		h.logger.Info("fee delegation completed",
			"txid", txid,
			"fee", feeSats,
			"fee_utxos", len(feeUTXOs),
			"rawtx_size", len(rawtx)/2, // hex chars / 2 = bytes
		)

		// Return Node.js-compatible response
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(DelegateResponse{
			Success: true,
			TxID:    txid,
			RawTx:   rawtx,
			Fee:     feeSats,
			Mode:    "raw_transaction_returned",
		})
	}
}

// calculateFee estimates the miner fee for the transaction.
// Fee rate: 1 sat/KB. Always rounds up. Minimum 1 sat.
// This accounts for the additional fee inputs that will be added.
func (h *Handler) calculateFee(tx *transaction.Transaction) uint64 {
	// Base transaction overhead: version(4) + nInputVarInt(1) + nOutputVarInt(1) + locktime(4) = 10 bytes
	baseSize := 10

	// Existing inputs
	inputSize := 0
	for _, input := range tx.Inputs {
		if input.UnlockingScript != nil && len(*input.UnlockingScript) > 0 {
			inputSize += 32 + 4 + 1 + len(*input.UnlockingScript) + 4
		} else {
			// Unsigned input — estimate as P2PKH (~107 bytes after signing)
			inputSize += 32 + 4 + 1 + 107 + 4
		}
	}

	// Existing outputs
	outputSize := 0
	for _, output := range tx.Outputs {
		if output.LockingScript != nil {
			outputSize += 8 + 1 + len(*output.LockingScript)
		} else {
			outputSize += 8 + 1 + 25
		}
	}

	// Iterative fee calculation: fee inputs add size, which may require more fee
	// Each P2PKH input is ~148 bytes. Since each fee UTXO is 1 sat,
	// we need ceil(totalSizeKB) fee UTXOs, each adding ~148 bytes.
	estimatedFeeInputs := 1
	for iter := 0; iter < 5; iter++ {
		totalSize := baseSize + inputSize + (estimatedFeeInputs * 148) + outputSize
		sizeKB := float64(totalSize) / 1024.0
		neededFee := uint64(math.Ceil(sizeKB))
		if neededFee < 1 {
			neededFee = 1
		}
		if int(neededFee) == estimatedFeeInputs {
			return neededFee
		}
		estimatedFeeInputs = int(neededFee)
	}

	// Converge after max iterations
	if estimatedFeeInputs < 1 {
		return 1
	}
	return uint64(estimatedFeeInputs)
}

// ---------------------------------------------------------------------------
// Health & Stats Endpoints (Node.js-compatible)
// ---------------------------------------------------------------------------

// HandleHealth returns an http.HandlerFunc for GET /health.
func (h *Handler) HandleHealth(startTime time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success":   true,
			"status":    "ok",
			"uptime":    time.Since(startTime).Seconds(),
			"timestamp": time.Now().Unix(),
		})
	}
}

// HandleUTXOStats returns an http.HandlerFunc for GET /api/utxo/stats.
func (h *Handler) HandleUTXOStats(redisEnabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success":      true,
			"redisEnabled": redisEnabled,
			"stats": map[string]any{
				"fee": h.feePool.Stats(),
			},
		})
	}
}

// HandleUTXOHealth returns an http.HandlerFunc for GET /api/utxo/health.
func (h *Handler) HandleUTXOHealth() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		feeStats := h.feePool.Stats()
		healthy := feeStats.Available > 0

		status := "healthy"
		if !healthy {
			status = "degraded"
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"status":  status,
			"healthy": healthy,
			"poolHealth": map[string]any{
				"fee": map[string]any{
					"healthy":   healthy,
					"available": feeStats.Available,
					"total":     feeStats.Total,
				},
			},
			"availableUtxos": feeStats.Available,
		})
	}
}

// writeError writes a JSON error response.
func (h *Handler) writeError(w http.ResponseWriter, status int, msg string) {
	h.logger.Error("fee delegation error", "status", status, "error", msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{
		Success: false,
		Error:   msg,
	})
}
