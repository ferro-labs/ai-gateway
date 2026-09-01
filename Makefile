VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
            -X github.com/ferro-labs/ai-gateway/internal/version.Version=$(VERSION) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Commit=$(COMMIT) \
            -X github.com/ferro-labs/ai-gateway/internal/version.Date=$(DATE)

# Keep Go tooling inside the gateway module even when web/node_modules contains
# third-party Go samples.
#
# The list is spelled out rather than `./...` for that reason alone, so it has to
# name every package the module actually has. config/ and pkg/ were missing, which
# meant `make test` reported success while never running 15 test files — including
# the config strict-decode contract. CI runs `./...` and did cover them, so this
# was a local blind spot rather than an unguarded one, which is exactly what makes
# it easy to leave: the gate that caught a regression was never the one a developer
# ran first.
GATEWAY_PACKAGES = $(shell go list . ./cmd/... ./config/... ./internal/... ./mcp/... ./models/... ./observability/... ./pkg/... ./plugin/... ./providers/... ./test/...)

# Where internal/webui embeds the dashboard from. go:embed cannot reference a
# path outside its own package directory, so the bundle web/ produces has to be
# copied in here before the Go build rather than read from web/dist.
EMBED_DIR := internal/webui/dist

.PHONY: fmt-check build run test test-coverage test-integration test-integration-postgres test-integration-containers test-integration-live test-integration-all bench fmt vet lint lint-fix clean deps web-deps web-build web-embed web-test web-check web-e2e web-clean precommit all snapshot release-check release-dry-run docker-build up up-prod down up-fullstack up-fullstack-live down-fullstack

build: web-embed
	@mkdir -p bin
	go build -ldflags="$(LDFLAGS)" -o bin/ferrogw ./cmd/ferrogw

run: build
	./bin/ferrogw

deps:
	go mod download && go mod verify

# Web application targets. `make build` is the one gateway target that needs
# them; plain `go build ./cmd/ferrogw` and `go test` still work on a fresh
# clone with no Node installed — internal/webui serves a compiled-in
# placeholder when no bundle was embedded.
web/node_modules/.package-lock.json: web/package.json web/package-lock.json
	npm ci --prefix web

web-deps: web/node_modules/.package-lock.json

web-build: web-deps
	npm run build --prefix web

# Replaces the bundle rather than merging into it, so a renamed hashed asset
# from an earlier build does not linger in the binary. .gitkeep survives the
# sweep because it is the one tracked file here and go:embed needs the
# directory to exist.
web-embed: web-build
	@mkdir -p $(EMBED_DIR)
	find $(EMBED_DIR) -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. $(EMBED_DIR)/

web-test: web-deps
	npm test --prefix web

web-check: web-deps
	npm run check --prefix web

web-e2e: web-deps
	npm run test:e2e --prefix web

web-clean:
	rm -rf web/dist web/coverage

# Per-package binary timeout, kept at or above the one ci.yml uses. 180s was the
# value CI had already diagnosed as too tight — the gateway package's -race suite
# alone runs ~150s, so a loaded developer machine got a timeout with no named
# failing test, which reads as a regression and costs an investigation.
# cmd/ferrogw/build_gates_test.go fails if this drifts under CI's value again.
test:
	go test -v -short -race -timeout 300s $(GATEWAY_PACKAGES)

test-coverage:
	go test -v -short -race -coverprofile=coverage.out -covermode=atomic $(GATEWAY_PACKAGES)
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | grep total | awk '{print "Total coverage: " $$3}'

test-integration:
	go test -v -tags=integration -race -timeout 180s ./test/integration/...

test-integration-postgres:
	go test -v -tags=integration -race -timeout 120s ./test/integration/...

# Backward-compatible alias for test-integration-postgres (deprecated — remove in v1.1.0).
test-integration-containers: test-integration-postgres

test-integration-live:
	go test -v -tags=live -race -timeout 300s ./test/live/...

test-integration-all: test-integration test-integration-live

# Every routing strategy over the real binary against scriptable mock upstreams
# (scripts/mockllm) — no provider keys. E2E_SLOW=1 adds the hung-target cell.
test-e2e-strategies:
	scripts/strategy_e2e.sh

bench:
	go test -v -bench=. -benchmem $(GATEWAY_PACKAGES)

GOFMT_FILES = find . -type f -name '*.go' -not -path './web/*' -not -path './vendor/*' -print0

fmt:
	$(GOFMT_FILES) | xargs -0 gofmt -s -w

# The read-only half of fmt, for CI. Formatting had drifted in two files before
# this existed, and neither of the checks that run would ever have said so:
# golangci-lint enables no formatter, and it analyses no build-tagged file
# unless build-tags are configured — which is precisely where the drift was.
# Sharing GOFMT_FILES with fmt is the point; a check over a narrower set than
# the fixer is a check that passes on files the fixer would rewrite.
fmt-check:
	@files=$$($(GOFMT_FILES) | xargs -0 gofmt -s -l); \
	test -z "$$files" || { \
		echo 'gofmt -s would rewrite these files; run `make fmt`:'; \
		echo "$$files"; \
		exit 1; \
	}

