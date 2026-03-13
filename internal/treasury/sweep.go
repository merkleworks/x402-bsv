// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package treasury

import (
	"fmt"
	"log/slog"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
)

// SweepInput describes one UTXO to sweep.
type SweepInput struct {
	TxID     string // txid of the UTXO
	Vout     uint32 // output index
	Script   string // hex locking script
	Satoshis uint64 // value in satoshis
}

// SweepRequest describes the inputs and destination for a sweep transaction.
type SweepRequest struct {
	Inputs      []SweepInput // UTXOs to sweep (all must be owned by the signing key)
	Destination string       // address to send swept funds to
	FeeRate     float64      // fee rate in sat/byte
}

// SweepResult contains the broadcast txid and value swept.
type SweepResult struct {
	TxID       string       // txid of the sweep transaction
	InputSats  uint64       // total input satoshis
	OutputSats uint64       // satoshis sent to destination (after fee)
	Fee        uint64       // miner fee paid
	OutputUTXO *FundingUTXO // consolidated output at destination (for mempool tracking)
}

// BuildSweep constructs and broadcasts a sweep transaction that consolidates
// multiple UTXOs into a single output at the destination address.
func BuildSweep(
	key *ec.PrivateKey,
	mainnet bool,
	req SweepRequest,
	bcast transaction.Broadcaster,
) (*SweepResult, error) {
	if len(req.Inputs) == 0 {
		return nil, fmt.Errorf("no inputs provided")
	}
	if req.Destination == "" {
		return nil, fmt.Errorf("destination address required")
	}

	logger := slog.Default().With("component", "treasury-sweep")

	tx := transaction.NewTransaction()

	// Standard signing (SIGHASH_ALL | FORKID)
	allForkID := sighash.AllForkID
	unlocker, err := p2pkh.Unlock(key, &allForkID)
	if err != nil {
		return nil, fmt.Errorf("create unlocker: %w", err)
	}

	// Add all inputs
	var totalInput uint64
	for i, inp := range req.Inputs {
		err = tx.AddInputFrom(inp.TxID, inp.Vout, inp.Script, inp.Satoshis, unlocker)
		if err != nil {
			return nil, fmt.Errorf("add input %d (%s:%d): %w", i, inp.TxID, inp.Vout, err)
		}
		totalInput += inp.Satoshis
	}

	// Calculate fee: each P2PKH input ~148 bytes, 1 output ~34 bytes, overhead ~10 bytes
	estimatedSize := 10 + (len(req.Inputs) * 148) + 34
	fee := uint64(ceilSats(float64(estimatedSize) * req.FeeRate))
	if fee < 1 {
		fee = 1
	}

	if totalInput <= fee {
		return nil, fmt.Errorf("insufficient funds: total input %d sats <= fee %d sats", totalInput, fee)
	}

	outputSats := totalInput - fee

	// Single output to destination
	if err := tx.PayToAddress(req.Destination, outputSats); err != nil {
		return nil, fmt.Errorf("add destination output: %w", err)
	}

	// Sign all inputs
	if err := tx.Sign(); err != nil {
		return nil, fmt.Errorf("sign transaction: %w", err)
	}

	// Broadcast
	success, failure := bcast.Broadcast(tx)
	if failure != nil {
		return nil, fmt.Errorf("broadcast failed: %s - %s", failure.Code, failure.Description)
	}

	txid := success.Txid

	// Build output UTXO for mempool tracking
	var outputUTXO *FundingUTXO
	destAddr, err := script.NewAddressFromString(req.Destination)
	if err == nil {
		destLockScript, err := p2pkh.Lock(destAddr)
		if err == nil {
			outputUTXO = &FundingUTXO{
				TxID:     txid,
				Vout:     0, // sweep has a single output
				Script:   fmt.Sprintf("%x", *destLockScript),
				Satoshis: outputSats,
			}
		}
	}

	logger.Info("sweep complete",
		"txid", txid,
		"inputs", len(req.Inputs),
		"input_sats", totalInput,
		"output_sats", outputSats,
		"fee", fee,
		"destination", req.Destination,
	)

	return &SweepResult{
		TxID:       txid,
		InputSats:  totalInput,
		OutputSats: outputSats,
		Fee:        fee,
		OutputUTXO: outputUTXO,
	}, nil
}
