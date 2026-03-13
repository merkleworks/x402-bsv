// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package dashboard

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// RequestStat records a single HTTP request for analytics.
type RequestStat struct {
	Timestamp time.Time
	Path      string
	Method    string
	Status    int
	Duration  time.Duration
	FeeSats   uint64
}

// StatsSummary provides aggregate statistics over a time window.
type StatsSummary struct {
	TotalRequests   int     `json:"totalRequests"`
	Payments        int     `json:"payments"`
	Challenges      int     `json:"challenges"`
	Errors          int     `json:"errors"`
	AvgDurationMS   float64 `json:"avgDurationMs"`
	TotalFeeSats    uint64  `json:"totalFeeSats"`
	UptimeSeconds   float64 `json:"uptimeSeconds"`
	NoncePool       any     `json:"noncePool"`
	FeePool         any     `json:"feePool"`
	PaymentPool     any     `json:"paymentPool"`
}

// TimeseriesPoint is a single time-series data point.
type TimeseriesPoint struct {
	Timestamp int64 `json:"timestamp"` // unix seconds
	Requests  int   `json:"requests"`
	Payments  int   `json:"payments"`
	Errors    int   `json:"errors"`
}

// StatsCollector records request statistics in a ring buffer.
type StatsCollector struct {
	mu             sync.Mutex
	stats          []RequestStat
	maxSize        int
	bucketDuration time.Duration
}

// NewStatsCollector creates a stats collector with the given max entries and bucket duration.
func NewStatsCollector(maxSize int, bucketDuration time.Duration) *StatsCollector {
	return &StatsCollector{
		stats:          make([]RequestStat, 0, maxSize),
		maxSize:        maxSize,
		bucketDuration: bucketDuration,
	}
}

// Record adds a new request stat.
func (sc *StatsCollector) Record(stat RequestStat) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.stats = append(sc.stats, stat)
	if len(sc.stats) > sc.maxSize {
		sc.stats = sc.stats[len(sc.stats)-sc.maxSize:]
	}
}

// Summary computes aggregate statistics over the given time window.
func (sc *StatsCollector) Summary(window time.Duration) StatsSummary {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	cutoff := time.Now().Add(-window)
	var summary StatsSummary
	var totalDuration time.Duration

	for _, s := range sc.stats {
		if s.Timestamp.Before(cutoff) {
			continue
		}
		summary.TotalRequests++
		totalDuration += s.Duration
		summary.TotalFeeSats += s.FeeSats

		if s.Status == 200 && isPaymentPath(s.Path) {
			summary.Payments++
		}
		if s.Status == 402 {
			summary.Challenges++
		}
		if s.Status >= 400 && s.Status != 402 {
			summary.Errors++
		}
	}

	if summary.TotalRequests > 0 {
		summary.AvgDurationMS = float64(totalDuration.Milliseconds()) / float64(summary.TotalRequests)
	}

	return summary
}

// Timeseries returns time-series data points for the given window.
func (sc *StatsCollector) Timeseries(window time.Duration) []TimeseriesPoint {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-window)
	numBuckets := int(window / sc.bucketDuration)
	if numBuckets <= 0 {
		numBuckets = 1
	}

	points := make([]TimeseriesPoint, numBuckets)
	for i := range points {
		bucketStart := cutoff.Add(time.Duration(i) * sc.bucketDuration)
		points[i].Timestamp = bucketStart.Unix()
	}

	for _, s := range sc.stats {
		if s.Timestamp.Before(cutoff) {
			continue
		}
		bucketIdx := int(s.Timestamp.Sub(cutoff) / sc.bucketDuration)
		if bucketIdx >= numBuckets {
			bucketIdx = numBuckets - 1
		}
		if bucketIdx < 0 {
			continue
		}

		points[bucketIdx].Requests++
		if s.Status == 200 && isPaymentPath(s.Path) {
			points[bucketIdx].Payments++
		}
		if s.Status >= 400 && s.Status != 402 {
			points[bucketIdx].Errors++
		}
	}

	return points
}

// isPaymentPath returns true if the path is a protected (paid) endpoint.
func isPaymentPath(path string) bool {
	return len(path) >= 4 && path[:4] == "/v1/"
}

// handleStatsSummary returns aggregate statistics.
func (d *DashboardAPI) handleStatsSummary() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		summary := d.stats.Summary(time.Hour)
		summary.UptimeSeconds = time.Since(d.startTime).Seconds()
		summary.NoncePool = d.noncePool.Stats()
		summary.FeePool = d.feePool.Stats()
		summary.PaymentPool = d.paymentPool.Stats()

		writeJSON(w, http.StatusOK, summary)
	}
}

// handleStatsTimeseries returns time-series data points.
func (d *DashboardAPI) handleStatsTimeseries() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		points := d.stats.Timeseries(time.Hour)
		writeJSON(w, http.StatusOK, map[string]any{
			"points":   points,
			"bucketMs": d.stats.bucketDuration.Milliseconds(),
		})
	}
}

// writeJSON encodes a value as JSON and writes it to the response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
