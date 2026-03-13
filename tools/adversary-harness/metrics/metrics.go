// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


// Package metrics provides thread-safe atomic counters and a periodic
// console reporter for the adversary harness.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Collector holds named counters and latency accumulators.
type Collector struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Int64
	// latency tracking: total_ns / count
	latTotal map[string]*atomic.Int64
	latCount map[string]*atomic.Int64
	started  time.Time
}

// New creates a Collector.
func New() *Collector {
	return &Collector{
		counters: make(map[string]*atomic.Int64),
		latTotal: make(map[string]*atomic.Int64),
		latCount: make(map[string]*atomic.Int64),
		started:  time.Now(),
	}
}

func (c *Collector) counter(name string) *atomic.Int64 {
	c.mu.RLock()
	v, ok := c.counters[name]
	c.mu.RUnlock()
	if ok {
		return v
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok = c.counters[name]; ok {
		return v
	}
	v = &atomic.Int64{}
	c.counters[name] = v
	return v
}

// Inc increments a named counter by 1.
func (c *Collector) Inc(name string) {
	c.counter(name).Add(1)
}

// Add adds delta to a named counter.
func (c *Collector) Add(name string, delta int64) {
	c.counter(name).Add(delta)
}

// Get returns the current value of a counter.
func (c *Collector) Get(name string) int64 {
	return c.counter(name).Load()
}

// RecordLatency records a single latency observation.
func (c *Collector) RecordLatency(name string, d time.Duration) {
	c.mu.RLock()
	lt, okT := c.latTotal[name]
	lc, okC := c.latCount[name]
	c.mu.RUnlock()

	if !okT || !okC {
		c.mu.Lock()
		if lt, okT = c.latTotal[name]; !okT {
			lt = &atomic.Int64{}
			c.latTotal[name] = lt
		}
		if lc, okC = c.latCount[name]; !okC {
			lc = &atomic.Int64{}
			c.latCount[name] = lc
		}
		c.mu.Unlock()
	}

	lt.Add(d.Nanoseconds())
	lc.Add(1)
}

// AvgLatency returns the average latency for a named metric.
func (c *Collector) AvgLatency(name string) time.Duration {
	c.mu.RLock()
	lt, okT := c.latTotal[name]
	lc, okC := c.latCount[name]
	c.mu.RUnlock()
	if !okT || !okC {
		return 0
	}
	cnt := lc.Load()
	if cnt == 0 {
		return 0
	}
	return time.Duration(lt.Load() / cnt)
}

// Snapshot returns a copy of all counter values.
func (c *Collector) Snapshot() map[string]int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snap := make(map[string]int64, len(c.counters))
	for k, v := range c.counters {
		snap[k] = v.Load()
	}
	return snap
}

// Report prints the current metrics to stdout.
func (c *Collector) Report() {
	elapsed := time.Since(c.started).Truncate(time.Second)
	snap := c.Snapshot()

	// Sort keys for stable output.
	keys := make([]string, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("\n╔══════════════════════════════════════════════╗\n"))
	b.WriteString(fmt.Sprintf("║  ADVERSARY HARNESS STATUS  (%s elapsed)  \n", elapsed))
	b.WriteString(fmt.Sprintf("╠══════════════════════════════════════════════╣\n"))

	for _, k := range keys {
		v := snap[k]
		b.WriteString(fmt.Sprintf("║  %-36s %6d  ║\n", k, v))
	}

	// Latencies
	c.mu.RLock()
	latKeys := make([]string, 0, len(c.latCount))
	for k := range c.latCount {
		latKeys = append(latKeys, k)
	}
	c.mu.RUnlock()
	sort.Strings(latKeys)

	if len(latKeys) > 0 {
		b.WriteString(fmt.Sprintf("╠══════════════════════════════════════════════╣\n"))
		for _, k := range latKeys {
			avg := c.AvgLatency(k)
			b.WriteString(fmt.Sprintf("║  avg_%-31s %5dms  ║\n", k, avg.Milliseconds()))
		}
	}

	// Derived rates
	total := snap["challenges_requested"]
	if total > 0 {
		if proofs := snap["proof_accepted"]; proofs > 0 {
			rate := float64(proofs) / float64(total) * 100
			b.WriteString(fmt.Sprintf("╠══════════════════════════════════════════════╣\n"))
			b.WriteString(fmt.Sprintf("║  proof_success_rate                 %5.1f%%  ║\n", rate))
		}
		if pending := snap["nonce_pending"]; pending > 0 {
			rate := float64(pending) / float64(total) * 100
			b.WriteString(fmt.Sprintf("║  nonce_pending_rate                 %5.1f%%  ║\n", rate))
		}
	}

	b.WriteString(fmt.Sprintf("╚══════════════════════════════════════════════╝\n"))
	fmt.Print(b.String())
}

// StartReporter launches a goroutine that prints metrics every interval.
// It stops when the stop channel is closed.
func (c *Collector) StartReporter(interval time.Duration, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.Report()
			case <-stop:
				return
			}
		}
	}()
}
