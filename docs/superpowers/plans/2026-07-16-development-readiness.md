# Development Readiness Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Switchyard from strong early-beta engineering to repeatable production qualification while reducing control-plane maintenance risk.

**Architecture:** Preserve the existing `controlplane` package and public contracts while splitting implementation files along store, HTTP, and workflow-policy boundaries. Add CI around existing Make gates rather than inventing a second verification system. Keep compatibility routes available, but document and instrument them as transitional interfaces.

**Tech Stack:** Go 1.26, PostgreSQL 17, React 19, TypeScript 5.9, Vitest, pnpm 10, Docker Compose, Gitea Actions.

---

## File structure

- `.gitea/workflows/template-check.yml`: upgraded pull-request/push standard verification.
- `.gitea/workflows/verify-production.yml`: scheduled/manual full release qualification.
- `web/pnpm-workspace.yaml`, `web/package.json`, `web/pnpm-lock.yaml`: supported pnpm override/build policy with lockfile ownership remaining in `web/`.
- `backend/internal/controlplane/http*.go`: route registration, middleware, workflow/run endpoints, and compatibility endpoints separated by concern.
- `backend/internal/controlplane/*store*.go`: memory/PostgreSQL workflow, run, Open Pilot, and broker persistence separated by concern.
- `backend/internal/controlplane/*executor*.go`: core registry, Open Pilot, Changemaker, and Devlog policies separated by concern.
- `backend/internal/controlplane/*_test.go`: lifecycle regression coverage.
- `web/src/*.test.tsx`, `web/src/hooks/*.test.ts`: operator mutation/navigation coverage.
- `docs/reference/client-compatibility.md`: compatibility support/deprecation lifecycle.
- `README.md`, `docs/production-remediation-plan.md`: CI and release qualification instructions.

### Task 1: Supported pnpm configuration

- [x] Remove the ignored `pnpm` object from `web/package.json`.
- [x] Create `web/pnpm-workspace.yaml` with `overrides` for `@babel/core` and `js-yaml`, and an explicit allowlist for required dependency build scripts; do not create a root shared-workspace lockfile.
- [x] Run `pnpm install --lockfile-only` and confirm the lock remains frozen-install compatible.
- [x] Run `make deps web-format web-test web-build`; no ignored-configuration warning occurred and all commands passed.

### Task 2: Gitea verification workflows

- [x] Upgrade existing `.gitea/workflows/template-check.yml` for pushes and pull requests; pin checkout by commit, validate Go 1.26.5/Node 24/Corepack pnpm 10.33.0/Make/Docker/Compose, set a CI-only Compose password, and run `make verify` without adding a duplicate baseline gate.
- [x] Add `.gitea/workflows/verify-production.yml` for manual and weekly execution; validate the baseline contract plus Buildx, prove the exact pinned SBOM/provenance OCI build path, install pinned `govulncheck`, then run `make verify-production`.
- [x] Add concurrency and timeouts so stale runs cancel and production qualification cannot hang indefinitely.
- [x] Document local and CI gates in `README.md` and `docs/production-remediation-plan.md`.

### Task 3: Mechanical control-plane decomposition

- [x] Capture the baseline with `go test ./internal/controlplane -count=1` from `backend/` (2026-07-18: 254 tests passed).
- [x] Update `openapi_contract_test.go` to parse all production `http*.go` files with the Go AST before route registration moves out of `http.go` (2026-07-18: excludes `_test.go` files; comments cannot satisfy route coverage).
- [x] Move route registration/middleware from `http.go` into focused `http_*.go` files without changing symbols (2026-07-18: 254 focused tests passed).
- [x] Move workflow-definition handlers, run/trigger handlers, and Open Pilot handlers from `http.go` into focused `http_*.go` files without changing symbols (2026-07-18: `http_workflow_definitions.go`, `http_runs.go`, and `http_open_pilot.go`; 254 focused tests passed).
- [x] Move memory and PostgreSQL methods into workflow/Open Pilot/run/model-broker persistence files; keep `Store` and constructors in their current public files (2026-07-18: `store_{workflows,open_pilot,runs,model_broker}.go` and matching `postgres_store_*.go`; 254 focused tests passed after each implementation family and at final).
- [x] Move Open Pilot Git/worktree policy and Changemaker/Devlog execution out of `executor.go`; keep registry/contracts in `executor.go` (2026-07-18: added `executor_{open_pilot,changemaker,devlog}.go`; 254 focused tests passed).
- [x] Run `gofmt` and `go test ./internal/controlplane -count=1` after each split; expect identical behavior (2026-07-18: 254 focused tests passed after executor domain splits and final).
- [x] Apply Phase 2 review corrections (2026-07-18: remove extracted duplicate comments, use explicit Go 1.22 run/trigger action patterns, and relocate shared run validation helpers without behavioral changes).

