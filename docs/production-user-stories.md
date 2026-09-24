# Production User Stories

> **Historical record.** These stories accompanied the 2026-07 production remediation plan. Acceptance statements below describe the gates as designed then; current evidence levels are in `docs/status.md`.

These stories define the release behavior behind the production remediation plan. A story is accepted only when its automated gate passes and its operational evidence is available.

## Operator access and safety

**As an operator, I want every control-plane action authenticated and attributable so that an exposed listener cannot silently administer Switchyard.**

- Health endpoints remain usable by infrastructure without credentials.
- Control-plane requests require a write token, read-only token, or explicitly trusted reverse-proxy identity.
- Cross-origin access is deny-by-default, actors are recorded in audit events, and model ingress cannot reach administrative routes.
- Acceptance: authentication/authorization API tests and `make docker-smoke` pass.

**As an operator, I want truthful run state and safe cancellation so that the dashboard never reports work that did not happen.**

- Missing executors, failed dispatches, timeouts, and exhausted retries end in explicit terminal states.
- Paused work remains paused; resume uses immutable pipeline identity; step actions enforce run ownership.
- Canceling queued or running work updates durable state and propagates best-effort cancellation upstream.
- Acceptance: lifecycle, resume, ownership, retry, timeout, and cancellation tests pass under `make verify` and `make api-race`.

## Workflow authors and automation clients

**As a workflow author, I want immutable, validated workflow versions so that editing a definition cannot change an in-flight run.**

- Definitions are validated as drafts before activation.
- Runs capture a pipeline snapshot and version hash.
- Rollback creates a new active version and preserves history.
- HTTP steps require explicit opt-in, an allowlisted destination, bounded I/O, and safe redirect behavior.
- Acceptance: workflow definition, snapshot, activation, rollback, and HTTP policy tests pass.

**As an automation client, I want durable bounded dispatch so that overload and restarts produce predictable behavior.**

- Workflow runs and model requests are claimed durably with bounded concurrency and leases.
- Model queues reject overflow with `429` and `Retry-After`, honor priority then FIFO, and recover stale claims.
- Retry backoff, attempt limits, graceful shutdown, cron timezone, and missed-run coalescing are deterministic.
- Acceptance: worker, scheduler, queue, retry, recovery, and Postgres tests pass.

## Operators and privacy reviewers

**As an operator, I want bounded history, readiness, metrics, and structured logs so that I can operate the service without unbounded queries or misleading telemetry.**

- List APIs use bounded cursor pagination and terminal records expire under configured retention.
- Readiness checks persistence; Prometheus metrics derive from stored runtime state.
- JSON logs and persisted model history redact credential-shaped values.
- Acceptance: pagination, retention, readiness, metrics, redaction, and Postgres tests pass.

**As a privacy reviewer, I want strict input and artifact limits so that accidental or hostile payloads cannot create uncontrolled storage or log exposure.**

- Request bodies, prompts, responses, artifacts, HTTP responses, and client request rates have enforced bounds.
- Compatibility feedback validates its rating and redacts its optional comment before durable storage.
- Acceptance: limit, rate, feedback, redaction, and persistence tests pass.

## Release engineering

**As a release engineer, I want reproducible, inspectable artifacts so that a deployed image is traceable to reviewed source and pinned dependencies.**

- Go binaries reproduce byte-for-byte; base images, OS packages, JavaScript packages, installers, and the SBOM generator are pinned.
- OCI archives include SBOM and provenance attestations plus verified SHA-256 checksums.
- OpenAPI route coverage, vulnerability scans, race tests, isolated Postgres tests, and authenticated container smoke tests are release gates.
- Acceptance: every command in `docs/production-remediation-plan.md` under “Release acceptance evidence” passes, including the sustained API soak gate.
