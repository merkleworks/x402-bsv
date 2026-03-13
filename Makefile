.PHONY: build test lint run clean demo client setup deploy dashboard-dev dashboard-build

# ─── Build ──────────────────────────────────────────────────
build: dashboard-build
	go build -o bin/x402-server ./cmd/server
	go build -o bin/x402-client ./cmd/client
	go build -o bin/x402-setup ./cmd/setup

test:
	go test ./... -v -count=1

lint:
	go vet ./...

run:
	go run ./cmd/server

clean:
	rm -rf bin/
	rm -rf cmd/server/static/

# ─── Dashboard ──────────────────────────────────────────────
dashboard-dev:
	cd dashboard && npm run dev

dashboard-build:
	cd dashboard && npm ci && npm run build

# ─── Setup ──────────────────────────────────────────────────
setup:
	go run ./cmd/setup

deploy: setup
	docker compose up -d --build

# ─── Demo mode ───────────────────────────────────────────────
# One command to go from zero to running:
#   make demo
#
# Generates a BSV key if needed, builds binaries, starts the
# server with auto-seeded pools, and opens the dashboard.

demo: build
	@if [ ! -f .env ]; then \
		echo "No .env found — generating demo key..."; \
		go run ./cmd/keygen; \
		echo ""; \
	fi
	@echo "  x402 Demo Mode"
	@echo "  ──────────────"
	@echo "  Dashboard:  http://localhost:8402/"
	@echo "  Client:     make client  (in another terminal)"
	@echo ""
	@set -a && . ./.env && set +a && ./bin/x402-server

# Run the CLI client against the local server
client:
	@go run ./cmd/client http://localhost:8402/v1/expensive
