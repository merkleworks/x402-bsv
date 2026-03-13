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
	"fmt"

	bip32 "github.com/bsv-blockchain/go-sdk/compat/bip32"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
)

// Derivation path: m/402'/0'/index'
// Index assignments:
//   0 = nonce pool key
//   1 = fee pool key
//   2 = treasury / funding key
//   3 = payment pool key (for service payments)
const (
	purposeIndex  = 402
	accountIndex  = 0
	nonceIndex    = 0
	feeIndex      = 1
	treasuryIndex = 2
	paymentIndex  = 3
)

// DerivedKeys holds purpose-specific keys derived from an xPriv or WIF.
type DerivedKeys struct {
	// MasterXPriv is the serialized xpriv string (empty if created from WIF).
	MasterXPriv string

	// NonceKey is derived at m/402'/0'/0' — used for nonce pool UTXOs.
	NonceKey *ec.PrivateKey

	// FeeKey is derived at m/402'/0'/1' — used for fee pool UTXOs.
	FeeKey *ec.PrivateKey

	// TreasuryKey is derived at m/402'/0'/2' — used for treasury funding.
	TreasuryKey *ec.PrivateKey

	// PaymentKey is derived at m/402'/0'/3' — used for service payment UTXOs.
	PaymentKey *ec.PrivateKey

	// Addresses (derived from each key + network)
	NonceAddress    string
	FeeAddress      string
	TreasuryAddress string
	PaymentAddress  string

	// Network
	Mainnet bool
}

// DeriveFromXPriv derives purpose-specific child keys from a BIP32 extended
// private key string. Uses hardened derivation path m/402'/0'/index'.
func DeriveFromXPriv(xpriv string, mainnet bool) (*DerivedKeys, error) {
	master, err := bip32.GenerateHDKeyFromString(xpriv)
	if err != nil {
		return nil, fmt.Errorf("parse xpriv: %w", err)
	}

	// Derive m/402'
	purpose, err := master.Child(bip32.HardenedKeyStart + purposeIndex)
	if err != nil {
		return nil, fmt.Errorf("derive m/402': %w", err)
	}

	// Derive m/402'/0'
	account, err := purpose.Child(bip32.HardenedKeyStart + accountIndex)
	if err != nil {
		return nil, fmt.Errorf("derive m/402'/0': %w", err)
	}

	// Derive child keys
	nonceKey, err := deriveChildKey(account, nonceIndex)
	if err != nil {
		return nil, fmt.Errorf("derive nonce key m/402'/0'/0': %w", err)
	}

	feeKey, err := deriveChildKey(account, feeIndex)
	if err != nil {
		return nil, fmt.Errorf("derive fee key m/402'/0'/1': %w", err)
	}

	treasuryKey, err := deriveChildKey(account, treasuryIndex)
	if err != nil {
		return nil, fmt.Errorf("derive treasury key m/402'/0'/2': %w", err)
	}

	paymentKey, err := deriveChildKey(account, paymentIndex)
	if err != nil {
		return nil, fmt.Errorf("derive payment key m/402'/0'/3': %w", err)
	}

	keys := &DerivedKeys{
		MasterXPriv: xpriv,
		NonceKey:    nonceKey,
		FeeKey:      feeKey,
		TreasuryKey: treasuryKey,
		PaymentKey:  paymentKey,
		Mainnet:     mainnet,
	}

	// Derive addresses
	if err := keys.deriveAddresses(); err != nil {
		return nil, err
	}

	return keys, nil
}

// DeriveFromWIF creates a DerivedKeys using the same WIF key for all purposes.
// This provides backward compatibility with single-key configurations.
func DeriveFromWIF(wif string, mainnet bool) (*DerivedKeys, error) {
	key, err := ec.PrivateKeyFromWif(wif)
	if err != nil {
		return nil, fmt.Errorf("parse WIF: %w", err)
	}

	keys := &DerivedKeys{
		NonceKey:    key,
		FeeKey:      key,
		TreasuryKey: key,
		PaymentKey:  key,
		Mainnet:     mainnet,
	}

	if err := keys.deriveAddresses(); err != nil {
		return nil, err
	}

	return keys, nil
}

// GenerateXPriv creates a new random BIP32 extended private key and derives
// all purpose-specific child keys from it.
func GenerateXPriv(mainnet bool) (xprivStr string, keys *DerivedKeys, err error) {
	xpriv, _, err := bip32.GenerateHDKeyPair(bip32.RecommendedSeedLength)
	if err != nil {
		return "", nil, fmt.Errorf("generate HD key pair: %w", err)
	}

	keys, err = DeriveFromXPriv(xpriv, mainnet)
	if err != nil {
		return "", nil, fmt.Errorf("derive from generated xpriv: %w", err)
	}

	return xpriv, keys, nil
}

// deriveChildKey derives a hardened child key at the given index.
func deriveChildKey(parent *bip32.ExtendedKey, index uint32) (*ec.PrivateKey, error) {
	child, err := parent.Child(bip32.HardenedKeyStart + index)
	if err != nil {
		return nil, err
	}

	privKey, err := child.ECPrivKey()
	if err != nil {
		return nil, err
	}

	return privKey, nil
}

// deriveAddresses derives BSV addresses from each key for the configured network.
func (dk *DerivedKeys) deriveAddresses() error {
	var err error

	dk.NonceAddress, err = addressFromKey(dk.NonceKey, dk.Mainnet)
	if err != nil {
		return fmt.Errorf("derive nonce address: %w", err)
	}

	dk.FeeAddress, err = addressFromKey(dk.FeeKey, dk.Mainnet)
	if err != nil {
		return fmt.Errorf("derive fee address: %w", err)
	}

	dk.TreasuryAddress, err = addressFromKey(dk.TreasuryKey, dk.Mainnet)
	if err != nil {
		return fmt.Errorf("derive treasury address: %w", err)
	}

	dk.PaymentAddress, err = addressFromKey(dk.PaymentKey, dk.Mainnet)
	if err != nil {
		return fmt.Errorf("derive payment address: %w", err)
	}

	return nil
}

// addressFromKey creates a BSV address from a private key.
func addressFromKey(key *ec.PrivateKey, mainnet bool) (string, error) {
	addr, err := script.NewAddressFromPublicKey(key.PubKey(), mainnet)
	if err != nil {
		return "", err
	}
	return addr.AddressString, nil
}
