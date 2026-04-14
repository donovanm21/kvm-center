BINARY_SERVER = kvm-center-server
BINARY_AGENT  = kvm-center-agent
DIST_DIR      = dist

.PHONY: all build build-server build-agent clean docker-build docker-up docker-down \
        dist linux-amd64 linux-arm64

# ── Local builds ──────────────────────────────────────────────────────────────

all: build

build: build-server build-agent build-cli

build-server:
	go build -ldflags="-s -w" -o $(BINARY_SERVER) ./server

build-agent:
	go build -ldflags="-s -w" -o $(BINARY_AGENT) ./agent

build-cli:
	go build -ldflags="-s -w" -o kvmctl ./cmd/kvmctl

# ── Cross-compiled distribution binaries ──────────────────────────────────────

dist: linux-amd64 linux-arm64
	@echo "Artifacts in $(DIST_DIR)/"

linux-amd64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_SERVER)-linux-amd64 ./server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
	  go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_AGENT)-linux-amd64 ./agent

linux-arm64:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_SERVER)-linux-arm64 ./server
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	  go build -ldflags="-s -w" -o $(DIST_DIR)/$(BINARY_AGENT)-linux-arm64 ./agent

# ── Docker ────────────────────────────────────────────────────────────────────

docker-build:
	docker compose build

docker-up:
	docker compose up -d
	@echo ""
	@echo "  Web UI  → http://localhost:8096"
	@echo "  API     → http://localhost:8090"
	@echo ""

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

docker-build-server:
	docker build -f Dockerfile.build -t kvm-center-build:latest .
	docker run --rm -p 8080:8080 kvm-center-build:latest

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
