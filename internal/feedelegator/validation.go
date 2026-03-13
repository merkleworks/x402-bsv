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
	"fmt"
)

// validateRequest validates the fee delegation request body.
// Matches the Node.js validation middleware rules.
func validateRequest(req *DelegateRequest) error {
	if len(req.TxJSON.Inputs) == 0 {
		return fmt.Errorf("txJson.inputs is required and must not be empty")
	}
	if len(req.TxJSON.Outputs) == 0 {
		return fmt.Errorf("txJson.outputs is required and must not be empty")
	}

	// Validate each input
	for i, inp := range req.TxJSON.Inputs {
		if inp.TxID == "" {
			return fmt.Errorf("input[%d].txid is required", i)
		}
		if len(inp.TxID) != 64 {
			return fmt.Errorf("input[%d].txid must be 64 hex characters, got %d", i, len(inp.TxID))
		}
		if _, err := hex.DecodeString(inp.TxID); err != nil {
			return fmt.Errorf("input[%d].txid is not valid hex: %s", i, err)
		}
		// scriptSig is optional (nonce inputs may be unsigned)
		if inp.ScriptSig != "" {
			if _, err := hex.DecodeString(inp.ScriptSig); err != nil {
				return fmt.Errorf("input[%d].scriptSig is not valid hex: %s", i, err)
			}
		}
	}

	// Validate each output
	for i, out := range req.TxJSON.Outputs {
		if out.Script == "" {
			return fmt.Errorf("output[%d].script is required", i)
		}
		if _, err := hex.DecodeString(out.Script); err != nil {
			return fmt.Errorf("output[%d].script is not valid hex: %s", i, err)
		}
		// Satoshis can be 0 for OP_RETURN outputs
	}

	return nil
}
