# backend/cmd/app/

## Responsibility
Bootstraps and runs the Switchyard API/runtime process.

## Design
- Environment-driven composition with memory storage as a local fallback and PostgreSQL for durability.
- Long-lived scheduler, retention manager, and worker pool share a signal-aware root context.
- Control-plane and model ingress are separate `http.Server` instances when configured on different addresses.

## Flow
1. `main.go` installs JSON logging and optional OTLP tracing.
2. It opens/migrates storage, seeds defaults, and creates `Runner` plus `API`.
3. Workers, scheduler, and retention loops start from environment settings.
4. Listeners serve until a signal/error, then intake stops and active work drains within configured grace periods.

## Integration
- Imports `internal/controlplane`, `internal/telemetry`, and the minimal `internal/app` greeting.
- Containerized by `backend/Dockerfile`; wired to PostgreSQL and OpenCode by `docker-compose.yml`.
