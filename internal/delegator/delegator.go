// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package delegator

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
	"github.com/merkleworks/x402-bsv/internal/replay"
)

// x402SigHash is SIGHASH_ALL | ANYONECANPAY | FORKID = 0xC1.
// Both client inputs and delegator fee inputs use this flag.
// ANYONECANPAY allows additional inputs to be appended after signing.
var x402SigHash = sighash.Flag(sighash.AllForkID | sighash.AnyOneCanPay)

// requiredSighashByte is the raw byte value (0xC1) that must appear as the
// last byte of every DER signature in the partial tx's client inputs.
const requiredSighashByte = byte(0xC1)

// allForkIDSighashByte is SIGHASH_ALL|FORKID (0x41) without ANYONECANPAY.
// Accepted on client/sponsor inputs alongside 0xC1.
const allForkIDSighashByte = byte(0x41)

// templateSighashByte is the raw byte value (0xC3) used by gateway-signed
// nonce inputs in Profile B (Gateway Template mode). This is
// SIGHASH_SINGLE|ANYONECANPAY|FORKID — allows appending inputs AND outputs.
const templateSighashByte = byte(0xC3)

// Delegator validates client-constructed partial transactions and appends
// fee inputs. Per canonical spec, the delegator:
//   - Does NOT construct the transaction (client does)
//   - Does NOT sign nonce or payment inputs (client does)
//   - Does NOT broadcast (client does)
//   - ONLY adds fee inputs, signs those, and returns the completed tx
type Delegator struct {
	key         *ec.PrivateKey
	address     *script.Address
	mainnet     bool
	feePool     pool.Pool    // pool for fee UTXOs (1-sat each)
	replayCache *replay.Cache
	feeRate     float64
	logger      *slog.Logger
}

// New creates a new Delegator.
// The key is used exclusively for signing fee inputs.
func New(
	key *ec.PrivateKey,
	mainnet bool,
	feePool pool.Pool,
	replayCache *replay.Cache,
	feeRate float64,
) (*Delegator, error) {
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		return nil, fmt.Errorf("derive address: %w", err)
	}

	return &Delegator{
		key:         key,
		address:     addr,
		mainnet:     mainnet,
		feePool:     feePool,
		replayCache: replayCache,
		feeRate:     feeRate,
		logger:      slog.Default().With("component", "delegator"),
	}, nil
}

