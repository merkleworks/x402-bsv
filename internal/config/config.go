// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds all gateway configuration.
type Config struct {
	// BSV private key in WIF format (legacy single-key mode)
	BSVPrivateKey string

	// BIP32 extended private key (HD wallet mode, alternative to BSV_PRIVATE_KEY)
	XPRIV string

	// BSV network: "mainnet" or "testnet"
	BSVNetwork string

	// Service payee address
	PayeeAddress string

	// HTTP server port
	Port int

	// Pool initial size (fee + payment pools)
	PoolSize int

	// Pool lease TTL
	LeaseTTL time.Duration

	// Fee rate in sat/byte (BSV standard: 1 sat/KB = 0.001 sat/byte)
	FeeRate float64

	// Broadcaster type: "mock", "woc", or "composite"
	Broadcaster string

	// GorillaPool ARC configuration (used when Broadcaster="composite")
	ArcURL    string // ARC_URL (default: https://arc.gorillapool.io/v1)
	ArcAPIKey string // ARC_API_KEY (optional Bearer token)

	// Daily fee budget in satoshis (0 = unlimited)
	DailyFeeBudget uint64

	// Redis configuration
	RedisURL     string // REDIS_URL (e.g. redis://localhost:6379)
	RedisEnabled bool   // REDIS_ENABLED (true = Redis pools, false = in-memory)

	// Pool thresholds for auto-refill
	PoolReplenishThreshold int // trigger refill when available < this (default 500)
	PoolOptimalSize        int // target size after refill (default 5000)

	// Treasury UTXO watcher poll interval in seconds (0 = disabled)
	TreasuryPollInterval int

	// Fee pool UTXO denomination in satoshis (1–1000, default 1)
	FeeUTXOSats uint64 // FEE_UTXO_SATS (denomination per fee pool UTXO)

	// Profile B: Gateway Template mode
	TemplateMode      bool   // TEMPLATE_MODE (true = generate pre-signed templates per nonce)
	TemplatePriceSats uint64 // TEMPLATE_PRICE_SATS (price embedded in each template, default 100)

	// Delegator configuration
	DelegatorPort     int  // DELEGATOR_PORT (default 8403, used by cmd/delegator)
	DelegatorEmbedded bool // DELEGATOR_EMBEDDED (true = gateway hosts delegator in-process)
	DelegatorURL      string // DELEGATOR_URL (for dashboard config, e.g. http://localhost:8403)

	// WhatsOnChain API base URL override (optional)
	WocApiURL string // WOC_API_URL (e.g. https://api.whatsonchain.com/v1/bsv/main)

	// Backward-compatible aliases (deprecated, use PoolSize/LeaseTTL)
	NoncePoolSize int
	NonceLeaseTTL time.Duration
}

// WocBaseURL returns the effective WoC API base URL.
// If WOC_API_URL is set, it is used verbatim. Otherwise, the URL is
// derived from BSV_NETWORK (e.g. https://api.whatsonchain.com/v1/bsv/main).
func (c *Config) WocBaseURL() string {
	if c.WocApiURL != "" {
		return c.WocApiURL
	}
	network := "test"
	if c.IsMainnet() {
		network = "main"
	}
	return fmt.Sprintf("https://api.whatsonchain.com/v1/bsv/%s", network)
}

