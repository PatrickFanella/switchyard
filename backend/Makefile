SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help verify fmt lint test compose-config open-pilot-check

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: fmt lint test compose-config open-pilot-check ## Run all checks

fmt: ## Format Go files
	gofmt -w $$(git ls-files '*.go')

lint: ## Run Go vet
	go vet ./...

test: ## Run Go tests
	go test ./...

compose-config: ## Validate Docker Compose if present
	@if [ -f docker-compose.yml ]; then docker compose config --quiet; else echo 'skip compose config'; fi

open-pilot-check: ## Verify Open Pilot issue template exists
	@test -f .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
