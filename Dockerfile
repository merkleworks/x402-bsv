# Copyright 2026 Merkle Works
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0

# =============================================================================
# x402 Gateway — Multi-stage Docker Build
# =============================================================================
#   docker compose up -d --build
# =============================================================================

# Stage 1: Build React dashboard
FROM node:20-alpine AS dashboard

WORKDIR /dashboard
COPY dashboard/package*.json ./
RUN npm ci
COPY dashboard/ .
RUN npm run build
# Vite outputs to: ../cmd/server/static/ (relative to /dashboard)
# Which resolves to: /cmd/server/static/

# Stage 2: Build Go binaries
FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Copy the React build output into the Go embed directory
COPY --from=dashboard /cmd/server/static/ ./cmd/server/static/

# Build all binaries
RUN CGO_ENABLED=0 go build -o /bin/x402-server ./cmd/server
RUN CGO_ENABLED=0 go build -o /bin/x402-client ./cmd/client
RUN CGO_ENABLED=0 go build -o /bin/x402-setup ./cmd/setup

# --- Runtime image ---
FROM alpine:3.19

RUN apk add --no-cache ca-certificates

COPY --from=builder /bin/x402-server /bin/x402-client /bin/x402-setup /bin/

EXPOSE 8402

ENTRYPOINT ["/bin/x402-server"]
