# migrations/

## Responsibility
Contains the root-level bootstrap SQL retained for external/manual database setup.

## Design
The current `0001_init.sql` creates only generic application metadata; authoritative runtime migrations are embedded under `backend/internal/controlplane/migrations/`.

## Flow
External tooling may apply this bootstrap independently; normal API startup applies the embedded control-plane migration series instead.

## Integration
Related to PostgreSQL deployment, but not loaded by `controlplane.Migrate`.
