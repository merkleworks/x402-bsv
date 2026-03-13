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
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/pool"
)

// testKey generates a deterministic private key for testing.
func testKey(t *testing.T) *ec.PrivateKey {
	t.Helper()
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

// testPayeeLockingScript generates a P2PKH locking script hex for a given key.
func testPayeeLockingScript(t *testing.T, key *ec.PrivateKey, mainnet bool) string {
	t.Helper()
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	lockScript, err := p2pkh.Lock(addr)
	if err != nil {
		t.Fatalf("lock script: %v", err)
	}
	return hex.EncodeToString(*lockScript)
}

// testNonceUTXOs creates n synthetic nonce UTXOs with valid scripts.
func testNonceUTXOs(t *testing.T, n int, nonceKey *ec.PrivateKey, mainnet bool) []pool.UTXO {
	t.Helper()
	addr, err := script.NewAddressFromPublicKey(nonceKey.PubKey(), mainnet)
	if err != nil {
		t.Fatalf("derive address: %v", err)
	}
	lockScript, err := p2pkh.Lock(addr)
	if err != nil {
		t.Fatalf("lock script: %v", err)
	}
	scriptHex := hex.EncodeToString(*lockScript)

	utxos := make([]pool.UTXO, n)
	for i := range utxos {
		utxos[i] = pool.UTXO{
			TxID:     "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			Vout:     uint32(i),
			Script:   scriptHex,
			Satoshis: 1,
		}
	}
	return utxos
}

func TestGenerateTemplates_Basic(t *testing.T) {
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 5, nonceKey, false)

	err := GenerateTemplates(nonceKey, utxos, payeeScript, 100)
	if err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	for i, u := range utxos {
		if u.RawTxTemplate == "" {
			t.Errorf("utxo[%d]: RawTxTemplate is empty", i)
		}
		if u.TemplatePriceSats != 100 {
			t.Errorf("utxo[%d]: TemplatePriceSats = %d, want 100", i, u.TemplatePriceSats)
		}
		if u.EndpointClass != "basic" {
			t.Errorf("utxo[%d]: EndpointClass = %q, want %q", i, u.EndpointClass, "basic")
		}
	}
}

func TestGenerateTemplates_StructureVerification(t *testing.T) {
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	err := GenerateTemplates(nonceKey, utxos, payeeScript, 100)
	if err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	// Deserialize the template
	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template hex: %v", err)
	}

	// Must have exactly 1 input
	if tx.InputCount() != 1 {
		t.Fatalf("template has %d inputs, want 1", tx.InputCount())
	}

	// Must have exactly 1 output
	if len(tx.Outputs) != 1 {
		t.Fatalf("template has %d outputs, want 1", len(tx.Outputs))
	}

	// Input 0 must reference the nonce outpoint
	if tx.Inputs[0].SourceTXID.String() != utxos[0].TxID {
		t.Errorf("input 0 txid = %s, want %s", tx.Inputs[0].SourceTXID.String(), utxos[0].TxID)
	}
	if tx.Inputs[0].SourceTxOutIndex != utxos[0].Vout {
		t.Errorf("input 0 vout = %d, want %d", tx.Inputs[0].SourceTxOutIndex, utxos[0].Vout)
	}

	// Input 0 must be signed (non-empty unlocking script)
	if tx.Inputs[0].UnlockingScript == nil || len(*tx.Inputs[0].UnlockingScript) == 0 {
		t.Fatal("input 0 has no unlocking script (unsigned)")
	}

	// Output 0 must pay the correct amount
	if tx.Outputs[0].Satoshis != 100 {
		t.Errorf("output 0 satoshis = %d, want 100", tx.Outputs[0].Satoshis)
	}

	// Output 0 must have the correct locking script
	outScript := hex.EncodeToString(*tx.Outputs[0].LockingScript)
	if outScript != payeeScript {
		t.Errorf("output 0 script = %s, want %s", outScript, payeeScript)
	}
}

