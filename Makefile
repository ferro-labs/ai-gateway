# AI Gateway Makefile

.PHONY: help build build-cli run run-example test test-coverage test-integration bench fmt lint clean deps \
        snapshot release-check release-dry-run

# Version stamping via ldflags (used in dev builds; GoReleaser overrides on release).
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X github.com/ferro-labs/ai-gateway/internal/version.Version=$(VERSION) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Commit=$(COMMIT) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Date=$(DATE)

# Default target
help:
	@echo "Ferro AI Gateway — Development Commands"
	@echo ""
	@echo "Build:"
	@echo "  make build             Build ferrogw binary (./bin/ferrogw)"
	@echo "  make build-cli         Build ferrogw-cli binary (./bin/ferrogw-cli)"
	@echo ""
	@echo "Run:"
	@echo "  make run               Run ferrogw server (requires OPENAI_API_KEY)"
	@echo "  make run-example       Run basic provider example"
	@echo ""
	@echo "Test:"
	@echo "  make test              Run all unit tests"
	@echo "  make test-coverage     Run tests with coverage report"
	@echo "  make test-integration  Run integration tests (requires API keys)"
	@echo "  make bench             Run benchmarks"
	@echo ""
	@echo "Quality:"
	@echo "  make fmt               Format code"
	@echo "  make lint              Lint code (requires golangci-lint)"
	@echo "  make precommit         fmt + test"
	@echo ""
	@echo "Release:"
	@echo "  make snapshot          Build a local snapshot (no publish)"
	@echo "  make release-check     Validate .goreleaser.yaml config"
	@echo "  make release-dry-run   Full release pipeline without publishing"
	@echo ""
	@echo "  Current version:       $(VERSION)"
	@echo ""

# Build ferrogw binary
build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/ferrogw ./cmd/ferrogw

# Build ferrogw-cli binary
build-cli:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/ferrogw-cli ./cmd/ferrogw-cli

# Run ferrogw server
run: build
	@if [ -z "$$OPENAI_API_KEY" ]; then echo "OPENAI_API_KEY required"; exit 1; fi
	./bin/ferrogw

# Run basic example
run-example:
	@if [ -z "$$OPENAI_API_KEY" ]; then echo "OPENAI_API_KEY required"; exit 1; fi
	go run ./examples/basic

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod verify

# Run unit tests
test:
	@echo "Running unit tests..."
	go test -v -short -race -timeout 30s ./...

# Run tests with coverage
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -short -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
	@go tool cover -func=coverage.out | grep total | awk '{print "Total coverage: " $$3}'

# Run integration tests (requires API keys)
test-integration:
	@echo "Running integration tests..."
	@echo "Note: This requires OPENAI_API_KEY to be set"
	@if [ -z "$$OPENAI_API_KEY" ]; then \
		echo "Error: OPENAI_API_KEY environment variable not set"; \
		exit 1; \
	fi
	go test -v -race -timeout 60s ./... -run Integration

# Run benchmarks
bench:
	@echo "Running benchmarks..."
	go test -v -bench=. -benchmem ./...

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	@which gofmt > /dev/null && gofmt -s -w . || echo "gofmt not available"

# Lint code (requires golangci-lint)
lint:
	@echo "Linting code..."
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed. Install from https://golangci-lint.run/usage/install/" && exit 1)
	golangci-lint run ./...

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin coverage.out coverage.html
	go clean -testcache
	go clean -cache

# Run a quick check before committing
precommit: fmt test
	@echo "✅ Pre-commit checks passed!"

# ─── Release targets (require goreleaser: https://goreleaser.com/install) ────

# Build a local snapshot tarball and Docker images without publishing.
# Useful for testing the release pipeline locally.
snapshot:
	@which goreleaser > /dev/null || (echo "goreleaser not installed. See https://goreleaser.com/install/" && exit 1)
	goreleaser release --snapshot --clean

# Validate the .goreleaser.yaml config without building anything.
release-check:
	@which goreleaser > /dev/null || (echo "goreleaser not installed. See https://goreleaser.com/install/" && exit 1)
	goreleaser check

# Full release pipeline (build + changelog) without uploading to GitHub or Docker.
# Requires a git tag to be present, e.g.: git tag v0.1.0
release-dry-run:
	@which goreleaser > /dev/null || (echo "goreleaser not installed. See https://goreleaser.com/install/" && exit 1)
	goreleaser release --skip=publish --clean

# Run everything
all: deps fmt lint test test-coverage
	@echo "✅ All checks passed!"
