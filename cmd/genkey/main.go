// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package main

import (
	"fmt"
	"log"

	"github.com/merkleworks/x402-bsv/internal/hdwallet"
)

func main() {
	xpriv, keys, err := hdwallet.GenerateXPriv(true) // true = mainnet
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Println("         NEW MAINNET HD WALLET - BACKUP THIS!")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Println("XPRIV (add to .env):")
	fmt.Println(xpriv)
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Println("                   MAINNET ADDRESSES")
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Println()
	fmt.Printf("Fee Pool:     %s\n", keys.FeeAddress)
	fmt.Printf("Payment Pool: %s\n", keys.PaymentAddress)
	fmt.Printf("Treasury:     %s\n", keys.TreasuryAddress)
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════")
	fmt.Println("  FUND THE TREASURY ADDRESS TO SEED THE UTXO POOLS")
	fmt.Println("════════════════════════════════════════════════════════════")
}