func TestGenerateTemplates_SighashIs0xC3(t *testing.T) {
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	err := GenerateTemplates(nonceKey, utxos, payeeScript, 100)
	if err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	// Extract the sighash byte from the scriptSig
	// P2PKH scriptSig: <push_len> <DER_sig || sighash_byte> <push_len> <pubkey>
	scriptSig := *tx.Inputs[0].UnlockingScript
	if len(scriptSig) < 2 {
		t.Fatal("scriptSig too short")
	}

	sigPushLen := int(scriptSig[0])
	if sigPushLen < 1 || sigPushLen > 75 {
		t.Fatalf("unexpected sig push opcode: 0x%02X", scriptSig[0])
	}
	if len(scriptSig) < 1+sigPushLen {
		t.Fatalf("scriptSig truncated: need %d bytes for sig, have %d", sigPushLen, len(scriptSig)-1)
	}

	// Last byte of the signature data is the sighash flag
	sighashByte := scriptSig[sigPushLen]
	if sighashByte != TemplateSigHashByte {
		t.Errorf("sighash byte = 0x%02X, want 0x%02X (SIGHASH_SINGLE|ANYONECANPAY|FORKID)", sighashByte, TemplateSigHashByte)
	}
}

func TestGenerateTemplates_Extensible(t *testing.T) {
	// Verify that a template can be extended with additional inputs and outputs
	// without invalidating the gateway's nonce signature.
	nonceKey := testKey(t)
	sponsorKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	err := GenerateTemplates(nonceKey, utxos, payeeScript, 100)
	if err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	// Deserialize the template
	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	// Sponsor adds a funding input at index 1
	sponsorSigHash := sighash.Flag(sighash.AllForkID | sighash.AnyOneCanPay)
	sponsorUnlocker, err := p2pkh.Unlock(sponsorKey, &sponsorSigHash)
	if err != nil {
		t.Fatalf("sponsor unlocker: %v", err)
	}

	sponsorAddr, err := script.NewAddressFromPublicKey(sponsorKey.PubKey(), false)
	if err != nil {
		t.Fatalf("sponsor address: %v", err)
	}
	sponsorLock, err := p2pkh.Lock(sponsorAddr)
	if err != nil {
		t.Fatalf("sponsor lock: %v", err)
	}
	sponsorScriptHex := hex.EncodeToString(*sponsorLock)

	// Add sponsor funding input
	err = tx.AddInputFrom(
		"b1b2b3b4b5b6b7b8b9b0b1b2b3b4b5b6b7b8b9b0b1b2b3b4b5b6b7b8b9b0b1b2",
		0,
		sponsorScriptHex,
		200, // enough to cover 100-sat payment + fees
		sponsorUnlocker,
	)
	if err != nil {
		t.Fatalf("add sponsor input: %v", err)
	}

	// Sponsor adds a change output at index 1
	if err := tx.PayToAddress(sponsorAddr.AddressString, 99); err != nil {
		t.Fatalf("add change output: %v", err)
	}

	// Verify the template still has the correct structure
	if tx.InputCount() != 2 {
		t.Fatalf("extended tx has %d inputs, want 2", tx.InputCount())
	}
	if len(tx.Outputs) != 2 {
		t.Fatalf("extended tx has %d outputs, want 2", len(tx.Outputs))
	}

	// Nonce input at index 0 is unchanged
	if tx.Inputs[0].SourceTXID.String() != utxos[0].TxID {
		t.Error("nonce input was modified")
	}

	// Payment output at index 0 is unchanged
	if tx.Outputs[0].Satoshis != 100 {
		t.Errorf("payment output modified: got %d, want 100", tx.Outputs[0].Satoshis)
	}

	// Sign the sponsor input (index 1)
	sponsorScript, err := tx.Inputs[1].UnlockingScriptTemplate.Sign(tx, 1)
	if err != nil {
		t.Fatalf("sign sponsor input: %v", err)
	}
	tx.Inputs[1].UnlockingScript = sponsorScript

	// The transaction should now have valid signatures on both inputs.
	// We can't fully verify without the actual UTXO set, but we can verify
	// the structure is correct and both inputs have non-empty scriptSigs.
	if tx.Inputs[0].UnlockingScript == nil || len(*tx.Inputs[0].UnlockingScript) == 0 {
		t.Error("nonce input lost its signature after extension")
	}
	if tx.Inputs[1].UnlockingScript == nil || len(*tx.Inputs[1].UnlockingScript) == 0 {
		t.Error("sponsor input has no signature")
	}

	// Verify the final tx serializes cleanly
	if tx.Hex() == "" {
		t.Error("extended tx serialization failed")
	}
}

func TestGenerateTemplates_Empty(t *testing.T) {
	nonceKey := testKey(t)
	err := GenerateTemplates(nonceKey, nil, "76a914aabbccdd00112233aabbccdd0011223344556677", 100)
	if err != nil {
		t.Errorf("GenerateTemplates with empty slice should not error, got: %v", err)
	}
}

