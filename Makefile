# Motivra monorepo — developer entry points.
#
# Targets:
#   make build               Build all Go packages in the monorepo.
#   make lint                Run golangci-lint over all Go packages.
#   make test                Run all Go tests with the race detector.
#   make test-cover          Run tests and print the per-function coverage summary.
#   make validate-migrations Validate per-domain migration chains (CI gate).
#   make dev                 Start local dev dependencies (postgres, redis, nats, minio).
#   make dev-temporal        Start dev dependencies plus the Temporal server and UI.
#   make dev-down            Stop the local dev stack.
#   make tidy                Sync go.mod and go.sum after changing imports.
#   make help                Show available targets.

.DEFAULT_GOAL := help

.PHONY: build lint test test-cover validate-migrations dev dev-temporal dev-down tidy help

# -tags tools loads the root package (tools.go) so the pinned shared
# dependencies are proven to compile; harmless once real code exists.
build: ## Build all Go packages in the monorepo.
	go build -tags tools ./...

lint: ## Run golangci-lint over all Go packages.
	golangci-lint run

test: ## Run all Go tests with the race detector.
	go test -race -tags tools ./...

test-cover: ## Run tests and print the per-function coverage summary.
	go test -race -tags tools -coverprofile=coverage.out -covermode=atomic ./...
	@if [ -f coverage.out ]; then go tool cover -func=coverage.out | tail -1; else echo "no coverage profile yet (no Go test packages in the skeleton)"; fi

validate-migrations: ## Validate per-domain migration chains (same gate as CI).
	bash scripts/validate_migrations.sh

dev: ## Start local dev dependencies (postgres, redis, nats, minio).
	docker compose -f docker-compose.dev.yml up -d

dev-temporal: ## Start dev dependencies plus the Temporal server and UI.
	docker compose -f docker-compose.dev.yml --profile temporal up -d

dev-down: ## Stop the local dev stack.
	docker compose -f docker-compose.dev.yml --profile temporal down

tidy: ## Sync go.mod and go.sum after changing imports.
	go mod tidy

help: ## Show available targets.
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-20s %s\n", $$1, $$2}'

# --- Deployment readiness (issues #25 + #31): Docker stack + demo seed ---

.PHONY: docker-build stack-up stack-down seed

docker-build: ## Build one service image (SVC=identity|vehicles|jobs|dispatch) via ./Dockerfile.
	docker build --build-arg SERVICE=$(SVC) -t motivra-$(SVC):dev .

stack-up: ## Start the full application stack (PostGIS/redis/nats + identity/vehicles/jobs/dispatch).
	docker compose -f docker-compose.app.yml up -d

stack-down: ## Stop the full application stack (data volumes persist).
	docker compose -f docker-compose.app.yml down

seed: ## Seed demo data with scripts/seed_demo.sh (arg: DB URL; default = app stack on :5433).
	bash scripts/seed_demo.sh $(SEED_URL)
