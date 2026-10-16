GO      ?= go
SQLC    ?= $(shell command -v sqlc 2>/dev/null || echo $(HOME)/go/bin/sqlc)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
PKG     := github.com/ncecere/open-mcp-gateway
LDFLAGS := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) -X $(PKG)/internal/buildinfo.Commit=$(COMMIT)

# Integration tests create one database per test on this server (a role
# allowed to create databases); they skip when it is unreachable.
export OMG_TEST_DATABASE_URL ?= postgres://omg:omg-dev-only@127.0.0.1:55442/postgres?sslmode=disable

.PHONY: help deps-up deps-down generate check-generated build run migrate fake-mcp test test-integration lint fmt web web-test web-dev docker

help: ## Show targets
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

deps-up: ## Start local Postgres (127.0.0.1:55442); it restarts with Docker
	docker compose up -d --wait

deps-down: ## Stop local Postgres (keeps data)
	docker compose down

generate: ## Regenerate sqlc queries and OpenAPI types (Go, and TypeScript when web/ exists)
	$(SQLC) generate
	$(GO) generate ./...
	@if [ -f web/package.json ]; then cd web && npm run gen; else echo "web/package.json not found; skipping TypeScript generation"; fi

check-generated: generate ## Fail if generated code is stale
	@git diff --exit-code -- internal/store/dbgen internal/httpapi/apitypes web/src/api/schema.gen.ts || \
		(echo "generated code is stale: run 'make generate' and commit" && exit 1)
	@test -z "$$(git ls-files --others --exclude-standard -- internal/store/dbgen internal/httpapi/apitypes web/src/api)" || \
		(echo "untracked generated files: run 'make generate' and commit" && exit 1)

build: ## Build bin/open-mcp-gateway (run `make web` first to include the UI)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/open-mcp-gateway ./cmd/open-mcp-gateway

run: ## Run `serve` locally on 127.0.0.1:8088 using .env (created from .env.example)
	@test -f .env || cp .env.example .env
	set -a && . ./.env && set +a && $(GO) run ./cmd/open-mcp-gateway serve

migrate: ## Apply migrations using .env
	@test -f .env || cp .env.example .env
	set -a && . ./.env && set +a && $(GO) run ./cmd/open-mcp-gateway migrate

fake-mcp: ## Run a fake MCP server on http://127.0.0.1:8091/mcp (FAKEMCP_DESCRIBE='tool=text' changes a description)
	$(GO) run ./cmd/fakemcp

test: ## All Go tests; integration tests skip when Postgres is unreachable
	$(GO) test -count=1 ./...

test-integration: deps-up ## All Go tests with Postgres required (fails instead of skipping)
	OMG_TEST_REQUIRE_DB=1 $(GO) test -race -count=1 ./...

lint: ## go vet and gofmt check
	$(GO) vet ./...
	@test -z "$$(gofmt -l cmd internal migrations web/embed.go)" || (gofmt -l cmd internal migrations web/embed.go && echo "run 'make fmt'" && exit 1)

fmt: ## Format Go code
	gofmt -w cmd internal migrations web/embed.go

web: ## Build the web UI into web/dist (embedded by `make build`)
	cd web && npm ci --ignore-scripts && npm run build

web-test: ## Type-check and test the web UI
	cd web && npm run typecheck && npm test

web-dev: ## Run the Vite dev server on :5175 (proxies the API to `make run` on :8088)
	cd web && npm run dev

docker: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t open-mcp-gateway:$(VERSION) .