func TestGenerateTemplates_InvalidPayeeScript(t *testing.T) {
	nonceKey := testKey(t)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)
	err := GenerateTemplates(nonceKey, utxos, "not-valid-hex", 100)
	if err == nil {
		t.Error("expected error for invalid payee script hex")
	}
}

func TestGenerateTemplatesParallel(t *testing.T) {
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 20, nonceKey, false)

	err := GenerateTemplatesParallel(nonceKey, utxos, payeeScript, 100, 4)
	if err != nil {
		t.Fatalf("GenerateTemplatesParallel: %v", err)
	}

	for i, u := range utxos {
		if u.RawTxTemplate == "" {
			t.Errorf("utxo[%d]: RawTxTemplate is empty", i)
		}
		if u.TemplatePriceSats != 100 {
			t.Errorf("utxo[%d]: TemplatePriceSats = %d, want 100", i, u.TemplatePriceSats)
		}
	}
}

// ─── Cryptographic Signature Verification Tests ───────────────────────────────
//
// These tests prove that SIGHASH_SINGLE|ANYONECANPAY|FORKID (0xC3) actually
// protects the payment output and allows sponsor extension, using real ECDSA
// signature verification — not just checking that bytes are present.

// reattachSourceOutput sets the source output on a deserialized template input
// so that CalcInputSignatureHash can compute the BIP143 sighash preimage.
func reattachSourceOutput(t *testing.T, tx *transaction.Transaction, utxo pool.UTXO) {
	t.Helper()
	lockBytes, err := hex.DecodeString(utxo.Script)
	if err != nil {
		t.Fatalf("decode nonce script: %v", err)
	}
	lockScript := script.Script(lockBytes)
	tx.Inputs[0].SetSourceTxOutput(&transaction.TransactionOutput{
		Satoshis:      utxo.Satoshis,
		LockingScript: &lockScript,
	})
}

// extractDERSigAndPubkey parses a P2PKH scriptSig and returns the DER
// signature (without the trailing sighash byte) and the compressed pubkey.
func extractDERSigAndPubkey(t *testing.T, scriptSig script.Script) ([]byte, []byte) {
	t.Helper()
	sigPushLen := int(scriptSig[0])
	sigData := scriptSig[1 : 1+sigPushLen] // DER + sighash byte
	derSig := sigData[:len(sigData)-1]      // strip sighash byte

	pubStart := 1 + sigPushLen
	pubPushLen := int(scriptSig[pubStart])
	pubKey := scriptSig[pubStart+1 : pubStart+1+pubPushLen]
	return derSig, pubKey
}

func TestTemplateOutputAmountTamper_BreaksSignature(t *testing.T) {
	// Proves: reducing output[0] amount invalidates the nonce ECDSA signature.
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	if err := GenerateTemplates(nonceKey, utxos, payeeScript, 100); err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	// Re-attach source output for sighash computation
	reattachSourceOutput(t, tx, utxos[0])

	// Extract DER signature and pubkey from input[0]
	derBytes, pubBytes := extractDERSigAndPubkey(t, *tx.Inputs[0].UnlockingScript)
	sig, err := ec.ParseDERSignature(derBytes)
	if err != nil {
		t.Fatalf("parse DER sig: %v", err)
	}
	pubKey, err := ec.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}

	// Verify with original output[0] — must pass
	origHash, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc original sighash: %v", err)
	}
	if !sig.Verify(origHash, pubKey) {
		t.Fatal("signature must verify with original output[0]")
	}

	// Tamper: reduce output[0] amount
	tx.Outputs[0].Satoshis = 50

	tamperedHash, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc tampered sighash: %v", err)
	}
	if sig.Verify(tamperedHash, pubKey) {
		t.Fatal("signature must NOT verify after reducing output[0] amount")
	}
}

func TestTemplateOutputScriptTamper_BreaksSignature(t *testing.T) {
	// Proves: changing output[0] locking script invalidates the nonce ECDSA signature.
	nonceKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	if err := GenerateTemplates(nonceKey, utxos, payeeScript, 100); err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	reattachSourceOutput(t, tx, utxos[0])

	derBytes, pubBytes := extractDERSigAndPubkey(t, *tx.Inputs[0].UnlockingScript)
	sig, err := ec.ParseDERSignature(derBytes)
	if err != nil {
		t.Fatalf("parse DER sig: %v", err)
	}
	pubKey, err := ec.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}

	// Verify with original output[0] — must pass
	origHash, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc original sighash: %v", err)
	}
	if !sig.Verify(origHash, pubKey) {
		t.Fatal("signature must verify with original output[0]")
	}

	// Tamper: change output[0] locking script to attacker's address
	attackerKey := testKey(t)
	attackerScript := testPayeeLockingScript(t, attackerKey, false)
	attackerBytes, _ := hex.DecodeString(attackerScript)
	attackerLock := script.Script(attackerBytes)
	tx.Outputs[0].LockingScript = &attackerLock

	tamperedHash, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc tampered sighash: %v", err)
	}
	if sig.Verify(tamperedHash, pubKey) {
		t.Fatal("signature must NOT verify after changing output[0] locking script")
	}
}

