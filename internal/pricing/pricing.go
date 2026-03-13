// Copyright 2026 Merkle Works
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//


package pricing

import "net/http"

// Func determines the price in satoshis for an HTTP request.
type Func func(r *http.Request) (int64, error)

// Fixed returns a pricing function that always returns the same price.
func Fixed(satoshis int64) Func {
	return func(r *http.Request) (int64, error) {
		return satoshis, nil
	}
}

// PerByte returns a pricing function that charges based on response body size estimate.
func PerByte(satsPerByte float64, estimator func(r *http.Request) int64) Func {
	return func(r *http.Request) (int64, error) {
		size := estimator(r)
		return int64(float64(size) * satsPerByte), nil
	}
}
