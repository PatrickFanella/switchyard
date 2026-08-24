# backend/

## Responsibility
Implements Switchyard's Go control-plane API, workflow runtime, native model broker, OpenCode executor adapter, persistence, scheduler, workers, and CLI.

## Design
- Two binaries share `internal/controlplane`: `cmd/app` is the server and `cmd/switchyard` is the operator CLI.
- `internal/runtimeconfig/defaults-v1.json` is the versioned source for critical runtime defaults consumed by Go and checked against rendered Compose and `.env.example`.
- `ModelService` is the single model-runtime lifecycle owner. Workflows receive the narrow `ModelGeneration` capability; authenticated control and compatibility handlers receive lifecycle operations through `ModelRuntimeControl` or a thin facade.
- Interfaces (`Store`, `WorkflowExecutor`, `ModelGeneration`, `ModelRuntimeControl`, `OpenCodeExecutor`) isolate persistence and compatibility backends; memory and PostgreSQL implementations are split by workflow, run, Open Pilot, broker, and artifact measurement domains.
- The API and model-compatible ingress use separate handlers and are intended for separate listeners.

## Flow
1. `cmd/app` loads telemetry and selects memory or PostgreSQL storage.
2. Defaults are seeded, then one `ModelService` is shared by workflow generation, control handlers, and compatibility adapters before workers, scheduler, and retention are assembled.
3. HTTP or scheduled triggers create runs; workers claim and execute pipeline snapshots.
4. Executors call models, OpenCode, Gitea, Git, or allowlisted commands and persist steps, events, artifacts, resolved backend decisions, and compatibility evidence. Artifact writes and run-retention pruning update durable aggregate count/byte measurements in the same transaction; the API process records revision-bound daily compatibility coverage for both deprecated backends.

## Integration
- Serves `web/` and `cmd/switchyard` through the control-plane API.
- Uses PostgreSQL and the embedded migrations in `internal/controlplane/migrations/`.
- Calls the managed service in `services/opencode/`, optional Ollama/llama-line endpoints, Gitea, and OTLP collectors.
- Serves authenticated bounded artifact and canonical compatibility-observation reports on the control listener and fixed-name artifact metrics on both canonical metrics routes.
- Built and configured by the root `Makefile` and `docker-compose.yml`.
- Listener/OpenAPI tests classify every documented method on the control and model handlers, including native lifecycle, artifact, and compatibility evidence routes.