func TestTemplateSponsorExtension_PreservesSignature(t *testing.T) {
	// Proves cryptographically: appending sponsor inputs and change outputs
	// does NOT invalidate the nonce signature at input[0].
	nonceKey := testKey(t)
	sponsorKey := testKey(t)
	payeeKey := testKey(t)
	payeeScript := testPayeeLockingScript(t, payeeKey, false)
	utxos := testNonceUTXOs(t, 1, nonceKey, false)

	if err := GenerateTemplates(nonceKey, utxos, payeeScript, 100); err != nil {
		t.Fatalf("GenerateTemplates: %v", err)
	}

	tx, err := transaction.NewTransactionFromHex(utxos[0].RawTxTemplate)
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}

	reattachSourceOutput(t, tx, utxos[0])

	// Extract and parse nonce signature BEFORE extension
	derBytes, pubBytes := extractDERSigAndPubkey(t, *tx.Inputs[0].UnlockingScript)
	sig, err := ec.ParseDERSignature(derBytes)
	if err != nil {
		t.Fatalf("parse DER sig: %v", err)
	}
	pubKey, err := ec.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pubkey: %v", err)
	}

	// Verify BEFORE extension
	hashBefore, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc sighash before extension: %v", err)
	}
	if !sig.Verify(hashBefore, pubKey) {
		t.Fatal("signature must verify before extension")
	}

	// --- Sponsor extends the template ---

	// Add funding input at index 1
	sponsorSigHash := sighash.Flag(sighash.AllForkID | sighash.AnyOneCanPay)
	sponsorUnlocker, err := p2pkh.Unlock(sponsorKey, &sponsorSigHash)
	if err != nil {
		t.Fatalf("sponsor unlocker: %v", err)
	}
	sponsorAddr, _ := script.NewAddressFromPublicKey(sponsorKey.PubKey(), false)
	sponsorLock, _ := p2pkh.Lock(sponsorAddr)
	sponsorScriptHex := hex.EncodeToString(*sponsorLock)

	err = tx.AddInputFrom(
		"b1b2b3b4b5b6b7b8b9b0b1b2b3b4b5b6b7b8b9b0b1b2b3b4b5b6b7b8b9b0b1b2",
		0, sponsorScriptHex, 200, sponsorUnlocker,
	)
	if err != nil {
		t.Fatalf("add sponsor input: %v", err)
	}

	// Add change output at index 1
	if err := tx.PayToAddress(sponsorAddr.AddressString, 99); err != nil {
		t.Fatalf("add change output: %v", err)
	}

	// Verify nonce signature AFTER extension — must still pass
	hashAfter, err := tx.CalcInputSignatureHash(0, TemplateSigHash)
	if err != nil {
		t.Fatalf("calc sighash after extension: %v", err)
	}
	if !sig.Verify(hashAfter, pubKey) {
		t.Fatal("nonce signature must still verify after sponsor extension")
	}
}

func BenchmarkGenerateTemplates(b *testing.B) {
	nonceKey, _ := ec.NewPrivateKey()
	payeeKey, _ := ec.NewPrivateKey()
	addr, _ := script.NewAddressFromPublicKey(payeeKey.PubKey(), false)
	lockScript, _ := p2pkh.Lock(addr)
	payeeScript := hex.EncodeToString(*lockScript)

	nonceAddr, _ := script.NewAddressFromPublicKey(nonceKey.PubKey(), false)
	nonceLock, _ := p2pkh.Lock(nonceAddr)
	nonceScriptHex := hex.EncodeToString(*nonceLock)

	utxos := make([]pool.UTXO, b.N)
	for i := range utxos {
		utxos[i] = pool.UTXO{
			TxID:     "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
			Vout:     uint32(i % 10000),
			Script:   nonceScriptHex,
			Satoshis: 1,
		}
	}

	b.ResetTimer()
	err := GenerateTemplates(nonceKey, utxos, payeeScript, 100)
	if err != nil {
		b.Fatalf("GenerateTemplates: %v", err)
	}
}
