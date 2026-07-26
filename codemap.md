# Repository Atlas: Switchyard

## Project Responsibility

Switchyard is one cohesive automation runtime for durable workflows, agent execution, model routing, operator control, and first-party Open Pilot, Changemaker, and Devlog policy. It owns runs, steps, events, artifacts, leases, retries, cancellation, APIs, UI, and operational summaries while keeping model-compatible ingress isolated from the operator control plane.

## System Entry Points

- `backend/cmd/app/main.go`: API/runtime process composition and listener lifecycle.
- `backend/cmd/switchyard/main.go`: operator CLI.
- `backend/internal/controlplane/http.go`: control-plane and model-ingress HTTP contracts.
- `backend/internal/controlplane/runner.go`: run and pipeline execution coordinator.
- `web/src/main.tsx` and `web/src/App.tsx`: operator dashboard bootstrap and shell.
- `services/opencode/Dockerfile`: managed OpenCode execution environment.
- `docker-compose.yml`: local/production-like service topology.
- `Makefile`: canonical build and verification interface.
- `.gitea/workflows/verify-production.yml`: scheduled/manual production qualification on contract-checked runners.
- `docs/development-readiness-report-2026-07-19.md`: release-candidate readiness verdict, local gate evidence, and promotion prerequisites.

## Responsibility
Provide a Go automation control plane, React operator UI, durable PostgreSQL state, a native model broker/compatibility ingress, and a managed OpenCode executor in a minimal deployable stack.

## Design
- **Native runtime core:** Go interfaces separate domain policy from stores and external executors.
- **Durable execution:** pipeline snapshots, step/event/artifact records, workers with leases, retries, scheduling, and retention.
- **Boundary safety:** authenticated operator routes and provider-compatible model routes are exposed through different handlers/listeners.
- **First-party workflows:** Open Pilot, Changemaker, and Devlog use generic runtime mechanics rather than owning schedulers.
- **Managed agent runtime:** Blacktower is represented by OpenCode profiles, prompts, plugins, and model defaults—not a separate app.
- **Operator visibility:** the web SPA consumes typed API contracts for runs, queues, observability, adapters, and configuration.

## Flow
1. Compose starts PostgreSQL, managed OpenCode, the Go API, and web UI.
2. The API migrates storage, seeds defaults, and starts scheduler/worker/retention loops.
3. Operators, schedules, polls, or webhooks create queued runs with immutable pipeline snapshots.
4. Workers claim runs, execute ordered steps through internal/model/OpenCode/command/Git/Gitea adapters, and persist evidence.
5. Model-compatible clients enter the isolated native broker listener, which routes to OpenCode, Ollama, or transitional llama-line compatibility.
6. The dashboard and CLI query/control the authenticated operator API.

## Integration
- External dependencies: PostgreSQL, OpenCode, optional Ollama/llama-line, Gitea, Git, Docker, and optional OTLP collectors.
- Configuration is environment-driven through `.env.example` and `docker-compose.yml`; secrets/local state are ignored.
- Canonical verification is `make verify`.
- Gitea runs the baseline gate on pushes/pull requests and provides scheduled/manual `make verify-production` qualification with pinned actions, explicit Go/Node/pnpm/Docker/Buildx checks, and an SBOM/provenance preflight.

## Directory Map

| Directory | Responsibility | Detailed map |
|---|---|---|
| `backend/` | Go API, runtime, persistence, adapters, model broker, and CLI. | [backend/codemap.md](backend/codemap.md) |
| `web/` | React operator control center. | [web/codemap.md](web/codemap.md) |
| `services/` | Managed supporting services, currently OpenCode. | [services/codemap.md](services/codemap.md) |
| `deploy/` | systemd installation for the Compose stack. | [deploy/codemap.md](deploy/codemap.md) |
| `scripts/` | Integration, soak, persistence, Docker, and broker checks. | [scripts/codemap.md](scripts/codemap.md) |
| `migrations/` | Root bootstrap SQL; embedded backend migrations are authoritative for runtime state. | [migrations/codemap.md](migrations/codemap.md) |

## Deliberately Unmapped

- `docs/`, tests, generated build/release artifacts, local data, and dependencies are excluded from cartography state.
- `blacktower/` is an ignored external reference checkout; Switchyard-native profiles live under `services/opencode/default-config/`.
- `workflows/` is currently empty because first-party workflow policy is implemented in the native control plane.
