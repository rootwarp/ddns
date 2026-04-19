# ddns — build tooling
#
# This Makefile is the single entry point developers and CI both use for the
# standard tasks. All commands are indented with tabs (Make requirement).

# --- Version metadata (injected into internal/version via -ldflags) ---------
VERSION    := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -X github.com/rootwarp/ddns/internal/version.Version=$(VERSION) \
           -X github.com/rootwarp/ddns/internal/version.Commit=$(COMMIT) \
           -X github.com/rootwarp/ddns/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: build test lint install integration-test clean

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/ddns ./cmd/ddns

test:
	go test ./...

lint:
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not found; install with: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	fi
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not found; install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/ddns

integration-test:
	@if [ -z "$$DDNS_INTEGRATION_PROJECT" ]; then \
		echo "DDNS_INTEGRATION_PROJECT unset; skipping integration tests"; \
		exit 0; \
	fi; \
	go test -tags=integration ./...

clean:
	rm -rf bin/
