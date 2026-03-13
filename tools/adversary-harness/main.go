// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


// adversary-harness is a standalone adversarial test tool for the x402 gateway.
//
// It simulates six classes of hostile behaviour:
//   - Miner ordering variance (competing transactions)
//   - Mempool suppression (visibility failures)
//   - Delayed broadcast (timing attacks)
//   - Fee delegation abuse (concurrent hammering)
//   - Nonce lease TTL reclaim (pool exhaustion + TTL recovery)
//   - Gateway crash / restart recovery (volatile state resilience)
//
// Usage:
//
//	go run ./tools/adversary-harness -adversary=all -clients=50 -duration=60s
//	go run ./tools/adversary-harness -adversary=ttl -lease-ttl=30s
//	go run ./tools/adversary-harness -adversary=crash -restart-cmd="docker restart x402-gateway-x402-gateway-1"
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/merkleworks/x402-bsv/tools/adversary-harness/client"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/metrics"
	"github.com/merkleworks/x402-bsv/tools/adversary-harness/scenarios"
)

// timedScenarios are the original stress-test scenarios whose runtime
// is governed by the -duration flag.
var timedScenarios = map[string]bool{
	"mining":    true,
	"mempool":   true,
	"broadcast": true,
	"abuse":     true,
}

func main() {
	// ─── CLI flags ──────────────────────────────────────────────────

	adversary := flag.String("adversary", "all",
		"Scenario: mining, mempool, broadcast, abuse, ttl, crash, all")
	clientCount := flag.Int("clients", 10,
		"Concurrent workers per timed scenario")
	duration := flag.Duration("duration", 30*time.Second,
		"Total duration for timed scenarios (mining/mempool/broadcast/abuse)")
	baseURL := flag.String("url", "http://localhost:8402",
		"Gateway base URL")
	verbose := flag.Bool("verbose", false,
		"Enable debug logging")

	// Scenario-specific flags
	leaseTTL := flag.Duration("lease-ttl", 300*time.Second,
		"Expected nonce lease TTL for the ttl scenario (must match gateway LEASE_TTL)")
	restartCmd := flag.String("restart-cmd", "docker restart x402-gateway-x402-gateway-1",
		"Shell command to restart the gateway for the crash scenario")

	flag.Parse()

	// ─── Logging ────────────────────────────────────────────────────

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	// ─── Determine scenario list ────────────────────────────────────

	scenarioList := parseScenarios(*adversary)

	// Separate timed (duration-bounded) from untimed (self-paced).
	var timed, untimed []string
	for _, s := range scenarioList {
		if timedScenarios[s] {
			timed = append(timed, s)
		} else {
			untimed = append(untimed, s)
		}
	}

	// ─── Banner ─────────────────────────────────────────────────────

	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════════════╗")
	fmt.Println("  ║   x402 ADVERSARY HARNESS                     ║")
	fmt.Println("  ║   Adversarial protocol testing tool           ║")
	fmt.Println("  ╠═══════════════════════════════════════════════╣")
	fmt.Printf("  ║  Target:      %-32s ║\n", *baseURL)
	fmt.Printf("  ║  Scenario:    %-32s ║\n", *adversary)
	fmt.Printf("  ║  Clients:     %-32d ║\n", *clientCount)
	fmt.Printf("  ║  Duration:    %-32s ║\n", *duration)
	if containsScenario(scenarioList, "ttl") {
		fmt.Printf("  ║  Lease TTL:   %-32s ║\n", *leaseTTL)
	}
	if containsScenario(scenarioList, "crash") {
		cmd := *restartCmd
		if len(cmd) > 32 {
			cmd = cmd[:29] + "..."
		}
		fmt.Printf("  ║  Restart cmd: %-32s ║\n", cmd)
	}
	fmt.Println("  ╚═══════════════════════════════════════════════╝")
	fmt.Println()

	// ─── Gateway connectivity ───────────────────────────────────────

	gw := client.New(*baseURL)

	health, err := gw.Health()
	if err != nil {
		logger.Error("cannot reach gateway", "url", *baseURL, "error", err)
		os.Exit(1)
	}
	logger.Info("gateway connected",
		"version", health.Version,
		"network", health.Network,
		"nonce_available", health.NoncePool.Available,
		"fee_available", health.FeePool.Available,
	)

	// ─── Context & signal handling ──────────────────────────────────
	//
	// We use a signal-aware context WITHOUT a global timeout.
	// Timed scenarios get per-scenario timeouts derived from -duration.
	// Untimed scenarios (ttl, crash) manage their own deadlines.

	sigCtx, sigCancel := context.WithCancel(context.Background())
	defer sigCancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			logger.Info("received signal, stopping", "signal", sig)
			sigCancel()
		case <-sigCtx.Done():
		}
	}()

	// ─── Metrics ────────────────────────────────────────────────────

	m := metrics.New()
	stop := make(chan struct{})
	m.StartReporter(5*time.Second, stop)

	startTime := time.Now()

	// ─── Phase 1: Run timed scenarios ───────────────────────────────

	if len(timed) > 0 {
		perScenario := *duration / time.Duration(len(timed))
		logger.Info("running timed scenarios",
			"scenarios", timed,
			"per_scenario", perScenario,
		)

		for _, s := range timed {
			select {
			case <-sigCtx.Done():
				goto done
			default:
			}

			scenarioCtx, scenarioCancel := context.WithTimeout(sigCtx, perScenario)

			logger.Info("═══════════════════════════════════════")
			logger.Info("SCENARIO: "+strings.ToUpper(s), "budget", perScenario)
			logger.Info("═══════════════════════════════════════")

			switch s {
			case "mining":
				scenarios.MinerOrdering(scenarioCtx, gw, m, *clientCount, logger)
			case "mempool":
				scenarios.MempoolSuppression(scenarioCtx, gw, m, *clientCount, logger)
			case "broadcast":
				scenarios.DelayedBroadcast(scenarioCtx, gw, m, *clientCount, logger)
			case "abuse":
				scenarios.FeeAbuse(scenarioCtx, gw, m, *clientCount, logger)
			}

			scenarioCancel()
		}
	}

	// ─── Phase 2: Run untimed scenarios ─────────────────────────────
	//
	// These scenarios have their own inherent durations:
	//   ttl   — waits for lease TTL + reclaim buffer (~5+ minutes)
	//   crash — restarts the gateway process (~30-120 seconds)
	//
	// They are always run AFTER the timed scenarios. The crash
	// scenario runs last because it disrupts the gateway process.

	for _, s := range untimed {
		select {
		case <-sigCtx.Done():
			goto done
		default:
		}

		logger.Info("═══════════════════════════════════════")
		logger.Info("SCENARIO: "+strings.ToUpper(s), "budget", "self-paced")
		logger.Info("═══════════════════════════════════════")

		switch s {
		case "ttl":
			// TTL timeout = lease TTL + 2 minutes (reclaim buffer + margin)
			ttlTimeout := *leaseTTL + 2*time.Minute
			ttlCtx, ttlCancel := context.WithTimeout(sigCtx, ttlTimeout)
			scenarios.LeaseReclaim(ttlCtx, gw, m, *leaseTTL, logger)
			ttlCancel()

		case "crash":
			// Crash scenario: generous 5-minute timeout
			crashCtx, crashCancel := context.WithTimeout(sigCtx, 5*time.Minute)
			scenarios.CrashRecovery(crashCtx, gw, m, *restartCmd, logger)
			crashCancel()

		default:
			logger.Error("unknown scenario", "name", s)
		}
	}

