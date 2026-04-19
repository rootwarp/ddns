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

COVERAGE_OUT  := coverage.out
COVERAGE_HTML := coverage.html

.PHONY: build test coverage coverage-html coverage-check lint install integration-test clean

build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/ddns ./cmd/ddns

test:
	go test ./...

# Run the full suite with coverage, write a machine-readable profile, and print
# the per-function summary (last line is the project-wide total).
coverage:
	go test -covermode=atomic -coverprofile=$(COVERAGE_OUT) ./...
	@echo ""
	@echo "=== per-package coverage ==="
	@go tool cover -func=$(COVERAGE_OUT) | awk '/^total:/ {t=$$0; next} {print} END {print "---"; print t}'

# Produce a clickable HTML report from the profile (run `make coverage` first).
coverage-html: $(COVERAGE_OUT)
	go tool cover -html=$(COVERAGE_OUT) -o $(COVERAGE_HTML)
	@echo "wrote $(COVERAGE_HTML)"

# Fail when project-wide coverage drops below $(COVERAGE_MIN) (default 70 %).
# Override at the command line: `make coverage-check COVERAGE_MIN=85`.
COVERAGE_MIN ?= 70
coverage-check: coverage
	@total=$$(go tool cover -func=$(COVERAGE_OUT) | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	awk -v t=$$total -v m=$(COVERAGE_MIN) 'BEGIN { \
		if (t+0 < m+0) { printf "coverage %s%% below threshold %s%%\n", t, m; exit 1 } \
		else           { printf "coverage %s%% meets threshold %s%%\n", t, m } \
	}'

$(COVERAGE_OUT):
	@$(MAKE) coverage

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
		echo "golangci-lint not found; install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.4"; \
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
	rm -rf bin/ $(COVERAGE_OUT) $(COVERAGE_HTML)
