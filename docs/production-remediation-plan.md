# Production Remediation Plan

This plan tracks the release-readiness audit remediation. Phases 0–5 have focused commits; phase 6 is complete when the final production gate passes and its focused commit is recorded.

## CI verification gates

- `switchyard-check` runs `make verify` for every push and pull request. Its runner contract is Go `1.26.5`, Node.js major `24`, Corepack-provided pnpm `10.33.0`, Make, Docker Engine, and the Docker Compose plugin.
- `switchyard-production-verify` runs `make verify-production` manually or weekly for release qualification. It requires the baseline contract plus Docker Buildx and a bootstrappable BuildKit builder.

Both workflows fail before Make when the runner contract does not match. After Make, they also fail if `gofmt` changed any checked-out Go source, so formatting cannot be silently repaired in CI. The production preflight performs a temporary OCI build using the exact pinned SBOM generator and `--provenance=mode=max` options from `make release-artifacts`, then removes its temporary output. CI sets only `POSTGRES_PASSWORD=switchyard-ci-only-not-a-secret` so clean-checkout Compose validation and test scripts can run; it is not a deployment credential. The workflows intentionally rely on the runner toolchain rather than unpinned setup actions.

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

- 2026-07-19 local release qualification: `make verify` and `make verify-production` both passed. The production gate included 329 race-tested control-plane cases, 19 frontend tests, a 30-second soak with 600 authenticated requests across eight clients, isolated PostgreSQL lifecycle/integration tests, Go/web/OpenCode vulnerability audits, Docker smoke tests, reproducible Go builds, and three OCI archives with pinned SBOM/provenance attestations and verified SHA-256 checksums.
- 2026-07-19 compatibility lifecycle review: JSON/SSE native, Ollama, llama-line, and nested OCQ routes; non-generation endpoints; asynchronous restart/retry; generic, Devlog, Open Pilot, and AI-review execution; panic/lease recovery; and memory/PostgreSQL correlation/event atomicity passed focused regression coverage. Oracle review returned GO with no remaining concerns.
- 2026-07-18 frontend Phase 3 review follow-up: removed the legacy commented fixture; 19 focused React 19 App/hash-navigation tests passed with observable UI waits, strict method/path/query fixtures, real `Response` objects, and typed Open Pilot state responses. The Open Pilot resume flow now visibly verifies the POST-selected run and refreshed lifecycle state. Local ESLint, TypeScript no-emit, and Vite production build also passed.

- `make verify`: formatting, static analysis, unit/integration tests, web tests/build, Compose validation, and byte-for-byte Go rebuild comparison.
- `make api-race`: lifecycle, cancellation, worker, scheduler, and API tests with the race detector.
- `make api-soak`: sustained concurrent authenticated API, readiness, metrics, process-survival, and graceful-shutdown checks (30 seconds by default; configurable for longer release qualification).
- `make postgres-test`: migrations and persistence behavior against an isolated pinned Postgres instance.
- `make security-audit`: Go vulnerability scan plus high-severity web and OpenCode dependency audits.
- `make docker-smoke`: isolated authenticated API, readiness, ingress-boundary, model-list, and graceful-shutdown checks.
- `make release-verify`: pinned OCI archives with SBOM/provenance attestations and verified SHA-256 checksums.

All code-level phases and documented local release gates passed on 2026-07-19. Release promotion still requires the same production workflow to pass on the actual Gitea runner and normal environment-specific deployment checks (credentials, backups, restore, monitoring, and rollback) for the target environment.
