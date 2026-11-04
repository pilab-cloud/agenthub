SHELL := /bin/bash
.DEFAULT_GOAL := help

BUF_VERSION ?= v1.55.1
GOLANGCI_LINT_VERSION ?= v2.12.2
GOOSE_VERSION ?= v3.29.0
GOFUMPT_VERSION ?= v0.9.1

BUF ?= go run github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
GOOSE ?= go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
GOFUMPT ?= go run mvdan.cc/gofumpt@$(GOFUMPT_VERSION)

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -ldflags "-s -w -X go.pilab.hu/agenthub/internal/version.Version=$(VERSION) -X go.pilab.hu/agenthub/internal/version.Commit=$(COMMIT) -X go.pilab.hu/agenthub/internal/version.Date=$(DATE)"

.PHONY: help
help: ## Show help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: check
check: fmt-check lint vet buf-lint buf-breaking ## Full static check
	@echo "check: ok"

.PHONY: fmt
fmt: ## Format Go code
	$(GOLANGCI_LINT) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if formatting changes are needed
	@out="$$($(GOLANGCI_LINT) fmt --diff ./... 2>/dev/null)"; \
	if [[ -n "$$out" ]]; then echo "$$out"; echo "run 'make fmt'"; exit 1; fi

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: buf-lint
buf-lint: ## Lint protobuf
	$(BUF) lint

BUF_BASE ?= main
.PHONY: buf-breaking
buf-breaking: ## Breaking-change check against BUF_BASE
	@if git rev-parse --verify -q $(BUF_BASE):buf.yaml >/dev/null; then \
	  $(BUF) breaking --against '.git#ref=$(BUF_BASE)'; \
	else echo "buf breaking: no baseline on $(BUF_BASE) yet, skipping"; fi

.PHONY: generate
generate: ## Regenerate protobuf code
	$(BUF) generate

.PHONY: generate-check
generate-check: generate ## Verify generated code is up to date
	@git diff --exit-code -- gen/ || (echo "generated code changed; run 'make generate'"; exit 1)

.PHONY: test
test: ## Run unit tests
	go test -race -count=1 ./...

.PHONY: build
build: ## Build binaries
	@mkdir -p bin
	go build -trimpath $(LDFLAGS) -o bin/agenthub-server ./cmd/agenthub-server
	go build -trimpath $(LDFLAGS) -o bin/agenthub ./cmd/agenthub

.PHONY: run
run: ## Run server in dev mode
	go run ./cmd/agenthub-server serve --config deploy/dev/config.yaml

.PHONY: migrate
migrate: ## Run migrations (up)
	go run ./cmd/agenthub-server migrate --config deploy/dev/config.yaml

.PHONY: web
web: ## Build Tailwind CSS
	bun run --cwd internal/web build:css

.PHONY: web-check
web-check: web ## Verify committed CSS is up to date
	@git diff --exit-code -- internal/web/static/app.css || (echo "CSS changed; run 'make web'"; exit 1)

.PHONY: smoke
smoke: build ## Start server and probe /healthz
	@./bin/agenthub-server serve --config deploy/dev/config.yaml & \
	  PID=$$!; \
	  trap "kill $$PID 2>/dev/null" EXIT; \
	  for i in $$(seq 1 30); do \
	    curl -fsS http://127.0.0.1:8080/healthz >/dev/null && { echo "healthz: ok"; exit 0; }; \
	    sleep 0.5; \
	  done; \
	  echo "server did not become healthy"; exit 1

.PHONY: docker
docker: ## Build container image
	docker build -t agenthub-server:dev -f deploy/docker/Dockerfile .

.PHONY: clean
clean: ## Clean build artifacts
	rm -rf bin/ internal/web/static/app.css
