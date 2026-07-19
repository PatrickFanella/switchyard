# backend/internal/

## Responsibility
Holds private server/runtime packages that cannot be imported outside the backend module.

## Design
- `controlplane/` owns nearly all domain and runtime behavior, organized into focused HTTP, executor, memory-store, and PostgreSQL-store files.
- `telemetry/` owns OTLP setup.
- `app/` is a small application-level utility retained by the server entry point.

## Flow
`cmd/app` composes these packages; requests enter `controlplane`, runtime operations emit telemetry, and persisted state exits through a `Store` implementation.

## Integration
Consumed only by commands under `backend/cmd/`; external systems interact through HTTP rather than Go imports.
