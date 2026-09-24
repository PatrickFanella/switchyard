# backend/

## Responsibility
Implements Switchyard's Go control-plane API, workflow runtime, native model broker, OpenCode executor adapter, persistence, scheduler, workers, and CLI.

## Design
- Two binaries share `internal/controlplane`: `cmd/app` is the server and `cmd/switchyard` is the operator CLI.
- `internal/runtimeconfig/defaults-v1.json` is the versioned source for critical runtime defaults consumed by Go and checked against rendered Compose and `.env.example`.
- `internal/runtimeconfig/inventory-v1.json` classifies settings by source of truth, sensitivity, mutability, restart requirement, and authorization; `docs/reference/runtime-configuration.md` documents the v1 boundary.
- `ModelService` is the single model-runtime lifecycle owner. PostgreSQL mode continuously watches its dedicated session advisory lock, permanently fences the runtime on first loss, and rejects a second healthy owner; memory mode remains process-local. Workflows receive the narrow `ModelGeneration` capability; authenticated control and compatibility handlers receive lifecycle operations through `ModelRuntimeControl` or a thin facade.
- Interfaces (`Store`, `WorkflowExecutor`, `ModelGeneration`, `ModelRuntimeControl`, `OpenCodeExecutor`) isolate persistence and compatibility backends; memory and PostgreSQL implementations are split by workflow, run, Open Pilot, broker, and artifact measurement domains.
- `OpenPilotAdmissionGate` canonicalizes repository/issue identity, binds new and resumed runs to the current singleton executor generation, and rejects runtime-manifest entries that cannot use that singleton. Migration 025 keeps legacy keys while adding durable affinity, alias-equivalent reservations, canonical collision evidence, and atomic adoption for safe migrated runs.
- The API, model-compatible ingress, and opt-in HMAC webhook ingress use separate handlers and listeners.

## Flow
1. `cmd/app` loads telemetry and selects memory or PostgreSQL storage.
2. Defaults are seeded, then one `ModelService` is shared by workflow generation, control handlers, and compatibility adapters before workers, scheduler, and retention are assembled.
3. HTTP or scheduled triggers create runs; workers claim and execute pipeline snapshots.
4. Executors call models, OpenCode, Gitea, Git, or allowlisted commands and persist steps, events, artifacts, resolved backend decisions, and compatibility evidence. PostgreSQL artifact triggers maintain retained and lifecycle count/byte aggregates in each mutation transaction; the API process records revision-bound daily compatibility coverage for both deprecated backends in a table separate from positive-use rows.

## Integration
- Serves `web/` and `cmd/switchyard` through the control-plane API.
- Uses PostgreSQL and the embedded migrations in `internal/controlplane/migrations/`.
- Calls the managed service in `services/opencode/`, optional Ollama/llama-line endpoints, Gitea, and OTLP collectors.
- Serves authenticated bounded artifact and canonical compatibility-observation reports on the control listener and fixed-name artifact metrics on both canonical metrics routes.
- Built and configured by the root `Makefile` and `docker-compose.yml`.
- Listener/OpenAPI tests classify every documented method on the control and model handlers, including native lifecycle, artifact, and compatibility evidence routes.
