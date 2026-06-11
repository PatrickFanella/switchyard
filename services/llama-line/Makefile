SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help verify fmt lint test compose-config open-pilot-check build install run restart stop web-install web-dev web-build web-clean

PID_FILE ?= tmp/llama-line.pid
RUN_CONFIG ?= config.yaml
RUN_BIN ?= /usr/local/bin/llama-line

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

verify: fmt lint test compose-config open-pilot-check ## Run all checks

fmt: ## Format Go files
	@git ls-files --cached --others --exclude-standard '*.go' | xargs -r gofmt -w

lint: ## Run Go vet
	go vet ./...

test: ## Run Go tests
	go test ./...

build: web-build ## Build binary to bin/llama-line (includes embedded SPA)
	@mkdir -p bin
	go build -o bin/llama-line ./cmd/app

install: ## Install binary to /usr/local/bin
	sudo cp bin/llama-line /usr/local/bin/llama-line

run: ## Run with example config
	go run ./cmd/app --config config.example.yaml

stop: ## Stop local broker recorded in PID_FILE
	@stopped=false; \
	stop_pid() { \
		pid="$$1"; \
		if kill -0 "$$pid" 2>/dev/null; then \
			echo "stopping llama-line pid $$pid"; \
			kill "$$pid"; \
			for _ in 1 2 3 4 5 6 7 8 9 10; do \
				if ! kill -0 "$$pid" 2>/dev/null; then break; fi; \
				sleep 0.5; \
			done; \
			if kill -0 "$$pid" 2>/dev/null; then \
				echo "force stopping llama-line pid $$pid"; \
				kill -KILL "$$pid"; \
			fi; \
			stopped=true; \
		fi; \
	}; \
	if [ -f "$(PID_FILE)" ]; then \
		pid=$$(cat "$(PID_FILE)"); \
		stop_pid "$$pid" || true; \
		rm -f "$(PID_FILE)"; \
	fi; \
	config_name=$$(basename "$(RUN_CONFIG)"); \
	while IFS= read -r pid; do \
		[ "$$pid" != "$$$$" ] || continue; \
		[ -r "/proc/$$pid/cmdline" ] || continue; \
		cmd=$$(tr '\0' ' ' <"/proc/$$pid/cmdline"); \
		case "$$cmd" in \
			*llama-line*--config*"$$config_name"*) stop_pid "$$pid" || true ;; \
		esac; \
	done < <(pgrep -f '[l]lama-line' || true); \
	if [ "$$stopped" = false ]; then echo "no local llama-line process found"; fi

restart: install stop ## Build, install, stop old broker, and run with streaming logs
	@if [ ! -f "$(RUN_CONFIG)" ]; then echo "missing $(RUN_CONFIG)" >&2; exit 1; fi
	@if [ ! -x "$(RUN_BIN)" ]; then echo "missing executable $(RUN_BIN)" >&2; exit 1; fi
	exec "$(RUN_BIN)" --config "$(RUN_CONFIG)"

compose-config: ## Validate Docker Compose if present
	@if [ -f docker-compose.yml ]; then docker compose config --quiet; else echo 'skip compose config'; fi

open-pilot-check: ## Verify Open Pilot issue template exists
	@test -f .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml

web-install: ## Install web/ dependencies (pnpm)
	cd web && pnpm install

web-dev: ## Run vite dev server (proxy /admin /ui/api → llama-line on :11434)
	cd web && pnpm dev

web-build: ## Build SPA into internal/ui/static/dist for Go embed
	cd web && pnpm install --silent && pnpm build
	rm -rf internal/ui/static/dist
	mkdir -p internal/ui/static/dist
	cp -r web/dist/. internal/ui/static/dist/

web-clean: ## Remove built SPA artifacts
	rm -rf web/dist internal/ui/static/dist