### Task 4: Lifecycle regression coverage

- [x] Add store-contract coverage for queue → claim → failed attempt → delayed requeue → successful terminal completion and run it against both memory and PostgreSQL stores through the isolated-container gate.
- [x] Add a cancellation test proving queued cancellation prevents later worker claim.
- [x] Add a lease-expiry test proving stale work requeues below max attempts and fails when exhausted.
- [x] Add a synchronized competing-claim contract proving exactly one worker claims a queued run without timing sleeps.
- [x] Run focused memory tests, `go test -race ./internal/controlplane -count=1`, and `make postgres-test` (2026-07-18: `go test ./internal/controlplane -run 'TestStoreLifecycleContract/memory' -count=1` passed 6 tests; race run passed 265 tests; isolated PostgreSQL gate, including `TestStoreLifecycleContract/postgres`, passed in 0.604s).

### Task 5: Frontend operator-flow coverage

- [x] Extract shared fetch fixtures/render helpers from `App.test.tsx` to `test-utils.tsx`.
- [x] Add tests that dispatch a trigger, retry/cancel a run, resume Open Pilot, and verify errors remain visible without corrupting selection.
- [x] Add focused tests for `parseHash`/`formatHash`, including malformed encodings and request/run deep links.
- [x] Run `pnpm --dir web test`, lint, type-check, and build (2026-07-18: Phase 3 review follow-up replaces loose fixture routing with exact method/path/query matching, observable UI waits, semantic controls, terminal mutation-state assertions, real `Response` fixtures, and typed Open Pilot state responses; 19 focused App/navigation tests passed).

### Task 6: Compatibility lifecycle

- [x] Document three states: supported compatibility ingress, deprecated external backend selection, and removal prerequisites (2026-07-18).
- [x] Keep Ollama/OpenAI-compatible ingress supported; mark `MODEL_BROKER_BACKEND=llama-line` and `OPENCODE_EXECUTOR_BACKEND=ocq` as deprecated cutover backend selectors.
- [x] Add valid non-breaking warning metadata on synchronous requests actually routed through deprecated external backends; record events/status metadata for asynchronous workflow execution where no HTTP response exists (2026-07-18).
- [x] Add exact regression coverage for all deprecated generation routes, async persistence/recovery, workflow decisions, Store atomicity, and selector metadata (2026-07-19: 329 race-tested control-plane cases; JSON/SSE native, Ollama, llama-line, nested OCQ and non-generation matrix; async restart/retry; generic/Devlog/Open Pilot/AI-review success/error/no-attempt cases; memory/PostgreSQL atomic decision and terminal-step closure contracts; Oracle verdict GO).
- [x] Update `.env.example`, README, and compatibility reference without changing existing defaults unexpectedly.

### Task 7: Final qualification and atlas refresh

- [x] Run `make verify` (2026-07-19: passed with 329 backend control-plane cases, 19 frontend tests, builds, reproducibility, Compose, and template checks).
- [x] Run `make verify-production` (2026-07-19: full local gate passed, including 600-request soak, isolated PostgreSQL, security audits, Docker smoke, and three checksum-verified attested OCI archives).
- [x] Run cartographer `changes`, update affected folder maps, then `update` (2026-07-19: seven affected codemaps refreshed; state updated to 115 files; final `changes` reports none).
- [x] Update `docs/production-remediation-plan.md` with dated gate evidence and remaining real-runner/staging prerequisites; add `docs/development-readiness-report-2026-07-19.md`.
- [x] Review `git diff` for user-change preservation, secrets, generated artifacts, and accidental compatibility removals (2026-07-19: no secret-pattern matches or diff whitespace errors; pre-existing `.env.example`/Compose timeout and telemetry edits retained; generated release/build outputs remain ignored; compatibility ingress retained; final Oracle verdict GO).

## Self-review

- Spec coverage: all five requested priorities are represented by Tasks 1–6; Task 7 owns qualification and documentation consistency.
- Scope control: compatibility backends are not removed because doing so would be a breaking migration; this plan creates measurable deprecation prerequisites instead.
- Type consistency: all refactors remain in package `controlplane`, so existing unexported symbols and tests retain access while files become navigable.
