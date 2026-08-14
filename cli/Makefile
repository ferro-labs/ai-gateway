.PHONY: build test lint fake itest smoke
build: ; go build -o ferro ./cmd/ferro
test:  ; go test -race ./...
lint:  ; golangci-lint run ./...
fake:  ; go run ./cmd/fakegw
itest: ; ./scripts/with-gateway.sh
smoke: build ; ./scripts/smoke.sh
