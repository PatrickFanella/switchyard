# scripts/

## Responsibility
Provides integration, soak, PostgreSQL, Docker, and model-broker smoke verification scripts.

## Design
Scripts use strict shell mode, isolated temporary/container resources, explicit health checks, and assertions against API contracts.

## Flow
- `test-api-soak.sh` exercises concurrent authenticated traffic and graceful shutdown.
- `test-docker-api.sh` validates the composed stack, auth, listener isolation, migrations, and shutdown; `SWITCHYARD_TEST_DOCKER_NETWORK` uses an existing runner network and container DNS instead of published ports.
- `test-postgres.sh` runs the PostgreSQL persistence and lifecycle-contract gate against an isolated pinned database, including durable claims, retries, lease recovery, and backend-attempt evidence; it supports the same optional runner network.
- `smoke-model-broker.sh` checks health, model listing, Ollama generate, and OpenAI chat compatibility.
- `smoke-production-contract.sh` proves authenticated control, model generation, listener isolation, and the public authentication redirect on a resolved production target.
- `backup-postgres.sh` produces verified custom-format backups; `restore-postgres-drill.sh` restores them into isolated PostgreSQL and proves API readiness.
- `report-compatibility-usage.sh` summarizes durable deprecated-backend attempts without treating configuration alone as usage.
- `check-docs-consistency.sh` keeps current-status documents and stable API references aligned.

## Integration
Invoked by `Makefile` verification, recovery, and production gates; targets the Go API, PostgreSQL, Docker Compose, model ingress, and current documentation contracts.
