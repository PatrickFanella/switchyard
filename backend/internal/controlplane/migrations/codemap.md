# backend/internal/controlplane/migrations/

## Responsibility
Defines the authoritative embedded PostgreSQL schema history for the control plane.

## Design
Numbered, forward-only SQL migrations evolve workflows, triggers, runs, steps, Open Pilot state, workflow definitions/versions and the separate active-version pointer, model broker ledger/feedback, pipeline snapshots, retention indexes, native-workflow activation, rolling-writer-safe trigger-maintained artifact aggregates, and compatibility observation coverage separate from positive-use rows.

## Flow
`migrate.go` embeds all SQL, sorts filenames, checks `schema_migrations`, and applies each unapplied file transactionally before recording its version.

## Integration
- Executed automatically by `cmd/app` before constructing `PostgresStore`.
- Schema columns are consumed directly by `postgres_store.go`.
