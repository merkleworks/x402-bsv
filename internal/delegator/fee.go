// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package delegator

import "github.com/bsv-blockchain/go-sdk/transaction"

// CalculateFee estimates the mining fee needed for a transaction.
// It uses the current number of inputs/outputs to estimate size.
// feeRate is in satoshis per byte.
// BSV standard: 1 sat/KB = 0.001 sat/byte.
// A typical x402 tx (~400 bytes) costs ~1 sat at the standard rate.
func CalculateFee(tx *transaction.Transaction, additionalInputs int, feeRate float64) uint64 {
	// Base transaction overhead: version(4) + nInputVarInt(1) + nOutputVarInt(1) + locktime(4) = 10 bytes
	baseSize := 10

	// Each existing input: prevTxID(32) + vout(4) + scriptLen(1-3) + script(~107 for P2PKH signed, 0 for unsigned) + sequence(4)
	existingInputSize := 0
	for _, input := range tx.Inputs {
		if input.UnlockingScript != nil {
			existingInputSize += 32 + 4 + 1 + len(*input.UnlockingScript) + 4
		} else {
			// Unsigned input — estimate as P2PKH (will be ~107 bytes after signing)
			existingInputSize += 32 + 4 + 1 + 107 + 4
		}
	}

	// Additional fee inputs (P2PKH): ~148 bytes each
	additionalInputSize := additionalInputs * 148

	// Each output: value(8) + scriptLen(1-3) + script(~25 for P2PKH)
	outputSize := 0
	for _, output := range tx.Outputs {
		if output.LockingScript != nil {
			outputSize += 8 + 1 + len(*output.LockingScript)
		} else {
			outputSize += 8 + 1 + 25 // estimate P2PKH
		}
	}

	// Add space for potential change output
	changeOutputSize := 8 + 1 + 25 // P2PKH change output

	totalSize := baseSize + existingInputSize + additionalInputSize + outputSize + changeOutputSize

	fee := uint64(float64(totalSize) * feeRate)
	if fee < 1 {
		fee = 1
	}
	return fee
}
