# scripts/

## Responsibility
Provides integration, soak, PostgreSQL, Docker, model-broker, and release-evidence verification scripts.

## Design
Scripts use strict shell mode, isolated temporary/container resources, explicit health checks, and assertions against API contracts. Production backup and restore adapters route Docker through `deploy/docker-local`; inherited CLI environment, context, config, and `PATH` cannot select another binary or daemon.

## Flow
- `test-api-soak.sh` exercises concurrent authenticated traffic and graceful shutdown.
- `test-docker-api.sh` validates the composed stack, auth, listener isolation, migrations, and shutdown; `SWITCHYARD_TEST_DOCKER_NETWORK` uses an existing runner network and container DNS instead of published ports.
- `test-linked-worktree-compose.sh` creates a linked worktree through the API mount, then proves OpenCode Git discovery and verifier diff execution with only the narrow Compose mounts.
- `test-postgres.sh` runs the PostgreSQL persistence and lifecycle-contract gate against an isolated pinned database, including durable claims, retries, lease recovery, and backend-attempt evidence; it supports the same optional runner network.
- `smoke-model-broker.sh` checks health, model listing, Ollama generate, and OpenAI chat compatibility.
- `smoke-production-contract.sh` derives control and model targets from validated listener settings, then proves authenticated control, model generation, listener isolation, and the public authentication redirect.
- `smoke-remote-production.sh` requires explicit remote web, control, model, public, authentication-origin, deployment-identity, read-token, and model inputs, then proves the live read-only paths and tagged source/deployment identity without inspecting host-local services or firewall state; deterministic fake-curl tests prevent network access.
- `backup-postgres.sh` publishes policy-bound custom-format bundles with a manifest-last completion marker.
- `restore_evidence.py` strictly validates policies and completed bundles, dispatches the sole approved provider adapter, and writes provider-neutral no-clobber evidence for every attempt.
- `restore_drill_scheduler.py` validates and write-probes independent private sinks, serializes full attempts with a private advisory lock, selects the newest metadata-validated completed bundle, and runs the correlated disposable restore under a whole-attempt deadline. Locked atomic metrics preserve prior success state.
- `restore-postgres-drill.sh` is the disposable PostgreSQL 17 adapter. It uses a per-attempt internal Docker network, reclaimable exact-owned resources, random credentials, and in-container probes while capturing object, migration, row-count, readiness, and authenticated-workflow observations in a private provider payload.
- `report-compatibility-usage.sh` summarizes durable deprecated-backend attempts without treating configuration alone as usage.
- `check-docs-consistency.sh` runs the standard-library semantic docs checker for capability evidence, deployed release pointers, Markdown/codemap links, and versioned runtime defaults against rendered Compose and `.env.example`.
- `test_check_docs_consistency.py` covers tracked canonical evidence, verified Git-archive execution, release pointers independent of HEAD, inline/reference Markdown links, and sanitized malformed input failures.
- `validate_release_evidence.py` validates local candidate/deployment proof and generated manifest checksums without third-party packages or network access.
- `download_retirement_qualifications.py` validates two small Gitea run/artifact references, verifies each unexpired artifact against its run, and accepts only the fixed qualification, deployed evidence, manifest, and compatibility-report basenames.
- `download_retirement_report.py` resolves a small Gitea run/artifact reference, verifies successful same-repository run and revision provenance, and extracts one bounded compatibility report.
- `produce_retirement_qualification.py` consumes one durable two-file candidate record, authenticated production compatibility report, and private scheduled restore evidence to emit and locally validate the exact four-file long-retention qualification bundle.
- `validate_production_config.py` checks the environment or an optional safely parsed dotenv file for the production Tailscale, proxy-authentication and hop-token, URL, host-port, and listener-isolation contract; its standard-library unit tests run in the baseline gate.
- `render_tailscale_web_gate.py` renders the pre-DNAT nft gate from the same Tailscale destination, proxy peer, web port, and pinned Docker bridge contract used by Compose and production validation.
- `manage_tailscale_web_forward.py` idempotently manages and verifies the exact UFW web-forward and pinned API-to-bridge-gateway Ollama rules, with per-rule rollback deletion.
- `validate_production_config.py` also enforces the production OpenCode strict-mode, non-default credential, dedicated workspace/config/data directories, private marker/layout, runtime ownership, safe modes, Git config/origin policy, and canonical container-path contract.
- `test_compose_runtime_isolation.py` renders Compose JSON with `/dev/null` as the env file and a synthetic process environment, then checks API/OpenCode/verifier hardening and source-checkout-free executor mounts without exposing rendered configuration.
- `prepare_opencode_workspace.py` initializes only the empty default relative Git workspace, validates existing Git/config/data paths and ownership with sanitized scoped-safe Git commands, and creates the private managed worktree marker without changing ownership.
- `test_opencode_config.py` locks the strict last-match Git command policy to read-only allow patterns and final mutation/network denies.
- `alert_lifecycle.py` is an opt-in, standard-library-only manual check of Alertmanager firing and resolved delivery through ntfy; its fake-server unit tests run in the baseline gate without contacting live services.
- `test_canonical_docker.py`, `test_restore_evidence.py`, `test_restore_shell_safety.py`, `test_restore_drill_scheduler.py`, and `test_restore_drill_installer.py` cover the fixed local Docker boundary, policy, bundle, qualification, scheduled selection/metrics, canonical-env rendering, main-unit/workload rollback, installer mutation ordering, sanitized failure evidence, provider payload semantics, and destructive shell guardrails without Docker.
- `test_web_gate_installer.py` uses root-command fixtures to verify the renderer's root/0755 destination contract, fail-closed web transition, unchanged-gate behavior, and full gate rollback without Docker or nft mutation.
- `test-restore-postgres-integration.sh` creates a real migrated fixture, publishes a completed bundle, and runs the pinned PostgreSQL adapter in the production gate.

## Integration
Invoked by `Makefile` verification, recovery, and production gates; targets the Go API, PostgreSQL, Docker Compose, model ingress, and current documentation contracts.