done:
	elapsed := time.Since(startTime)
	close(stop)

	// ─── Final report ───────────────────────────────────────────────

	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════")
	fmt.Println("  FINAL RESULTS")
	fmt.Printf("  Elapsed: %s\n", elapsed.Truncate(time.Millisecond))
	fmt.Println("═══════════════════════════════════════════════")
	m.Report()

	// Final health check (gateway may have been restarted by crash scenario)
	healthAfter, err := gw.Health()
	if err == nil {
		fmt.Printf("\n  Pool state after test:\n")
		fmt.Printf("    Nonce:   %d avail / %d leased / %d spent / %d total\n",
			healthAfter.NoncePool.Available, healthAfter.NoncePool.Leased,
			healthAfter.NoncePool.Spent, healthAfter.NoncePool.Total)
		fmt.Printf("    Fee:     %d avail / %d leased / %d spent / %d total\n",
			healthAfter.FeePool.Available, healthAfter.FeePool.Leased,
			healthAfter.FeePool.Spent, healthAfter.FeePool.Total)
		fmt.Printf("    Payment: %d avail / %d leased / %d spent / %d total\n",
			healthAfter.PaymentPool.Available, healthAfter.PaymentPool.Leased,
			healthAfter.PaymentPool.Spent, healthAfter.PaymentPool.Total)
	} else {
		fmt.Printf("\n  Pool state: unavailable (gateway may be restarting)\n")
	}

	// Check for CRITICAL failures
	snap := m.Snapshot()
	criticals := 0
	for k, v := range snap {
		if strings.HasPrefix(k, "CRITICAL_") && v > 0 {
			fmt.Printf("\n  ⚠️  CRITICAL FAILURE: %s = %d\n", k, v)
			criticals++
		}
	}

	if criticals > 0 {
		fmt.Printf("\n  RESULT: FAIL (%d critical vulnerabilities detected)\n\n", criticals)
		os.Exit(1)
	} else {
		fmt.Printf("\n  RESULT: PASS (all protocol invariants held)\n\n")
	}
}

// parseScenarios converts the -adversary flag value into an ordered
// list of scenario names. "all" expands to all six scenarios with
// crash intentionally last (it disrupts the gateway process).
func parseScenarios(input string) []string {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "all" {
		return []string{"mining", "mempool", "broadcast", "abuse", "ttl", "crash"}
	}
	parts := strings.Split(input, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// containsScenario checks whether a scenario name is in the list.
func containsScenario(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}
