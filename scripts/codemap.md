# scripts/

## Responsibility
Provides integration, soak, PostgreSQL, Docker, model-broker, and release-evidence verification scripts.

## Design
Scripts use strict shell mode, isolated temporary/container resources, explicit health checks, and assertions against API contracts.

## Flow
- `test-api-soak.sh` exercises concurrent authenticated traffic and graceful shutdown.
- `test-docker-api.sh` validates the composed stack, auth, listener isolation, migrations, and shutdown; `SWITCHYARD_TEST_DOCKER_NETWORK` uses an existing runner network and container DNS instead of published ports.
- `test-linked-worktree-compose.sh` creates a linked worktree through the API mount, then proves OpenCode Git discovery and verifier diff execution with only the narrow Compose mounts.
- `test-postgres.sh` runs the PostgreSQL persistence and lifecycle-contract gate against an isolated pinned database, including durable claims, retries, lease recovery, and backend-attempt evidence; it supports the same optional runner network.
- `smoke-model-broker.sh` checks health, model listing, Ollama generate, and OpenAI chat compatibility.
- `smoke-production-contract.sh` derives control and model targets from validated listener settings, then proves authenticated control, model generation, listener isolation, and the public authentication redirect.
- `backup-postgres.sh` publishes policy-bound custom-format bundles with a manifest-last completion marker.
- `restore_evidence.py` strictly validates policies and completed bundles, dispatches the sole approved provider adapter, and writes provider-neutral no-clobber evidence for every attempt.
- `restore-postgres-drill.sh` is the disposable PostgreSQL 17 adapter. It uses a per-attempt internal Docker network, reclaimable exact-owned resources, random credentials, and in-container probes while capturing object, migration, row-count, readiness, and authenticated-workflow observations in a private provider payload.
- `report-compatibility-usage.sh` summarizes durable deprecated-backend attempts without treating configuration alone as usage.
- `check-docs-consistency.sh` keeps current-status documents and stable API references aligned.
- `validate_release_evidence.py` validates local candidate/deployment proof and generated manifest checksums without third-party packages or network access.
- `validate_production_config.py` checks the environment or an optional safely parsed dotenv file for the production Tailscale, proxy-authentication, URL, host-port, and listener-isolation contract; its standard-library unit tests run in the baseline gate.
- `validate_production_config.py` also enforces the production OpenCode strict-mode, non-default credential, dedicated workspace/config/data directories, private marker/layout, runtime ownership, safe modes, Git config/origin policy, and canonical container-path contract.
- `test_compose_runtime_isolation.py` renders Compose JSON with `/dev/null` as the env file and a synthetic process environment, then checks API/OpenCode/verifier hardening and source-checkout-free executor mounts without exposing rendered configuration.
- `prepare_opencode_workspace.py` initializes only the empty default relative Git workspace, validates existing Git/config/data paths and ownership with sanitized scoped-safe Git commands, and creates the private managed worktree marker without changing ownership.
- `test_opencode_config.py` locks the strict last-match Git command policy to read-only allow patterns and final mutation/network denies.
- `alert_lifecycle.py` is an opt-in, standard-library-only manual check of Alertmanager firing and resolved delivery through ntfy; its fake-server unit tests run in the baseline gate without contacting live services.
- `test_restore_evidence.py` and `test_restore_shell_safety.py` cover policy, bundle, qualification, sanitized failure evidence, provider payload semantics, and destructive shell guardrails without Docker.
- `test-restore-postgres-integration.sh` creates a real migrated fixture, publishes a completed bundle, and runs the pinned PostgreSQL adapter in the production gate.

## Integration
Invoked by `Makefile` verification, recovery, and production gates; targets the Go API, PostgreSQL, Docker Compose, model ingress, and current documentation contracts.
