// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0


package broadcast

import (
	"sync"
	"sync/atomic"
	"time"
)

// ServiceHealth represents the health status of a single broadcaster service.
type ServiceHealth struct {
	Healthy   bool      `json:"healthy"`
	LastCheck time.Time `json:"lastCheck"`
	LastError string    `json:"lastError,omitempty"`
	Service   string    `json:"service"` // "gorilla", "woc"
	Role      string    `json:"role"`    // "broadcast", "status"
}

// BroadcastStats holds cumulative broadcast statistics for the composite
// broadcaster, exposed to the dashboard for visibility into fallback behavior.
type BroadcastStats struct {
	PrimarySuccess   int64 `json:"primarySuccess"`   // broadcasts that succeeded on ARC
	PrimaryFailed    int64 `json:"primaryFailed"`     // ARC failures (transport + policy)
	FallbackSuccess  int64 `json:"fallbackSuccess"`   // broadcasts that succeeded on WoC after ARC failure
	FallbackFailed   int64 `json:"fallbackFailed"`    // WoC also failed (both down)
	FeePolicyRejects int64 `json:"feePolicyRejects"`  // ARC fee-too-low rejections (461/465)
}

// HealthTracker tracks the health of broadcaster services.
// It records successes and failures for each (service, role) pair,
// allowing the dashboard to display per-service health indicators.
type HealthTracker struct {
	mu       sync.RWMutex
	services map[string]*ServiceHealth // key = "gorilla:broadcast", "woc:status", etc.

	// Cumulative broadcast counters (lock-free atomics)
	primarySuccess   atomic.Int64
	primaryFailed    atomic.Int64
	fallbackSuccess  atomic.Int64
	fallbackFailed   atomic.Int64
	feePolicyRejects atomic.Int64
}

// NewHealthTracker creates a new health tracker.
func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		services: make(map[string]*ServiceHealth),
	}
}

// key builds the map key for a (service, role) pair.
func key(service, role string) string {
	return service + ":" + role
}

// RecordSuccess marks a service+role as healthy.
func (h *HealthTracker) RecordSuccess(service, role string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	k := key(service, role)
	sh, ok := h.services[k]
	if !ok {
		sh = &ServiceHealth{Service: service, Role: role}
		h.services[k] = sh
	}
	sh.Healthy = true
	sh.LastCheck = time.Now()
	sh.LastError = ""
}

// RecordFailure marks a service+role as unhealthy with an error message.
func (h *HealthTracker) RecordFailure(service, role, errMsg string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	k := key(service, role)
	sh, ok := h.services[k]
	if !ok {
		sh = &ServiceHealth{Service: service, Role: role}
		h.services[k] = sh
	}
	sh.Healthy = false
	sh.LastCheck = time.Now()
	sh.LastError = errMsg
}

// RecordPrimarySuccess increments the primary success counter.
func (h *HealthTracker) RecordPrimarySuccess() { h.primarySuccess.Add(1) }

// RecordPrimaryFailed increments the primary failure counter.
func (h *HealthTracker) RecordPrimaryFailed() { h.primaryFailed.Add(1) }

// RecordFallbackSuccess increments the fallback success counter.
func (h *HealthTracker) RecordFallbackSuccess() { h.fallbackSuccess.Add(1) }

// RecordFallbackFailed increments the fallback failure counter.
func (h *HealthTracker) RecordFallbackFailed() { h.fallbackFailed.Add(1) }

// RecordFeePolicyReject increments the fee-policy rejection counter.
func (h *HealthTracker) RecordFeePolicyReject() { h.feePolicyRejects.Add(1) }

// Stats returns a snapshot of the cumulative broadcast statistics.
func (h *HealthTracker) Stats() BroadcastStats {
	return BroadcastStats{
		PrimarySuccess:   h.primarySuccess.Load(),
		PrimaryFailed:    h.primaryFailed.Load(),
		FallbackSuccess:  h.fallbackSuccess.Load(),
		FallbackFailed:   h.fallbackFailed.Load(),
		FeePolicyRejects: h.feePolicyRejects.Load(),
	}
}

// Get returns the health status for a specific service+role, or nil if not tracked yet.
func (h *HealthTracker) Get(service, role string) *ServiceHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if sh, ok := h.services[key(service, role)]; ok {
		// Return a copy to avoid data races on the caller side
		cp := *sh
		return &cp
	}
	return nil
}

// All returns a snapshot of all tracked service health statuses.
func (h *HealthTracker) All() map[string]*ServiceHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string]*ServiceHealth, len(h.services))
	for k, v := range h.services {
		cp := *v
		result[k] = &cp
	}
	return result
}
