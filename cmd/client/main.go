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
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sighash "github.com/bsv-blockchain/go-sdk/transaction/sighash"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"

	"github.com/merkleworks/x402-bsv/internal/challenge"
	"github.com/merkleworks/x402-bsv/internal/gatekeeper"
)

// x402ClientSigHash is SIGHASH_ALL | ANYONECANPAY | FORKID = 0xC1.
// Client inputs must use this flag so the delegator can append fee inputs.
var x402ClientSigHash = sighash.Flag(sighash.AllForkID | sighash.AnyOneCanPay)

func main() {
	delegatorURL := flag.String("delegator", "", "Delegator URL (default: derives from target host)")
	method := flag.String("method", "GET", "HTTP method (GET or POST)")
	data := flag.String("data", "", "Request body (for POST)")
	nonceKeyWIF := flag.String("nonce-key", "", "WIF private key for signing nonce input (required)")
	paymentKeyWIF := flag.String("payment-key", "", "WIF private key for signing payment input (required)")
	paymentTxID := flag.String("payment-txid", "", "Payment UTXO txid (required)")
	paymentVout := flag.Uint("payment-vout", 0, "Payment UTXO vout")
	paymentSats := flag.Uint64("payment-sats", 0, "Payment UTXO satoshis (required)")
	paymentScript := flag.String("payment-script", "", "Payment UTXO locking script hex (required)")
	broadcastURL := flag.String("broadcast-url", "", "Broadcast endpoint URL (default: derives from target host)")
	flag.Parse()

	if flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: x402-client [flags] <url>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fmt.Fprintf(os.Stderr, "  --delegator <url>        Delegator endpoint URL\n")
		fmt.Fprintf(os.Stderr, "  --method <method>        HTTP method (default: GET)\n")
		fmt.Fprintf(os.Stderr, "  --data <body>            Request body (for POST)\n")
		fmt.Fprintf(os.Stderr, "  --nonce-key <WIF>        Private key for nonce input (required)\n")
		fmt.Fprintf(os.Stderr, "  --payment-key <WIF>      Private key for payment input (required)\n")
		fmt.Fprintf(os.Stderr, "  --payment-txid <hex>     Payment UTXO txid (required)\n")
		fmt.Fprintf(os.Stderr, "  --payment-vout <n>       Payment UTXO vout (default: 0)\n")
		fmt.Fprintf(os.Stderr, "  --payment-sats <n>       Payment UTXO satoshis (required)\n")
		fmt.Fprintf(os.Stderr, "  --payment-script <hex>   Payment UTXO locking script hex (required)\n")
		fmt.Fprintf(os.Stderr, "  --broadcast-url <url>    Broadcast endpoint URL\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  x402-client --nonce-key L1... --payment-key L2... --payment-txid abc... --payment-sats 100 --payment-script 76a9... http://localhost:8402/v1/expensive\n")
		os.Exit(1)
	}

	targetURL := flag.Arg(0)
	httpMethod := strings.ToUpper(*method)

	// Validate required flags
	if *nonceKeyWIF == "" || *paymentKeyWIF == "" {
		fmt.Fprintf(os.Stderr, "Error: --nonce-key and --payment-key are required\n")
		os.Exit(1)
	}
	if *paymentTxID == "" || *paymentSats == 0 || *paymentScript == "" {
		fmt.Fprintf(os.Stderr, "Error: --payment-txid, --payment-sats, and --payment-script are required\n")
		os.Exit(1)
	}

	// Parse keys
	nonceKey, err := ec.PrivateKeyFromWif(*nonceKeyWIF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing nonce key: %s\n", err)
		os.Exit(1)
	}
	paymentKey, err := ec.PrivateKeyFromWif(*paymentKeyWIF)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing payment key: %s\n", err)
		os.Exit(1)
	}

	// Derive delegator URL if not provided
	delegateEndpoint := *delegatorURL
	if delegateEndpoint == "" {
		u, err := url.Parse(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing URL: %s\n", err)
			os.Exit(1)
		}
		delegateEndpoint = fmt.Sprintf("%s://%s/delegate/x402", u.Scheme, u.Host)
	}

	// Derive broadcast URL if not provided
	bcastEndpoint := *broadcastURL
	if bcastEndpoint == "" {
		u, err := url.Parse(targetURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing URL: %s\n", err)
			os.Exit(1)
		}
		// Default: use WoC testnet
		bcastEndpoint = fmt.Sprintf("%s://%s/api/v1/tx", u.Scheme, u.Host)
	}

	// ──────────────────────────────────────────────────────────
	// Step 1: Make initial request — expect 402
	// ──────────────────────────────────────────────────────────
	fmt.Printf("-> %s %s\n", httpMethod, targetURL)
	var bodyReader io.Reader
	if *data != "" {
		bodyReader = strings.NewReader(*data)
	}
	req1, err := http.NewRequest(httpMethod, targetURL, bodyReader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %s\n", err)
		os.Exit(1)
	}
	if *data != "" {
		req1.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req1)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPaymentRequired {
		respBody, _ := io.ReadAll(resp.Body)
		fmt.Printf("<- %d (expected 402)\n", resp.StatusCode)
		fmt.Printf("%s\n", respBody)
		os.Exit(0)
	}

	fmt.Printf("<- 402 Payment Required\n")

	// ──────────────────────────────────────────────────────────
	// Step 2: Parse challenge from X402-Challenge header
	// ──────────────────────────────────────────────────────────
	challengeHeader := resp.Header.Get("X402-Challenge")
	if challengeHeader == "" {
		fmt.Fprintf(os.Stderr, "Error: no X402-Challenge header in 402 response\n")
		os.Exit(1)
	}

	acceptHeader := resp.Header.Get("X402-Accept")
	fmt.Printf("  Accept:  %s\n", acceptHeader)

	ch, err := challenge.Decode(challengeHeader)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding challenge: %s\n", err)
		os.Exit(1)
	}

	challengeHash, err := challenge.ComputeHash(ch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error computing challenge hash: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Challenge:\n")
	fmt.Printf("    Scheme:   %s v%s\n", ch.Scheme, ch.V)
	fmt.Printf("    Payee:    %s\n", truncate(ch.PayeeLockingScriptHex, 40))
	fmt.Printf("    Amount:   %d sats\n", ch.AmountSats)
	fmt.Printf("    Hash:     %s\n", truncate(challengeHash, 24))
	if ch.NonceUTXO != nil {
		fmt.Printf("    Nonce:    %s:%d\n", truncate(ch.NonceUTXO.TxID, 16), ch.NonceUTXO.Vout)
	}

	// ──────────────────────────────────────────────────────────
	// Step 3: Construct partial transaction (client responsibility)
	// ──────────────────────────────────────────────────────────
	fmt.Printf("\n  Constructing partial tx...\n")

	if ch.NonceUTXO == nil {
		fmt.Fprintf(os.Stderr, "Error: challenge has no nonce_utxo\n")
		os.Exit(1)
	}

	tx := transaction.NewTransaction()

	// Input 0: nonce UTXO (signed with nonceKey, 0xC1)
	nonceUnlocker, err := p2pkh.Unlock(nonceKey, &x402ClientSigHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating nonce unlocker: %s\n", err)
		os.Exit(1)
	}
	if err := tx.AddInputFrom(ch.NonceUTXO.TxID, ch.NonceUTXO.Vout, ch.NonceUTXO.LockingScriptHex, ch.NonceUTXO.Satoshis, nonceUnlocker); err != nil {
		fmt.Fprintf(os.Stderr, "Error adding nonce input: %s\n", err)
		os.Exit(1)
	}

	// Input 1: payment UTXO (signed with paymentKey, 0xC1)
	paymentUnlocker, err := p2pkh.Unlock(paymentKey, &x402ClientSigHash)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating payment unlocker: %s\n", err)
		os.Exit(1)
	}
	if err := tx.AddInputFrom(*paymentTxID, uint32(*paymentVout), *paymentScript, *paymentSats, paymentUnlocker); err != nil {
		fmt.Fprintf(os.Stderr, "Error adding payment input: %s\n", err)
		os.Exit(1)
	}

	// Output 0: payee
	payeeScriptBytes, err := hex.DecodeString(ch.PayeeLockingScriptHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding payee script: %s\n", err)
		os.Exit(1)
	}
	payeeScript := script.Script(payeeScriptBytes)
	tx.AddOutput(&transaction.TransactionOutput{
		Satoshis:      uint64(ch.AmountSats),
		LockingScript: &payeeScript,
	})

	// Sign all client inputs with 0xC1 sighash
	if err := tx.Sign(); err != nil {
		fmt.Fprintf(os.Stderr, "Error signing partial tx: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Partial tx: %d inputs, %d outputs\n", tx.InputCount(), len(tx.Outputs))

	// ──────────────────────────────────────────────────────────
	// Step 4: Send partial tx to delegator for fee addition
	// ──────────────────────────────────────────────────────────
	fmt.Printf("\n-> POST %s (fee delegation)\n", delegateEndpoint)

	delegReq := map[string]any{
		"partial_tx_hex":           tx.Hex(),
		"challenge_hash":           challengeHash,
		"payee_locking_script_hex": ch.PayeeLockingScriptHex,
		"amount_sats":              ch.AmountSats,
		"nonce_outpoint": map[string]any{
			"txid": ch.NonceUTXO.TxID,
			"vout": ch.NonceUTXO.Vout,
		},
	}
	delegBody, _ := json.Marshal(delegReq)

	delegResp, err := http.Post(delegateEndpoint, "application/json", bytes.NewReader(delegBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error calling delegator: %s\n", err)
		os.Exit(1)
	}
	defer delegResp.Body.Close()

	delegRespBody, _ := io.ReadAll(delegResp.Body)

	if delegResp.StatusCode != http.StatusOK {
		fmt.Printf("<- %d (delegator error)\n", delegResp.StatusCode)
		fmt.Printf("  %s\n", delegRespBody)
		os.Exit(1)
	}

	var delegResult struct {
		TxID     string `json:"txid"`
		RawTxHex string `json:"rawtx_hex"`
		Accepted bool   `json:"accepted"`
	}
	if err := json.Unmarshal(delegRespBody, &delegResult); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing delegator response: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("<- 200 Delegation accepted\n")
	fmt.Printf("  TxID:    %s\n", truncate(delegResult.TxID, 24))

	// ──────────────────────────────────────────────────────────
	// Step 5: Broadcast to network (client responsibility per spec)
	// ──────────────────────────────────────────────────────────
	fmt.Printf("\n-> Broadcasting tx %s...\n", truncate(delegResult.TxID, 16))

	bcastBody, _ := json.Marshal(map[string]string{
		"txhex": delegResult.RawTxHex,
	})
	bcastResp, err := http.Post(bcastEndpoint, "application/json", bytes.NewReader(bcastBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: broadcast failed: %s (continuing with proof)\n", err)
	} else {
		defer bcastResp.Body.Close()
		bcastRespBody, _ := io.ReadAll(bcastResp.Body)
		if bcastResp.StatusCode == http.StatusOK {
			fmt.Printf("<- 200 Broadcast accepted\n")
		} else {
			fmt.Printf("<- %d Broadcast response: %s\n", bcastResp.StatusCode, string(bcastRespBody))
		}
	}

	// ──────────────────────────────────────────────────────────
	// Step 6: Build spec-compliant proof
	// ──────────────────────────────────────────────────────────

	rawTxBytes, err := hex.DecodeString(delegResult.RawTxHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error decoding rawtx hex: %s\n", err)
		os.Exit(1)
	}
	rawTxB64 := base64.StdEncoding.EncodeToString(rawTxBytes)

	var dataBytes []byte
	if *data != "" {
		dataBytes = []byte(*data)
	}
	targetParsed, _ := url.Parse(targetURL)

	proof := &gatekeeper.Proof{
		V:               challenge.Version,
		Scheme:          challenge.Scheme,
		TxID:            delegResult.TxID,
		RawTxB64:        rawTxB64,
		ChallengeSHA256: challengeHash,
		Request: gatekeeper.RequestBinding{
			Domain:           targetParsed.Host,
			Method:           httpMethod,
			Path:             targetParsed.Path,
			Query:            targetParsed.RawQuery,
			ReqHeadersSHA256: challenge.HashHeaders(req1.Header, gatekeeper.HeaderAllowlist),
			ReqBodySHA256:    challenge.HashBody(dataBytes),
		},
	}

	proofEncoded, err := gatekeeper.EncodeProof(proof)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding proof: %s\n", err)
		os.Exit(1)
	}

	// ──────────────────────────────────────────────────────────
	// Step 7: Retry request with X402-Proof header
	// ──────────────────────────────────────────────────────────
	fmt.Printf("\n-> %s %s (with X402-Proof)\n", httpMethod, targetURL)

	var retryBody io.Reader
	if *data != "" {
		retryBody = strings.NewReader(*data)
	}
	req2, err := http.NewRequest(httpMethod, targetURL, retryBody)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating request: %s\n", err)
		os.Exit(1)
	}
	if *data != "" {
		req2.Header.Set("Content-Type", "application/json")
	}
	req2.Header.Set("X402-Proof", proofEncoded)

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
	defer resp2.Body.Close()

	resp2Body, _ := io.ReadAll(resp2.Body)
	fmt.Printf("<- %d\n", resp2.StatusCode)

	if receipt := resp2.Header.Get("X402-Receipt"); receipt != "" {
		fmt.Printf("  Receipt: %s\n", truncate(receipt, 24))
	}
	if status := resp2.Header.Get("X402-Status"); status != "" {
		fmt.Printf("  Status:  %s\n", status)
	}

	// Pretty-print JSON response
	var prettyJSON map[string]any
	if err := json.Unmarshal(resp2Body, &prettyJSON); err == nil {
		formatted, _ := json.MarshalIndent(prettyJSON, "  ", "  ")
		fmt.Printf("  %s\n", formatted)
	} else {
		fmt.Printf("  %s\n", resp2Body)
	}

	if resp2.StatusCode == 200 {
		fmt.Printf("\nPayment successful!\n")
	} else {
		fmt.Printf("\nPayment failed (status %d)\n", resp2.StatusCode)
		os.Exit(1)
	}
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
