// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


// Package client provides an HTTP client for the x402 gateway.
// It wraps challenge acquisition, proof building, proof submission,
// delegation, and health endpoints into typed methods.
package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GatewayClient is a thin HTTP wrapper around the x402 gateway API.
type GatewayClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New creates a GatewayClient with sensible defaults.
func New(baseURL string) *GatewayClient {
	return &GatewayClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// -------------------------------------------------------------------
// Response types
// -------------------------------------------------------------------

// Challenge is a decoded X402-Challenge from the gateway.
type Challenge struct {
	V                       string    `json:"v"`
	Scheme                  string    `json:"scheme"`
	AmountSats              int64     `json:"amount_sats"`
	PayeeLockingScriptHex   string    `json:"payee_locking_script_hex"`
	ExpiresAt               int64     `json:"expires_at"`
	Domain                  string    `json:"domain"`
	Method                  string    `json:"method"`
	Path                    string    `json:"path"`
	Query                   string    `json:"query"`
	ReqHeadersSHA256        string    `json:"req_headers_sha256"`
	ReqBodySHA256           string    `json:"req_body_sha256"`
	NonceUTXO               *NonceRef `json:"nonce_utxo"`
	RequireMempoolAccept    bool      `json:"require_mempool_accept"`
	ConfirmationsRequired   int       `json:"confirmations_required"`
	Raw                     string    `json:"-"` // original base64 header value
}

// NonceRef is the nonce UTXO embedded in a challenge.
type NonceRef struct {
	TxID             string `json:"txid"`
	Vout             uint32 `json:"vout"`
	Satoshis         int64  `json:"satoshis"`
	LockingScriptHex string `json:"locking_script_hex"`
}

// ProofResponse is the response from /demo/build-proof.
type ProofResponse struct {
	ProofHeader   string `json:"proof_header"`
	TxID          string `json:"txid"`
	ChallengeHash string `json:"challenge_hash"`
	Error         string `json:"error,omitempty"`
}

// DelegationResult is the response from /delegate/x402 on success.
type DelegationResult struct {
	TxID     string `json:"txid"`
	RawTxHex string `json:"rawtx_hex"`
	Accepted bool   `json:"accepted"`
}

// GatewayError is an error response from the gateway.
type GatewayError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status,omitempty"`
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("%s: %s (HTTP %d)", e.Code, e.Message, e.Status)
}

// PoolStats is pool-level statistics from /health.
type PoolStats struct {
	Total     int `json:"total"`
	Available int `json:"available"`
	Leased    int `json:"leased"`
	Spent     int `json:"spent"`
}

// HealthResponse is the response from /health.
type HealthResponse struct {
	Status      string    `json:"status"`
	Version     string    `json:"version"`
	Network     string    `json:"network"`
	NoncePool   PoolStats `json:"nonce_pool"`
	FeePool     PoolStats `json:"fee_pool"`
	PaymentPool PoolStats `json:"payment_pool"`
}

// -------------------------------------------------------------------
// API methods
// -------------------------------------------------------------------

// GetChallenge requests the protected resource and returns the 402 challenge.
func (c *GatewayClient) GetChallenge() (*Challenge, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/v1/expensive")
	if err != nil {
		return nil, fmt.Errorf("GET /v1/expensive: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusPaymentRequired {
		return nil, fmt.Errorf("expected 402, got %d", resp.StatusCode)
	}

	raw := resp.Header.Get("X402-Challenge")
	if raw == "" {
		return nil, fmt.Errorf("missing X402-Challenge header")
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		// try base64url
		decoded, err = base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decode challenge: %w", err)
		}
	}

	var ch Challenge
	if err := json.Unmarshal(decoded, &ch); err != nil {
		return nil, fmt.Errorf("unmarshal challenge: %w", err)
	}
	ch.Raw = raw
	return &ch, nil
}

// BuildProof calls /demo/build-proof with the given challenge.
func (c *GatewayClient) BuildProof(challengeEncoded string) (*ProofResponse, error) {
	body, _ := json.Marshal(map[string]string{"challenge": challengeEncoded})
	resp, err := c.HTTPClient.Post(
		c.BaseURL+"/demo/build-proof",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("POST /demo/build-proof: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("build-proof returned %d: %s", resp.StatusCode, string(data))
	}

	var pr ProofResponse
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal proof response: %w", err)
	}
	return &pr, nil
}

// SubmitProof submits a proof to the protected resource.
// Returns (status_code, response_body, error).
func (c *GatewayClient) SubmitProof(proofHeader string) (int, []byte, error) {
	req, _ := http.NewRequest("GET", c.BaseURL+"/v1/expensive", nil)
	req.Header.Set("X402-Proof", proofHeader)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("GET /v1/expensive with proof: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// Delegate sends a delegation request to /delegate/x402.
// Returns (status_code, response_body, error).
func (c *GatewayClient) Delegate(partialTxHex, challengeHash, nonceTxID string, nonceVout uint32) (int, []byte, error) {
	payload := map[string]any{
		"partial_tx_hex":  partialTxHex,
		"challenge_hash":  challengeHash,
		"nonce_outpoint": map[string]any{
			"txid": nonceTxID,
			"vout": nonceVout,
		},
	}
	body, _ := json.Marshal(payload)

	resp, err := c.HTTPClient.Post(
		c.BaseURL+"/delegate/x402",
		"application/json",
		bytes.NewReader(body),
	)
	if err != nil {
		return 0, nil, fmt.Errorf("POST /delegate/x402: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, nil
}

// Health calls /health and returns pool statistics.
func (c *GatewayClient) Health() (*HealthResponse, error) {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/health")
	if err != nil {
		return nil, fmt.Errorf("GET /health: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var h HealthResponse
	if err := json.Unmarshal(data, &h); err != nil {
		return nil, fmt.Errorf("unmarshal health: %w", err)
	}
	return &h, nil
}
