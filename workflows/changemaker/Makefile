SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help deps verify fmt lint test build compose-config open-pilot-check

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Install Node dependencies
	pnpm install --frozen-lockfile

verify: deps fmt lint test build compose-config open-pilot-check ## Run all checks

fmt: ## Run formatter if configured
	pnpm run format

lint: ## Lint TypeScript
	pnpm run lint

test: ## Run tests
	pnpm run test

build: ## Build project
	pnpm run build

compose-config: ## Validate Docker Compose if present
	@if [ -f docker-compose.yml ]; then docker compose config --quiet; else echo 'skip compose config'; fi

open-pilot-check: ## Verify Open Pilot issue template exists
	@test -f .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
