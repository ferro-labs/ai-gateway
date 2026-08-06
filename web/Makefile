SHELL := /bin/sh
.DEFAULT_GOAL := help

NPM ?= npm
HOST ?= 127.0.0.1
PORT ?= 5173
PREVIEW_PORT ?= 4173
GATEWAY_URL ?= http://127.0.0.1:8080

.PHONY: help deps dev ui build preview lint test test-watch check e2e clean

# There is no web image and no web container. The gateway embeds this bundle
# and serves it from its own port, so shipping it is `make build` at the
# repository root, not anything here.
help:
	@printf '%s\n' 'Ferro AI Gateway Web'
	@printf '%s\n' ''
	@printf '%s\n' '  make dev          Start the development UI'
	@printf '%s\n' '  make build        Build the production bundle into dist/'
	@printf '%s\n' '  make preview      Build and preview the production bundle'
	@printf '%s\n' '  make check        Run lint, unit tests, and production build'
	@printf '%s\n' '  make e2e          Run Playwright browser tests'
	@printf '%s\n' '  make clean        Remove generated frontend artifacts'
	@printf '%s\n' ''
	@printf '%s\n' 'To embed the bundle in the gateway binary, run `make build` at the'
	@printf '%s\n' 'repository root — it builds this bundle and copies it into place.'
	@printf '%s\n' ''
	@printf '%s\n' 'Options:'
	@printf '%s\n' '  GATEWAY_URL=http://127.0.0.1:8080'
	@printf '%s\n' '  HOST=127.0.0.1 PORT=5173 PREVIEW_PORT=4173'

node_modules/.package-lock.json: package.json package-lock.json
	$(NPM) ci

deps: node_modules/.package-lock.json

dev: deps
	@printf 'Web UI: http://%s:%s\n' '$(HOST)' '$(PORT)'
	@printf 'Gateway: %s\n' '$(GATEWAY_URL)'
	GATEWAY_DEV_URL='$(GATEWAY_URL)' $(NPM) run dev -- --host '$(HOST)' --port '$(PORT)'

ui: dev

build: deps
	$(NPM) run build

preview: build
	@printf 'Web preview: http://%s:%s\n' '$(HOST)' '$(PREVIEW_PORT)'
	@printf 'Gateway: %s\n' '$(GATEWAY_URL)'
	GATEWAY_DEV_URL='$(GATEWAY_URL)' $(NPM) run preview -- --host '$(HOST)' --port '$(PREVIEW_PORT)'

lint: deps
	$(NPM) run lint

test: deps
	$(NPM) run test

test-watch: deps
	$(NPM) run test:watch

check: deps
	$(NPM) run check

e2e: deps
	$(NPM) run test:e2e

clean:
	rm -rf dist coverage playwright-report test-results
