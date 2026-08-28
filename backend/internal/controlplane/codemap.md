# backend/internal/controlplane/

## Responsibility
Owns Switchyard's automation runtime: workflows, definitions, pipelines, triggers, runs, steps, events, artifacts, leases, retries, cancellation, Open Pilot policy, model/OpenCode adapters, HTTP APIs, and persistence.

## Design
- `Store` has matching memory and PostgreSQL implementations, split into `store_{workflows,runs,open_pilot,model_broker}.go` and `postgres_store_{workflows,runs,open_pilot,model_broker}.go`; shared store types and database helpers remain in `store.go` and `postgres_store.go`.
- `Runner` executes immutable pipeline snapshots through capability interfaces, records a resolved backend only immediately before an outbound attempt, and recovers previously running steps.
- `WorkerPool` uses durable claims and renewable leases, recovers expired leases, retries with backoff, and terminalizes worker panics; `Scheduler` converts cron/poll triggers into queued or immediate runs.
- `EnvModelBroker` and `EnvOpenCodeExecutor` are Switchyard-native contracts with compatibility backends.
- `backend_decision.go` resolves native and deprecated backend routes; its atomic store recording attaches namespaced correlation metadata and deprecated-attempt events.
- Open Pilot and Devlog executors live in `executor_open_pilot.go` and `executor_devlog.go`; the unreachable external Changemaker executor is removed, while adapter migration probing remains read-only.
- `open_pilot_repository_registry.go` owns strict v1 registry/manifest decoding and target parsing. `open_pilot_admission.go` is the singleton admission and worker pre-execution gate. `open_pilot_audit.go` builds the bounded control-only readiness report. Migration 025 adds rollback-compatible affinity, alias-equivalent reservations, and shadow identity fields without replacing `(repo, issue)` uniqueness; workers atomically adopt safe migrated runs before execution or lease recovery.
- HTTP registration is in `http_routes.go`; run/trigger, workflow-definition, Open Pilot lifecycle, and CORS handling are in `http_runs.go`, `http_workflow_definitions.go`, `http_open_pilot.go`, and `http_middleware.go`.
- Status providers expose configured versus effective selector and transitional compatibility metadata; listener boundaries prevent model ingress from exposing operator/admin routes.
- PostgreSQL `ModelService` ownership uses a watched dedicated advisory-lock session. Session or lock loss permanently poisons the old runtime, cancels active requests and workers, and fails readiness and broker operations before a replacement owner serves work.
- Operator and broker observability use bounded storage projections: PostgreSQL aggregates counts/latency and returns only capped recent rows, while memory mirrors the contract for local use.
- Artifacts are validated inline data URIs with persisted storage, MIME, decoded-size, SHA-256, and retention metadata. PostgreSQL triggers always maintain exact retained aggregates; a transaction-local writer setting selects trigger lifecycle accounting without double-counting rolling pre-023 writers. Deprecated backend usage and explicit revision/deployment-bound UTC daily observation coverage use separate tables and survive run retention.

## Flow
1. `SeedDefaults` creates first-party workflows, triggers, and workflow definitions.
2. API/scheduler validates inputs and creates a run with a pipeline snapshot.
3. A worker claims the run, renews its lease, and calls `Runner.ExecuteClaimedRun`.
4. The runner orders steps, resolves models/profiles, enforces executor limits, and dispatches workflow capabilities.
5. Each transition writes step status, events, artifacts, correlations, and final run status; backend evidence is written atomically before external work, while failures, panics, and expired leases close or retry work safely.
6. APIs expose control state and compatibility model/OpenCode surfaces; retention prunes old records.

## Integration
- Called by both Go commands and consumed remotely by `web/`.
- Persists through PostgreSQL schemas in `migrations/`; can use in-memory state for local runs.
- Integrates with managed OpenCode, Ollama/llama-line, Gitea, Git, command execution, and OTEL spans.
- Important entry points: `http.go`, `http_routes.go`, `http_runs.go`, `http_workflow_definitions.go`, `http_open_pilot.go`, `runner.go`, `executor.go`, `executor_open_pilot.go`, `executor_devlog.go`, `backend_decision.go`, `store.go`, `postgres_store.go`, `pipeline.go`, `worker.go`, `scheduler.go`.