vet:
	go vet $(GATEWAY_PACKAGES)

# golangci-lint truncates its report at 3 issues per message by default. That is
# not a display detail here: a measurement of the tagged tree below reported 13
# issues where there were 17, hiding two noctx and two errcheck findings, and the
# count taken from that report was then planned against. The gate reports all of it.
GOLANGCI_FLAGS = --max-issues-per-linter=0 --max-same-issues=0

# golangci-lint analyses no build-tagged file unless the tag is given, so every
# //go:build integration and //go:build live source was invisible to the run
# above and accumulated lint debt no gate reported.
#
# It is a second pass over named packages rather than one tagged run over ./...
# because test/testutil is itself integration-tagged and imports the root
# package: under the tag, a repo-wide analysis is an import cycle, not a
# superset. The scope is therefore hand-written, and the guard in the lint
# target fails if a tagged file ever appears outside it — the one thing a
# hand-written scope cannot notice on its own.
#
# CI does not run this target; it calls golangci-lint-action directly. The
# tagged pass belongs in .github/workflows/ci.yml as well as here.
TAGGED_LINT_PACKAGES = ./mcp/... ./test/...
TAGGED_LINT_SCOPE_RE = ^(\./)?(mcp|test)/

lint:
	golangci-lint run $(GOLANGCI_FLAGS) ./...
	@stray=$$(grep -rlE 'go:build (integration|live)' --include='*.go' . | grep -Ev '$(TAGGED_LINT_SCOPE_RE)'); \
	test -z "$$stray" || { \
		echo 'build-tagged files outside TAGGED_LINT_PACKAGES; widen it:'; \
		echo "$$stray"; \
		exit 1; \
	}
	golangci-lint run $(GOLANGCI_FLAGS) --build-tags=integration,live $(TAGGED_LINT_PACKAGES)

lint-fix:
	golangci-lint run --fix $(GOLANGCI_FLAGS) ./...
	golangci-lint run --fix $(GOLANGCI_FLAGS) --build-tags=integration,live $(TAGGED_LINT_PACKAGES)

# Docker. The compose files live in deploy/ and are always used as a base plus
# one override, so these targets exist to keep the -f pair out of the docs and
# out of anyone's shell history. Run them from the repository root: the build
# context is "." and Docker reads .dockerignore from there.
COMPOSE_DEV  := docker compose -f deploy/compose.yaml -f deploy/compose.dev.yaml
COMPOSE_PROD := docker compose -f deploy/compose.yaml -f deploy/compose.prod.yaml
# Standalone stack (its own project name), so it neither adopts nor collides
# with the dev/prod stack above. The base file is production-shaped (real
# providers); the mock overlay turns it into the self-contained demo.
COMPOSE_FULL      := docker compose -f deploy/compose.fullstack.yaml
COMPOSE_FULL_MOCK := docker compose -f deploy/compose.fullstack.yaml -f deploy/compose.fullstack.mock.yaml

# One Dockerfile, one context, one target. Built from the repository root so
# Docker picks up the .dockerignore here. The dashboard bundle is produced by a
# stage inside that build, so this needs no Node on the host and no web-embed.
# LDFLAGS is passed through so the image's binary stamps the same version,
# commit and date `make build` would give it. Without it `ferrogw version`
# inside the image reports dev/none/unknown and cannot be traced to a commit.
docker-build:
	docker build -f deploy/Dockerfile --target gateway \
		--build-arg LDFLAGS="$(LDFLAGS)" \
		-t ferrogw:$(VERSION) .

up:
	$(COMPOSE_DEV) up

up-prod:
	$(COMPOSE_PROD) up -d

# Either override tears down the same stack — compose.yaml pins the project
# name, so dev and prod are the same project rather than two by directory.
down:
	$(COMPOSE_DEV) down

# Fullstack observability stack: gateway + Postgres + Jaeger + Prometheus +
# Grafana (localhost:3000). `up-fullstack` adds the mock upstream + load
# generator for a self-contained demo; `up-fullstack-live` runs the same stack
# against real providers using keys from a repository-root .env.
up-fullstack:
	$(COMPOSE_FULL_MOCK) up -d --build

up-fullstack-live:
	$(COMPOSE_FULL) up -d --build

down-fullstack:
	$(COMPOSE_FULL_MOCK) down -v

# Sweeping the bundle leaves .gitkeep, which is the whole of what is tracked
# here — so a clean tree goes back to compiling against the built-in
# placeholder with no git operation and no partial bundle left behind. Sparing
# index.html instead would keep a file naming content-hashed assets this target
# just deleted, and Available() would then report a dashboard whose every asset
# 404s.
clean:
	rm -rf bin coverage.out coverage.html
	@mkdir -p $(EMBED_DIR)
	find $(EMBED_DIR) -mindepth 1 ! -name .gitkeep -delete
	go clean -testcache -cache

precommit: fmt test

all: deps fmt vet lint test-coverage build

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check

release-dry-run:
	goreleaser release --skip=publish --clean
