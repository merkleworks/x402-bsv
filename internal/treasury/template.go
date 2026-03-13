// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package treasury

import (
	"encoding/hex"
	"fmt"
	"sync"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// TemplateSigHash is SIGHASH_SINGLE | ANYONECANPAY | FORKID = 0xC3.
//
// This flag combination is the ONLY choice that satisfies all Profile B requirements:
//   - SIGHASH_SINGLE (0x03): commits only to output[input_index].
//     Since the nonce is signed at input 0, only Output 0 (payment) is locked.
//     Sponsors can append change outputs at index >= 1.
//   - ANYONECANPAY (0x80): commits only to the current input.
//     Sponsors can append funding inputs at index >= 1.
//   - FORKID (0x40): required by BSV.
//
// The nonce input MUST remain at index 0. Moving it to any other index
// changes which output is committed (SIGHASH_SINGLE binds to output[N]
// for input N), causing signature verification to fail.
var TemplateSigHash = sighash.Flag(sighash.SingleForkID | sighash.AnyOneCanPay)

// TemplateSigHashByte is the raw byte value (0xC3) that appears as the last
// byte of DER signatures in template-mode nonce inputs.
const TemplateSigHashByte = byte(0xC3)

// GenerateTemplates creates a pre-signed transaction template for each UTXO
// in the slice. Each template contains:
//
//	Input 0:  nonce UTXO (signed by gateway with 0xC3)
//	Output 0: payment (priceSats → payeeLockingScript)
//
// The template is partially complete — sponsors extend it by appending
// funding inputs and optional change outputs, then sign their own inputs.
//
// Templates are NEVER broadcast by the gateway. They are stored as metadata
// on nonce pool entries and returned to clients in 402 challenge responses.
//
// This function modifies utxos in-place, populating RawTxTemplate,
// TemplatePriceSats, and EndpointClass on each entry.
func GenerateTemplates(
	nonceKey *ec.PrivateKey,
	utxos []pool.UTXO,
	payeeLockingScriptHex string,
	priceSats uint64,
) error {
	if len(utxos) == 0 {
		return nil
	}

	payeeScriptBytes, err := hex.DecodeString(payeeLockingScriptHex)
	if err != nil {
		return fmt.Errorf("invalid payee locking script hex: %w", err)
	}

	for i := range utxos {
		tmplHex, err := buildTemplate(nonceKey, &utxos[i], payeeScriptBytes, priceSats)
		if err != nil {
			return fmt.Errorf("template for %s:%d: %w", utxos[i].TxID, utxos[i].Vout, err)
		}
		utxos[i].RawTxTemplate = tmplHex
		utxos[i].TemplatePriceSats = priceSats
		utxos[i].EndpointClass = "basic"
		utxos[i].TemplateVersion = 1
	}

	return nil
}

// GenerateTemplatesParallel is like GenerateTemplates but distributes work
// across multiple goroutines. Safe because each goroutine writes to its own
// slice segment — no shared mutable state.
func GenerateTemplatesParallel(
	nonceKey *ec.PrivateKey,
	utxos []pool.UTXO,
	payeeLockingScriptHex string,
	priceSats uint64,
	workers int,
) error {
	if len(utxos) == 0 {
		return nil
	}
	if workers <= 1 {
		return GenerateTemplates(nonceKey, utxos, payeeLockingScriptHex, priceSats)
	}

	payeeScriptBytes, err := hex.DecodeString(payeeLockingScriptHex)
	if err != nil {
		return fmt.Errorf("invalid payee locking script hex: %w", err)
	}

	// Split into chunks
	chunkSize := (len(utxos) + workers - 1) / workers
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for start := 0; start < len(utxos); start += chunkSize {
		end := start + chunkSize
		if end > len(utxos) {
			end = len(utxos)
		}
		chunk := utxos[start:end]

		wg.Add(1)
		go func(slice []pool.UTXO) {
			defer wg.Done()
			for j := range slice {
				tmplHex, err := buildTemplate(nonceKey, &slice[j], payeeScriptBytes, priceSats)
				if err != nil {
					errCh <- fmt.Errorf("template for %s:%d: %w", slice[j].TxID, slice[j].Vout, err)
					return
				}
				slice[j].RawTxTemplate = tmplHex
				slice[j].TemplatePriceSats = priceSats
				slice[j].EndpointClass = "basic"
				slice[j].TemplateVersion = 1
			}
		}(chunk)
	}

	wg.Wait()
	close(errCh)

	// Return first error if any
	for err := range errCh {
		return err
	}
	return nil
}

// buildTemplate constructs and signs a single template transaction.
func buildTemplate(
	nonceKey *ec.PrivateKey,
	utxo *pool.UTXO,
	payeeScriptBytes []byte,
	priceSats uint64,
) (string, error) {
	tx := transaction.NewTransaction()

	// Input 0: nonce UTXO signed with SIGHASH_SINGLE|ANYONECANPAY|FORKID (0xC3)
	sigHash := TemplateSigHash
	unlocker, err := p2pkh.Unlock(nonceKey, &sigHash)
	if err != nil {
		return "", fmt.Errorf("create nonce unlocker: %w", err)
	}

	err = tx.AddInputFrom(utxo.TxID, utxo.Vout, utxo.Script, utxo.Satoshis, unlocker)
	if err != nil {
		return "", fmt.Errorf("add nonce input: %w", err)
	}

	// Output 0: payment (locked by SIGHASH_SINGLE — cannot be modified)
	payeeScript := script.Script(payeeScriptBytes)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      priceSats,
		LockingScript: &payeeScript,
	})

	// Sign the nonce input
	if err := tx.Sign(); err != nil {
		return "", fmt.Errorf("sign template: %w", err)
	}

	return tx.Hex(), nil
}
