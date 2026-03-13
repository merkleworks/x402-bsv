// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package hdwallet

import (
	"testing"
)

func TestGenerateXPriv(t *testing.T) {
	xpriv, keys, err := GenerateXPriv(false) // testnet
	if err != nil {
		t.Fatalf("GenerateXPriv: %v", err)
	}

	if xpriv == "" {
		t.Fatal("expected non-empty xpriv string")
	}
	if keys.NonceKey == nil || keys.FeeKey == nil || keys.TreasuryKey == nil {
		t.Fatal("expected all derived keys to be non-nil")
	}
	if keys.NonceAddress == "" || keys.FeeAddress == "" || keys.TreasuryAddress == "" {
		t.Fatal("expected all derived addresses to be non-empty")
	}
	if keys.MasterXPriv != xpriv {
		t.Fatalf("MasterXPriv mismatch: got %q, want %q", keys.MasterXPriv, xpriv)
	}
	if keys.Mainnet {
		t.Fatal("expected testnet")
	}

	// All three addresses should be different (different derivation paths)
	if keys.NonceAddress == keys.FeeAddress {
		t.Fatal("nonce and fee addresses should differ")
	}
	if keys.NonceAddress == keys.TreasuryAddress {
		t.Fatal("nonce and treasury addresses should differ")
	}
	if keys.FeeAddress == keys.TreasuryAddress {
		t.Fatal("fee and treasury addresses should differ")
	}

	// Testnet addresses should start with 'm' or 'n'
	for name, addr := range map[string]string{
		"nonce":    keys.NonceAddress,
		"fee":      keys.FeeAddress,
		"treasury": keys.TreasuryAddress,
	} {
		if addr[0] != 'm' && addr[0] != 'n' {
			t.Errorf("%s address %q should start with 'm' or 'n' for testnet", name, addr)
		}
	}
}

func TestDeriveFromXPriv_Deterministic(t *testing.T) {
	// Generate an xpriv, then re-derive — should produce identical keys
	xpriv, keys1, err := GenerateXPriv(false)
	if err != nil {
		t.Fatalf("GenerateXPriv: %v", err)
	}

	keys2, err := DeriveFromXPriv(xpriv, false)
	if err != nil {
		t.Fatalf("DeriveFromXPriv: %v", err)
	}

	// Same xpriv should produce identical addresses
	if keys1.NonceAddress != keys2.NonceAddress {
		t.Errorf("nonce address mismatch: %s vs %s", keys1.NonceAddress, keys2.NonceAddress)
	}
	if keys1.FeeAddress != keys2.FeeAddress {
		t.Errorf("fee address mismatch: %s vs %s", keys1.FeeAddress, keys2.FeeAddress)
	}
	if keys1.TreasuryAddress != keys2.TreasuryAddress {
		t.Errorf("treasury address mismatch: %s vs %s", keys1.TreasuryAddress, keys2.TreasuryAddress)
	}
}

func TestDeriveFromXPriv_MainnetVsTestnet(t *testing.T) {
	xpriv, _, err := GenerateXPriv(false)
	if err != nil {
		t.Fatalf("GenerateXPriv: %v", err)
	}

	testnetKeys, err := DeriveFromXPriv(xpriv, false)
	if err != nil {
		t.Fatalf("DeriveFromXPriv testnet: %v", err)
	}

	mainnetKeys, err := DeriveFromXPriv(xpriv, true)
	if err != nil {
		t.Fatalf("DeriveFromXPriv mainnet: %v", err)
	}

	// Same xpriv with different networks should produce different addresses
	// (due to address version byte differences)
	if testnetKeys.NonceAddress == mainnetKeys.NonceAddress {
		t.Error("testnet and mainnet nonce addresses should differ")
	}

	// Mainnet addresses start with '1'
	if mainnetKeys.NonceAddress[0] != '1' {
		t.Errorf("mainnet nonce address %q should start with '1'", mainnetKeys.NonceAddress)
	}
}

func TestDeriveFromWIF(t *testing.T) {
	// Generate a key, get WIF, then derive from WIF
	xpriv, xprivKeys, err := GenerateXPriv(false)
	if err != nil {
		t.Fatalf("GenerateXPriv: %v", err)
	}
	_ = xpriv

	// Get WIF from one of the derived keys (use nonce key as example)
	wif := xprivKeys.NonceKey.Wif()

	wifKeys, err := DeriveFromWIF(wif, false)
	if err != nil {
		t.Fatalf("DeriveFromWIF: %v", err)
	}

	// All three keys should be the same (single-key mode)
	if wifKeys.NonceAddress != wifKeys.FeeAddress {
		t.Error("WIF mode: all addresses should be identical")
	}
	if wifKeys.NonceAddress != wifKeys.TreasuryAddress {
		t.Error("WIF mode: all addresses should be identical")
	}

	// MasterXPriv should be empty in WIF mode
	if wifKeys.MasterXPriv != "" {
		t.Error("WIF mode: MasterXPriv should be empty")
	}
}

func TestDeriveFromXPriv_InvalidInput(t *testing.T) {
	_, err := DeriveFromXPriv("not-a-valid-xpriv", false)
	if err == nil {
		t.Fatal("expected error for invalid xpriv")
	}
}

func TestDeriveFromWIF_InvalidInput(t *testing.T) {
	_, err := DeriveFromWIF("not-a-valid-wif", false)
	if err == nil {
		t.Fatal("expected error for invalid WIF")
	}
}

func TestGenerateXPriv_TwoGenerationsAreDifferent(t *testing.T) {
	xpriv1, _, err := GenerateXPriv(false)
	if err != nil {
		t.Fatalf("GenerateXPriv 1: %v", err)
	}

	xpriv2, _, err := GenerateXPriv(false)
	if err != nil {
		t.Fatalf("GenerateXPriv 2: %v", err)
	}

	if xpriv1 == xpriv2 {
		t.Error("two generated xprivs should be different")
	}
}
