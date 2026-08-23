SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

.PHONY: help verify verify-production deps fmt lint test build clean compose-config docs-check open-pilot-check api-run api-build cli-build api-test api-race api-soak api-soak-extended postgres-test postgres-backup postgres-restore-drill security-audit docker-build docker-smoke api-smoke production-smoke compatibility-report web-dev web-build web-test web-format switchyard-smoke docker-up docker-down docker-reset release-artifacts release-verify release-evidence-test release-evidence-verify reproducible-build-check

API_ADDR ?= :8080
SMOKE_ADDR ?= :18080
SMOKE_URL ?= http://127.0.0.1:18080

help:
	@awk 'BEGIN {FS = ":.*##"; printf "Commands:\n"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

deps: ## Install web dependencies
	pnpm --dir web install --frozen-lockfile

verify: deps fmt lint test build reproducible-build-check compose-config docs-check open-pilot-check release-evidence-test ## Run all checks

fmt: ## Format/check Go and web sources
	@git ls-files --cached --others --exclude-standard 'backend/**/*.go' | xargs -r gofmt -w
	pnpm --dir web run format

lint: ## Run Go vet and web lint
	cd backend && go vet ./...
	pnpm --dir web run lint

test: api-test web-test ## Run backend and web tests

api-test: ## Run Switchyard backend tests
	cd backend && go test ./...

api-race: ## Run backend tests under the race detector
	cd backend && go test -race ./...

api-soak: ## Exercise sustained concurrent authenticated API traffic and graceful shutdown
	./scripts/test-api-soak.sh

api-soak-extended: ## Run the release-grade 15-minute authenticated API soak
	SWITCHYARD_SOAK_DURATION=900 SWITCHYARD_SOAK_CLIENTS=16 SWITCHYARD_SOAK_MIN_REQUESTS=10000 ./scripts/test-api-soak.sh

postgres-test: ## Run integration tests against an isolated pinned Postgres container
	./scripts/test-postgres.sh

postgres-backup: ## Create and verify a custom-format backup in SWITCHYARD_BACKUP_DIR
	./scripts/backup-postgres.sh

postgres-restore-drill: ## Restore BACKUP into an isolated PostgreSQL container and prove API readiness
	@test -n "$(BACKUP)" || (echo "BACKUP=/path/to/switchyard.dump is required" >&2; exit 2)
	./scripts/restore-postgres-drill.sh "$(BACKUP)"

security-audit: ## Audit Go and web dependencies for known vulnerabilities
	cd backend && govulncheck ./...
	pnpm --dir web audit --audit-level high
	cd services/opencode && npm audit --audit-level=high

docker-build: ## Build all pinned container images
	docker compose build

docker-smoke: docker-build ## Exercise auth, listener isolation, readiness, migrations, and shutdown in isolated containers
	./scripts/test-docker-api.sh

web-test: ## Run Switchyard web tests
	pnpm --dir web run test

build: api-build web-build ## Build backend and web

api-build: cli-build ## Build Switchyard API binary
	mkdir -p bin
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o ../bin/switchyard-api ./cmd/app

cli-build: ## Build Switchyard CLI binary
	mkdir -p bin
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o ../bin/switchyard ./cmd/switchyard

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
	SWITCHYARD_LISTEN_ADDR=$(SMOKE_ADDR) SWITCHYARD_CONTROL_API_TOKEN=smoke-token SWITCHYARD_WORKER_ENABLED=false SWITCHYARD_SCHEDULER_ENABLED=false go run ./cmd/app >/tmp/switchyard-smoke.log 2>&1 & \
	pid=$$!; \
	trap 'kill $$pid >/dev/null 2>&1 || true' EXIT; \
	for _ in 1 2 3 4 5 6 7 8 9 10; do \
		if curl -fsS $(SMOKE_URL)/healthz >/dev/null 2>&1; then break; fi; \
		sleep 0.2; \
	done; \
	curl -fsS -H 'Authorization: Bearer smoke-token' $(SMOKE_URL)/api/workflows >/dev/null; \
	curl -fsS -H 'Authorization: Bearer smoke-token' $(SMOKE_URL)/api/triggers >/dev/null; \
	curl -fsS -H 'Authorization: Bearer smoke-token' $(SMOKE_URL)/api/runs >/dev/null; \
	echo "Switchyard API smoke passed at $(SMOKE_URL)"