// Load reads configuration from environment variables.
// Required: one of XPRIV or BSV_PRIVATE_KEY, plus FEE_RATE and BROADCASTER.
func Load() (*Config, error) {
	poolSize := envIntOrDefault("POOL_SIZE", envIntOrDefault("NONCE_POOL_SIZE", 100))
	leaseTTL := time.Duration(envIntOrDefault("LEASE_TTL", envIntOrDefault("NONCE_LEASE_TTL", 300))) * time.Second

	cfg := &Config{
		BSVPrivateKey:  os.Getenv("BSV_PRIVATE_KEY"),
		XPRIV:          os.Getenv("XPRIV"),
		BSVNetwork:     envOrDefault("BSV_NETWORK", "testnet"),
		PayeeAddress:   os.Getenv("PAYEE_ADDRESS"),
		Port:           envIntOrDefault("PORT", 8402),
		PoolSize:       poolSize,
		LeaseTTL:       leaseTTL,
		FeeRate:        envFloatOrDefault("FEE_RATE", 0),
		Broadcaster:    os.Getenv("BROADCASTER"),
		ArcURL:         envOrDefault("ARC_URL", "https://arc.gorillapool.io/v1"),
		ArcAPIKey:      os.Getenv("ARC_API_KEY"),
		DailyFeeBudget:         uint64(envIntOrDefault("DAILY_FEE_BUDGET", 0)),
		RedisURL:               os.Getenv("REDIS_URL"),
		RedisEnabled:           envBoolOrDefault("REDIS_ENABLED", false),
		PoolReplenishThreshold: envIntOrDefault("POOL_REPLENISH_THRESHOLD", 500),
		PoolOptimalSize:        envIntOrDefault("POOL_OPTIMAL_SIZE", 5000),
		TreasuryPollInterval:   envIntOrDefault("TREASURY_POLL_INTERVAL", 60),

		// Fee pool denomination — each fee UTXO should match the payment price
		// so a single fee input covers the payment output.
		FeeUTXOSats: uint64(envIntOrDefault("FEE_UTXO_SATS", 100)),

		// Profile B
		TemplateMode:      envBoolOrDefault("TEMPLATE_MODE", false),
		TemplatePriceSats: uint64(envIntOrDefault("TEMPLATE_PRICE_SATS", 100)),

		// Delegator
		DelegatorPort:     envIntOrDefault("DELEGATOR_PORT", 8403),
		DelegatorEmbedded: envBoolOrDefault("DELEGATOR_EMBEDDED", false),
		DelegatorURL:      os.Getenv("DELEGATOR_URL"),

		// WhatsOnChain API URL override
		WocApiURL: os.Getenv("WOC_API_URL"),

		// Backward-compatible aliases
		NoncePoolSize: poolSize,
		NonceLeaseTTL: leaseTTL,
	}

	if cfg.BSVPrivateKey == "" && cfg.XPRIV == "" {
		return nil, fmt.Errorf("one of XPRIV or BSV_PRIVATE_KEY is required")
	}
	if cfg.BSVNetwork != "mainnet" && cfg.BSVNetwork != "testnet" {
		return nil, fmt.Errorf("BSV_NETWORK must be 'mainnet' or 'testnet', got %q", cfg.BSVNetwork)
	}
	if cfg.FeeRate <= 0 {
		return nil, fmt.Errorf("FEE_RATE is required (set in .env, e.g. FEE_RATE=0.001 for BSV standard 1 sat/KB)")
	}
	if cfg.Broadcaster == "" {
		return nil, fmt.Errorf("BROADCASTER is required (set in .env, e.g. BROADCASTER=mock, BROADCASTER=woc, or BROADCASTER=composite)")
	}
	switch cfg.Broadcaster {
	case "mock", "woc", "composite":
		// valid
	default:
		return nil, fmt.Errorf("BROADCASTER must be \"mock\", \"woc\", or \"composite\", got %q", cfg.Broadcaster)
	}
	if cfg.FeeUTXOSats < 1 || cfg.FeeUTXOSats > 1000 {
		return nil, fmt.Errorf("FEE_UTXO_SATS must be between 1 and 1000, got %d", cfg.FeeUTXOSats)
	}

	return cfg, nil
}

// IsMainnet returns true if the network is mainnet.
func (c *Config) IsMainnet() bool {
	return c.BSVNetwork == "mainnet"
}

// RuntimeMode returns "mock" or "live" based on the broadcaster config.
// Used to namespace Redis keys so mock and live data never intersect.
func (c *Config) RuntimeMode() string {
	if c.Broadcaster == "mock" {
		return "mock"
	}
	return "live"
}

// PoolPrefix returns the namespaced Redis key prefix for a pool.
// Format: "<mode>:<poolName>:" — e.g. PoolPrefix("live", "nonce") → "live:nonce:"
func PoolPrefix(mode, poolName string) string {
	return mode + ":" + poolName + ":"
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envBoolOrDefault(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		return v == "true" || v == "1" || v == "yes"
	}
	return def
}

func envFloatOrDefault(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
