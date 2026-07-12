# Production Remediation Plan

This plan tracks the release-readiness audit remediation. Phases 0–5 have focused commits; phase 6 is complete when the final production gate passes and its focused commit is recorded.

## Phase 0 — containment and safe defaults

- Separate operator and model ingress listeners.
- Bind published ports to loopback by default.
- Make command execution deny-by-default and remove shell invocation.
- Patch known toolchain vulnerabilities and pin mutable build inputs.
- Keep verification outputs out of the source tree.

## Phase 1 — lifecycle correctness and truthful operations

- Replace placeholder health, queue, session, agent, and metrics data.
- Add paused and timed-out run semantics.
- Resolve resume cursors by immutable pipeline step identity and version.
- Enforce step/run ownership.

## Phase 2 — authorization, cancellation, and bounded queues

- Authenticate every control-plane request and authorize by role.
- Restrict CORS and record actors in audit events.
- Cancel queued and running work through registered execution contexts.
- Add durable, bounded, priority-aware model request claiming.

## Phase 3 — durable asynchronous runtime

- Dispatch production work asynchronously by default.
- Add CLI polling and explicit wait/detach behavior.
- Drain scheduler/workers on shutdown.
- Apply workflow/step retry and timeout policies with backoff.

## Phase 4 — persistence, scale, and privacy

- Add cursor pagination and bounded list APIs.
- Add retention, artifact storage limits, and prompt/response redaction.
- Propagate request contexts to database operations and expose readiness.
- Enforce body, prompt, response, and rate limits.

## Phase 5 — complete supported contracts

- Replace the partial cron parser and document timezone/missed-run policy.
- Add workflow-definition validation, versioning, activation, and rollback.
- Implement or remove the HTTP executor.
- Persist compatibility feedback or return an explicit unsupported response.

## Phase 6 — operability and reproducibility

- Export real runtime metrics and structured, redacted logs.
- Contract-test OpenAPI against registered routes.
- Pin images and external installers, generate SBOM/provenance, and keep builds reproducible.
- Run full Postgres, Docker, lifecycle, cancellation, security, and soak gates.

## Release acceptance evidence

- `make verify`: formatting, static analysis, unit/integration tests, web tests/build, Compose validation, and byte-for-byte Go rebuild comparison.
- `make api-race`: lifecycle, cancellation, worker, scheduler, and API tests with the race detector.
- `make postgres-test`: migrations and persistence behavior against an isolated pinned Postgres instance.
- `make security-audit`: Go vulnerability scan plus high-severity web and OpenCode dependency audits.
- `make docker-smoke`: isolated authenticated API, readiness, ingress-boundary, model-list, and graceful-shutdown checks.
- `make release-verify`: pinned OCI archives with SBOM/provenance attestations and verified SHA-256 checksums.

The application is not production-ready until every phase is complete and the security, correctness, durability, and operability release gates pass.