switchyard-smoke: api-smoke ## Run Switchyard control-plane smoke checks

production-smoke: ## Verify the real production control, model, isolation, and public-web contracts
	./scripts/smoke-production-contract.sh

compatibility-report: ## Summarize deprecated backend attempts from durable broker history
	./scripts/report-compatibility-usage.sh

docker-up: ## Start local Switchyard stack
	docker compose up --build

docker-down: ## Stop local Switchyard stack
	docker compose down

docker-reset: ## Stop stack and remove local Postgres data volume; requires CONFIRM=1
	@test "$(CONFIRM)" = "1" || (echo "Refusing to remove database volume. Rerun with CONFIRM=1 make docker-reset" && exit 1)
	docker compose down -v

clean: ## Remove generated build artifacts
	rm -rf web/dist bin

compose-config: ## Validate Docker Compose config
	docker compose config --quiet

docs-check: ## Verify current-status documents and stable API references agree
	./scripts/check-docs-consistency.sh

open-pilot-check: ## Verify Open Pilot templates are present
	@test -f .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml
	@grep -q 'Test Command' .gitea/ISSUE_TEMPLATE/open-pilot-task.yaml

release-artifacts: ## Build pinned OCI archives with BuildKit SBOM, provenance, and source manifest
	mkdir -p release
	docker buildx build --build-arg SWITCHYARD_SOURCE_REVISION=$$(git rev-parse HEAD) --platform linux/amd64 --attest type=sbom,generator=docker.io/docker/buildkit-syft-scanner@sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68 --provenance=mode=max --output type=oci,dest=release/switchyard-api.oci.tar backend
	docker buildx build --build-arg SWITCHYARD_SOURCE_REVISION=$$(git rev-parse HEAD) --platform linux/amd64 --attest type=sbom,generator=docker.io/docker/buildkit-syft-scanner@sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68 --provenance=mode=max --output type=oci,dest=release/switchyard-web.oci.tar web
	docker buildx build --build-arg SWITCHYARD_SOURCE_REVISION=$$(git rev-parse HEAD) --platform linux/amd64 --attest type=sbom,generator=docker.io/docker/buildkit-syft-scanner@sha256:79e7b013cbec16bbb436f312819a49a4a57752b2270c1a9332ae1a10fcc82a68 --provenance=mode=max --output type=oci,dest=release/switchyard-opencode.oci.tar services/opencode
	cd release && sha256sum *.oci.tar > SHA256SUMS
	./scripts/write-release-manifest.sh

release-verify: release-artifacts ## Verify release archives and checksums
	cd release && sha256sum -c SHA256SUMS

release-evidence-test: ## Test the release-evidence contract
	python3 -m unittest discover -s scripts -p 'test_validate_release_evidence.py'

release-evidence-verify: ## Validate EVIDENCE and MANIFEST for CANDIDATE_SHA
	@test -n "$(CANDIDATE_SHA)" || (echo "CANDIDATE_SHA is required" >&2; exit 2)
	@test -n "$(EVIDENCE)" || (echo "EVIDENCE is required" >&2; exit 2)
	@test -n "$(MANIFEST)" || (echo "MANIFEST is required" >&2; exit 2)
	python3 scripts/validate_release_evidence.py --candidate-sha "$(CANDIDATE_SHA)" --evidence "$(EVIDENCE)" --manifest "$(MANIFEST)"

reproducible-build-check: ## Build Go binaries twice and compare bytes
	@set -euo pipefail; tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cd backend; \
	CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o "$$tmp/api-1" ./cmd/app; \
	CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o "$$tmp/api-2" ./cmd/app; \
	CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o "$$tmp/cli-1" ./cmd/switchyard; \
	CGO_ENABLED=0 go build -trimpath -ldflags='-buildid=' -o "$$tmp/cli-2" ./cmd/switchyard; \
	cmp "$$tmp/api-1" "$$tmp/api-2"; cmp "$$tmp/cli-1" "$$tmp/cli-2"

verify-production: verify api-race api-soak postgres-test security-audit docker-smoke release-verify ## Run all production release gates