// Accept validates a client-constructed partial transaction, appends fee
// inputs, signs only the fee inputs, and returns the completed transaction.
// It does NOT broadcast — that is the client's responsibility.
func (d *Delegator) Accept(req DelegationRequest) (*DelegationResult, error) {
	d.logger.Info("accepting delegation",
		"challenge_hash", req.ChallengeHash,
		"amount", req.ExpectedAmount,
	)

	// ── Step 1: Validate nonce outpoint is provided ──
	if req.NonceOutpoint == nil {
		return nil, &DelegationError{
			Code:    ErrInvalidProof.Code,
			Message: "nonce_outpoint is required for replay protection",
			Status:  ErrInvalidProof.Status,
		}
	}

	// ── Step 2: Decode partial transaction ──
	if req.PartialTxHex == "" {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: "partial_tx_hex is required",
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	partialTxBytes, err := hex.DecodeString(req.PartialTxHex)
	if err != nil {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: fmt.Sprintf("invalid partial_tx_hex: %s", err),
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	tx, err := transaction.NewTransactionFromBytes(partialTxBytes)
	if err != nil {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: fmt.Sprintf("cannot parse partial transaction: %s", err),
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	// ── Step 3: Structural validation (BEFORE replay cache) ──

	// 3a. Must have at least 1 input (nonce)
	if tx.InputCount() < 1 {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: "partial transaction has no inputs",
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	// 3b. Must have at least 1 output (payee)
	if len(tx.Outputs) < 1 {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: "partial transaction has no outputs",
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	// 3c. Verify nonce input references the expected outpoint
	if err := verifyNonceInput(tx, req.NonceOutpoint); err != nil {
		return nil, &DelegationError{
			Code:    ErrInvalidPartialTx.Code,
			Message: err.Error(),
			Status:  ErrInvalidPartialTx.Status,
		}
	}

	// 3d. Verify payee output pays >= expected amount to expected script
	if err := verifyPayeeOutput(tx, req.ExpectedPayeeLockingScriptHex, req.ExpectedAmount); err != nil {
		return nil, &DelegationError{
			Code:    ErrInvalidPayee.Code,
			Message: err.Error(),
			Status:  ErrInvalidPayee.Status,
		}
	}

	// 3e. Enforce sighash on client inputs
	//     Profile A: all inputs must be 0xC1
	//     Profile B: input 0 (gateway template nonce) may be 0xC3, rest must be 0xC1
	if err := enforceSighash(tx, req.TemplateMode); err != nil {
		return nil, &DelegationError{
			Code:    ErrInvalidSighash.Code,
			Message: err.Error(),
			Status:  ErrInvalidSighash.Status,
		}
	}

	// 3f. In template mode, nonce input MUST be at index 0 (SIGHASH_SINGLE
	//     binds the signature to output[input_index] — moving it breaks the sig)
	if req.TemplateMode {
		if err := verifyNonceAtIndex0(tx, req.NonceOutpoint); err != nil {
			return nil, &DelegationError{
				Code:    ErrInvalidPartialTx.Code,
				Message: err.Error(),
				Status:  ErrInvalidPartialTx.Status,
			}
		}
	}

	// ── Step 4: Atomic nonce reservation (AFTER structural validation) ──
	// TryReserve holds an exclusive lock for the entire check+insert,
	// eliminating the TOCTOU window that allowed RACE-01.
	nonceTxID := req.NonceOutpoint.TxID
	nonceVout := req.NonceOutpoint.Vout
	reserved, existingTxID, pending := d.replayCache.TryReserve(nonceTxID, nonceVout, req.ChallengeHash)
	if !reserved {
		if pending {
			d.logger.Info("nonce reservation pending (concurrent request)",
				"nonce", fmt.Sprintf("%s:%d", nonceTxID, nonceVout),
			)
			return nil, &DelegationError{
				Code:    ErrNoncePending.Code,
				Message: "nonce is being processed by another request; retry shortly",
				Status:  ErrNoncePending.Status,
			}
		}
		d.logger.Warn("replay detected",
			"nonce", fmt.Sprintf("%s:%d", nonceTxID, nonceVout),
			"existing_txid", existingTxID,
		)
		return nil, &DelegationError{
			Code:    ErrDoubleSpend.Code,
			Message: fmt.Sprintf("nonce already spent in tx %s", existingTxID),
			Status:  ErrDoubleSpend.Status,
		}
	}
	// Reservation acquired. If we return before Commit, release it so the
	// nonce is not stranded in pending state and the client can retry.
	nonceCommitted := false
	defer func() {
		if !nonceCommitted {
			d.replayCache.Release(nonceTxID, nonceVout, req.ChallengeHash)
		}
	}()

	// ── Step 5: Calculate funding deficit and iteratively lease fee UTXOs ──
	clientInputCount := tx.InputCount()

	// Sum all existing output values (payment output + any client outputs).
	var totalOutputSats uint64
	for _, out := range tx.Outputs {
		totalOutputSats += out.Satoshis
	}

	// Account for existing input values (nonce + any client inputs).
	// Bitcoin raw tx bytes don't carry input amounts, but the nonce value
	// is known from the pool (passed via NonceOutpointRef.Satoshis).
	// The nonce's 1 sat covers the miner fee; fee inputs cover the payment.
	var existingInputSats uint64
	if req.NonceOutpoint != nil && req.NonceOutpoint.Satoshis > 0 {
		existingInputSats = req.NonceOutpoint.Satoshis
	}

	var feeInputSats uint64
	var feeUTXOs []*pool.UTXO

	for {
		// Estimate miner fee with current number of planned fee inputs.
		minerFee := CalculateFee(tx, len(feeUTXOs), d.feeRate)

		// Deficit = outputs + miner fee - existing inputs (nonce contributes its sats)
		var needed uint64
		total := totalOutputSats + minerFee
		if total > existingInputSats {
			needed = total - existingInputSats
		}

		if feeInputSats >= needed {
			break // sufficient funding accumulated
		}

		utxo, err := d.feePool.Lease()
		if err != nil {
			// Fee pool exhausted before covering the deficit.
			// Previously leased UTXOs will be reclaimed by the pool's reclaim loop.
			return nil, &DelegationError{
				Code: ErrNoUTXOAvailable.Code,
				Message: fmt.Sprintf(
					"insufficient fee pool balance: accumulated %d sats from %d UTXOs, need %d sats (payment=%d, miner_fee_est=%d, existing_inputs=%d)",
					feeInputSats, len(feeUTXOs), needed, totalOutputSats, minerFee, existingInputSats,
				),
				Status: ErrNoUTXOAvailable.Status,
			}
		}
		feeUTXOs = append(feeUTXOs, utxo)
		feeInputSats += utxo.Satoshis
	}

	d.logger.Info("fee UTXOs leased",
		"count", len(feeUTXOs),
		"total_fee_sats", feeInputSats,
		"payment_output_sats", totalOutputSats,
	)

	// ── Step 6: Add all fee inputs with 0xC1 sighash ──
	for _, utxo := range feeUTXOs {
		feeUnlocker, err := p2pkh.Unlock(d.key, &x402SigHash)
		if err != nil {
			return nil, fmt.Errorf("create fee unlocker: %w", err)
		}
		if err := tx.AddInputFrom(utxo.TxID, utxo.Vout, utxo.Script, utxo.Satoshis, feeUnlocker); err != nil {
			return nil, fmt.Errorf("add fee input %s:%d: %w", utxo.TxID, utxo.Vout, err)
		}
	}

	// ── Step 7: Add change output if fee inputs exceed needed amount ──
	finalMinerFee := CalculateFee(tx, 0, d.feeRate) // all inputs already added
	var change uint64
	if feeInputSats > totalOutputSats+finalMinerFee {
		change = feeInputSats - totalOutputSats - finalMinerFee
		if change > 546 { // above dust threshold
			if err := tx.PayToAddress(d.address.AddressString, change); err != nil {
				return nil, fmt.Errorf("add change output: %w", err)
			}
		} else {
			change = 0 // dust goes to miner as extra fee
		}
	}

	// ── Step 8: Sign ALL fee inputs ──
	for i := 0; i < len(feeUTXOs); i++ {
		feeInputIdx := uint32(clientInputCount + i)
		if tx.Inputs[feeInputIdx].UnlockingScriptTemplate == nil {
			return nil, fmt.Errorf("fee input at index %d has no unlocking script template", feeInputIdx)
		}
		unlockScript, err := tx.Inputs[feeInputIdx].UnlockingScriptTemplate.Sign(tx, feeInputIdx)
		if err != nil {
			return nil, fmt.Errorf("sign fee input at index %d: %w", feeInputIdx, err)
		}
		tx.Inputs[feeInputIdx].UnlockingScript = unlockScript
	}

	// ── Pre-return validation: consensus rule check ──
	// Verify total inputs (fee + nonce) cover all outputs.
	var finalOutputSats uint64
	for _, out := range tx.Outputs {
		finalOutputSats += out.Satoshis
	}
	totalInputSats := feeInputSats + existingInputSats
	if totalInputSats < finalOutputSats {
		return nil, fmt.Errorf(
			"consensus violation: total inputs (%d sats = %d fee + %d nonce) < outputs (%d sats) — transaction would be rejected",
			totalInputSats, feeInputSats, existingInputSats, finalOutputSats,
		)
	}

	txid := tx.TxID().String()

	// ── Step 9: Commit nonce reservation with final txid ──
	if err := d.replayCache.Commit(nonceTxID, nonceVout, req.ChallengeHash, txid); err != nil {
		return nil, fmt.Errorf("replay cache commit: %w", err)
	}
	nonceCommitted = true

	// ── Step 10: Mark ALL fee UTXOs spent ──
	for _, utxo := range feeUTXOs {
		d.feePool.MarkSpent(utxo.TxID, utxo.Vout)
	}

	d.logger.Info("delegation accepted",
		"txid", txid,
		"challenge_hash", req.ChallengeHash,
		"nonce", fmt.Sprintf("%s:%d", req.NonceOutpoint.TxID, req.NonceOutpoint.Vout),
		"fee_inputs", len(feeUTXOs),
		"fee_input_sats", feeInputSats,
		"output_sats", finalOutputSats,
		"miner_fee_est", finalMinerFee,
		"change_sats", change,
		"client_inputs", clientInputCount,
	)

	// ── Step 11: Return completed tx — NO broadcast ──
	return &DelegationResult{
		TxID:     txid,
		RawTxHex: tx.Hex(),
		Accepted: true,
	}, nil
}

// verifyNonceInput checks that the partial transaction contains an input
// spending the expected nonce outpoint. Uses constant-time comparison.
func verifyNonceInput(tx *transaction.Transaction, nonce *NonceOutpointRef) error {
	if nonce == nil {
		return fmt.Errorf("nonce outpoint is nil")
	}
	for _, input := range tx.Inputs {
		if input.SourceTXID != nil &&
			constantTimeEqual(input.SourceTXID.String(), nonce.TxID) &&
			input.SourceTxOutIndex == nonce.Vout {
			return nil
		}
	}
	return fmt.Errorf("partial tx does not contain nonce input %s:%d", nonce.TxID, nonce.Vout)
}

// verifyPayeeOutput checks that the transaction has at least one output
// paying >= minAmount to the expected payee locking script.
// Uses constant-time comparison for the script hex.
func verifyPayeeOutput(tx *transaction.Transaction, expectedScriptHex string, minAmount int64) error {
	for _, out := range tx.Outputs {
		scriptHex := hex.EncodeToString(*out.LockingScript)
		if constantTimeEqual(scriptHex, expectedScriptHex) && int64(out.Satoshis) >= minAmount {
			return nil
		}
	}
	return fmt.Errorf("no output paying >= %d sats to expected payee", minAmount)
}

// enforceSighash validates sighash flags on all client inputs.
//
// Profile A (templateMode=false):
//
//	All inputs must use 0xC1 (SIGHASH_ALL|ANYONECANPAY|FORKID) or 0x41 (SIGHASH_ALL|FORKID).
//
// Profile B (templateMode=true):
//
//	Input 0 (gateway template nonce) must use 0xC3 (SIGHASH_SINGLE|ANYONECANPAY|FORKID).
//	All other inputs must use 0xC1 or 0x41.
//
// Template mode requires 0xC3 exclusively on input 0 because SIGHASH_SINGLE|ANYONECANPAY
// locks output[0] while allowing sponsors to append inputs and change outputs.
// Accepting 0xC1 (ALL|ANYONECANPAY) on the nonce input would commit to all outputs,
// breaking the intended template extensibility model.
//
// Parses the P2PKH scriptSig structure:
//
//	<sig_push_opcode> <DER_signature || sighash_byte> <pubkey_push_opcode> <pubkey>
//
// The sighash byte is the last byte of the signature data chunk.
func enforceSighash(tx *transaction.Transaction, templateMode bool) error {
	for i, input := range tx.Inputs {
		if input.UnlockingScript == nil || len(*input.UnlockingScript) == 0 {
			return fmt.Errorf("input %d has no unlocking script", i)
		}

		sighashByte, err := extractSighashByte(*input.UnlockingScript)
		if err != nil {
			return fmt.Errorf("input %d: %w", i, err)
		}

		if templateMode && i == 0 {
			// Profile B: input 0 must be gateway-signed with 0xC3
			if sighashByte != templateSighashByte {
				return fmt.Errorf("input 0 uses sighash 0x%02X, required 0xC3 (SIGHASH_SINGLE|ANYONECANPAY|FORKID)", sighashByte)
			}
			continue
		}

		if sighashByte != requiredSighashByte && sighashByte != allForkIDSighashByte {
			return fmt.Errorf("input %d uses sighash 0x%02X, required 0xC1 or 0x41 (SIGHASH_ALL with FORKID)", i, sighashByte)
		}
	}
	return nil
}

// verifyNonceAtIndex0 checks that the nonce outpoint is at input index 0.
// Required for Profile B because SIGHASH_SINGLE binds the gateway signature
// to output[input_index]. If the nonce input is not at index 0, the signature
// commits to the wrong output.
func verifyNonceAtIndex0(tx *transaction.Transaction, nonce *NonceOutpointRef) error {
	if nonce == nil {
		return fmt.Errorf("nonce outpoint is nil")
	}
	if tx.InputCount() < 1 {
		return fmt.Errorf("transaction has no inputs")
	}
	input0 := tx.Inputs[0]
	if input0.SourceTXID == nil || !constantTimeEqual(input0.SourceTXID.String(), nonce.TxID) || input0.SourceTxOutIndex != nonce.Vout {
		return fmt.Errorf("template mode requires nonce input at index 0, but input 0 is %s:%d (expected %s:%d)",
			input0.SourceTXID, input0.SourceTxOutIndex, nonce.TxID, nonce.Vout)
	}
	return nil
}

// extractSighashByte parses a P2PKH scriptSig to extract the sighash flag byte.
// P2PKH scriptSig format: <push_opcode> <signature_data> <push_opcode> <pubkey_data>
// The sighash byte is the last byte of the signature_data portion.
func extractSighashByte(scriptSig script.Script) (byte, error) {
	if len(scriptSig) < 2 {
		return 0, fmt.Errorf("scriptSig too short (%d bytes)", len(scriptSig))
	}

	// First byte is the push opcode indicating signature data length
	sigPushLen := int(scriptSig[0])

	// Validate: push opcode should be a direct data push (1-75 bytes)
	if sigPushLen < 1 || sigPushLen > 75 {
		return 0, fmt.Errorf("unexpected signature push opcode: 0x%02X", scriptSig[0])
	}

	// Ensure we have enough bytes
	if len(scriptSig) < 1+sigPushLen {
		return 0, fmt.Errorf("scriptSig truncated: need %d bytes for signature, have %d", sigPushLen, len(scriptSig)-1)
	}

	// The sighash byte is the last byte of the pushed signature data
	return scriptSig[sigPushLen], nil
}

// constantTimeEqual compares two strings in constant time to prevent timing attacks.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
