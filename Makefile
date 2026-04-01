VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X github.com/ferro-labs/ai-gateway/internal/version.Version=$(VERSION) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Commit=$(COMMIT) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Date=$(DATE)

.PHONY: build run test test-coverage test-integration bench fmt vet lint clean deps precommit all snapshot release-check release-dry-run

build:
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/ferrogw ./cmd/ferrogw

run: build
	./bin/ferrogw

deps:
	go mod download && go mod verify

test:
	go test -v -short -race -timeout 30s ./...

test-coverage:
	go test -v -short -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | grep total | awk '{print "Total coverage: " $$3}'

test-integration:
	go test -v -race -timeout 60s ./... -run Integration

bench:
	go test -v -bench=. -benchmem ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin coverage.out coverage.html
	go clean -testcache -cache

precommit: fmt test

all: deps fmt vet lint test-coverage build

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check

release-dry-run:
	goreleaser release --skip=publish --clean
