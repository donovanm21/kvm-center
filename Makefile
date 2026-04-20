BINARY_SERVER = kvm-center-server
BINARY_AGENT  = kvm-center-agent
DIST_DIR      = dist

VERSION := $(shell cat server/VERSION | tr -d '[:space:]')
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: all build build-server build-agent clean docker-build docker-up docker-down \
        dist linux-amd64 linux-arm64

# ── Local builds ──────────────────────────────────────────────────────────────

all: build

build: build-server build-agent build-cli

build-server:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_SERVER) ./server

build-agent:
	go build -ldflags="$(LDFLAGS)" -o $(BINARY_AGENT) ./agent

build-cli:
	go build -ldflags="$(LDFLAGS)" -o kvmctl ./cmd/kvmctl

# ── Cross-compiled distribution binaries ──────────────────────────────────────

dist: linux-amd64 linux-arm64
	@echo "Artifacts in $(DIST_DIR)/"

linux-amd64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_SERVER)-linux-amd64 ./server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_AGENT)-linux-amd64 ./agent

linux-arm64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_SERVER)-linux-arm64 ./server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -ldflags="$(LDFLAGS)" -o $(DIST_DIR)/$(BINARY_AGENT)-linux-arm64 ./agent

# ── Docker ────────────────────────────────────────────────────────────────────

docker-build:
	docker compose build

docker-up:
	docker compose up -d
	@echo ""
	@echo "  Web UI  → http://localhost:8090"
	@echo "  API     → http://localhost:8090/api"
	@echo ""

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

# ── Utilities ─────────────────────────────────────────────────────────────────

clean:
	rm -f $(BINARY_SERVER) $(BINARY_AGENT)
	rm -rf $(DIST_DIR)

run-server:
	KVM_CENTER_DATA=./data.json go run ./server -addr :8090

run-agent:
	go run ./agent -server http://localhost:8090

fmt:
	gofmt -w .

vet:
	go vet ./...

# ── Quality gates (v5.12) ─────────────────────────────────────────────────────
# `make test`  — go test across every package.
# `make lint`  — gofmt check (no writes) + go vet. golangci-lint runs when
#                it's on PATH; skipped otherwise so contributors without the
#                extra tool get useful output anyway.
.PHONY: test lint lint-fix check

test:
	go test ./...

lint:
	@echo "→ gofmt check"
	@changed=$$(gofmt -l .); if [ -n "$$changed" ]; then \
		echo "  files need gofmt:"; echo "$$changed" | sed 's/^/    /'; \
		exit 1; \
	fi
	@echo "→ go vet"
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		echo "→ golangci-lint"; \
		golangci-lint run ./...; \
	else \
		echo "→ golangci-lint not installed — skipping (brew install golangci-lint to enable)"; \
	fi

lint-fix:
	gofmt -w .
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run --fix ./... || true

check: lint test
