SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help verify deps fmt lint test build clean compose-config open-pilot-check api-run api-build api-test api-smoke web-dev web-build web-test web-format switchyard-smoke docker-up docker-down

API_ADDR ?= :8080
SMOKE_ADDR ?= :18080
SMOKE_URL ?= http://127.0.0.1:18080

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Install web dependencies
	pnpm --dir web install --frozen-lockfile

verify: deps fmt lint test build compose-config open-pilot-check ## Run all checks

fmt: ## Format/check Go and web sources
	@git ls-files --cached --others --exclude-standard 'backend/**/*.go' | xargs -r gofmt -w
	pnpm --dir web run format

lint: ## Run Go vet and web lint
	cd backend && go vet ./...
	pnpm --dir web run lint

test: api-test web-test ## Run backend and web tests

api-test: ## Run Switchyard backend tests
	cd backend && go test ./...

web-test: ## Run Switchyard web tests
	pnpm --dir web run test

build: api-build web-build ## Build backend and web

api-build: ## Build Switchyard API binary
	cd backend && go build ./cmd/app

web-build: ## Build Switchyard web UI
	pnpm --dir web run build

web-format: ## Type-check Switchyard web UI
	pnpm --dir web run format

api-run: ## Run Switchyard API locally on API_ADDR=:8080
	cd backend && SWITCHYARD_LISTEN_ADDR=$(API_ADDR) go run ./cmd/app

web-dev: ## Run Switchyard web dev server
	pnpm --dir web run dev

api-smoke: api-build ## Start API temporarily and check core control-plane endpoints
	@set -euo pipefail; \
	cd backend; \
	SWITCHYARD_LISTEN_ADDR=$(SMOKE_ADDR) go run ./cmd/app >/tmp/switchyard-smoke.log 2>&1 & \
	pid=$$!; \
	trap 'kill $$pid >/dev/null 2>&1 || true' EXIT; \
	for _ in 1 2 3 4 5 6 7 8 9 10; do \
		if curl -fsS $(SMOKE_URL)/healthz >/dev/null 2>&1; then break; fi; \
		sleep 0.2; \
	done; \
	curl -fsS $(SMOKE_URL)/api/workflows >/dev/null; \
	curl -fsS $(SMOKE_URL)/api/triggers >/dev/null; \
	curl -fsS -X POST $(SMOKE_URL)/api/triggers/manual-open-pilot/run >/dev/null; \
	curl -fsS $(SMOKE_URL)/api/runs >/dev/null; \
	echo "Switchyard API smoke passed at $(SMOKE_URL)"

switchyard-smoke: api-smoke ## Run Switchyard control-plane smoke checks

docker-up: ## Start local Switchyard stack
	docker compose up --build

docker-down: ## Stop local Switchyard stack
	docker compose down

clean: ## Remove generated build artifacts
	rm -rf web/dist backend/app

compose-config: ## Validate Docker Compose config
	docker compose config --quiet

open-pilot-check: ## Verify Open Pilot templates are present
	@test -f .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
