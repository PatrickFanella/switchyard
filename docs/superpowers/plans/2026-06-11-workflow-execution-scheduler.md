# Workflow Execution Scheduler Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Turn Switchyard from an observable dashboard into a control plane that can dispatch real external workflows and schedule enabled triggers.

**Architecture:** Keep Switchyard as the control plane. Workflow executors are adapter clients selected by workflow ID/kind, with safe no-op fallbacks when external services are not configured. The scheduler owns trigger timing and uses the same runner path as manual dispatch so run history and events stay consistent.

**Tech Stack:** Go `net/http`, embedded Postgres migrations via `database/sql`/pgx, Vite React TypeScript HUD UI, existing Switchyard Store/Runner/API contracts.

---

## File Structure

- `backend/internal/controlplane/executor.go`: workflow executor interfaces and env-backed executor clients.
- `backend/internal/controlplane/runner.go`: route runs through executors and record events/summaries.
- `backend/internal/controlplane/scheduler.go`: lightweight in-process cron/manual scheduler tick loop.
- `backend/internal/controlplane/store.go`: add trigger update and run transition methods to the Store interface and MemoryStore.
- `backend/internal/controlplane/postgres_store.go`: implement new Store methods for Postgres.
- `backend/internal/controlplane/migrations/002_trigger_scheduler.sql`: add scheduler metadata fields.
- `backend/internal/controlplane/http.go`: expose trigger update, retry, cancel, scheduler status endpoints.
- `backend/cmd/app/main.go`: wire env executors and start scheduler when enabled.
- `web/src/App.tsx`: expose enable/disable, retry/cancel, and scheduler status controls.
- `README.md`, `.env.example`, `docker-compose.yml`: document/run new env knobs.

---

### Task 1: Executor Contracts and Changemaker Dispatch

**Files:**
- Create: `backend/internal/controlplane/executor.go`
- Modify: `backend/internal/controlplane/runner.go`
- Test: `backend/internal/controlplane/runner_test.go`

- [ ] Add `WorkflowExecutor` and `ExecutorRegistry` interfaces.
- [ ] Add `EnvWorkflowExecutor` that dispatches Changemaker when `CHANGEMAKER_BASE_URL` is set:
  - `workflow_id=changemaker` → `POST /api/runs` with bearer `CHANGEMAKER_ADMIN_TOKEN` when present.
  - body: `{"kind":"daily"}` by default, or `{"kind":"manual","since":"...","until":"..."}` when run inputs include date bounds.
- [ ] Keep Open Pilot and Devlog as explicit recorded stubs with events stating they are not wired yet.
- [ ] Test success and failure behavior with `httptest`.
- [ ] Run `cd backend && go test ./internal/controlplane` and commit.

### Task 2: Trigger Scheduler Metadata and Store Updates

**Files:**
- Create: `backend/internal/controlplane/migrations/002_trigger_scheduler.sql`
- Modify: `backend/internal/controlplane/types.go`
- Modify: `backend/internal/controlplane/store.go`
- Modify: `backend/internal/controlplane/postgres_store.go`
- Test: `backend/internal/controlplane/store_test.go`, `backend/internal/controlplane/migrate_test.go`

- [ ] Add trigger fields `last_run_at`, `updated_at`, and `last_error`.
- [ ] Add Store methods `SetTriggerEnabled`, `UpdateTriggerScheduleState`, and `RunsByTrigger` if needed.
- [ ] Preserve backward-compatible JSON names.
- [ ] Test MemoryStore update behavior.
- [ ] Ensure migration text is covered by migration tests.
- [ ] Run backend tests and commit.

### Task 3: Scheduler Loop

**Files:**
- Create: `backend/internal/controlplane/scheduler.go`
- Modify: `backend/cmd/app/main.go`
- Test: `backend/internal/controlplane/scheduler_test.go`

- [ ] Implement cron schedule parsing for simple five-field expressions used by seeded triggers: `0 9 * * *` and `*/10 * * * *`.
- [ ] Add scheduler tick interval controlled by `SWITCHYARD_SCHEDULER_ENABLED` and `SWITCHYARD_SCHEDULER_INTERVAL`.
- [ ] Scheduler should dispatch due enabled cron/poll triggers via `Runner.RunTrigger`.
- [ ] Update `last_run_at`, `next_run_at`, and `last_error` after dispatch attempts.
- [ ] Test due/not-due behavior with fake clock.
- [ ] Run backend tests and commit.

### Task 4: Operator API Controls

**Files:**
- Modify: `backend/internal/controlplane/http.go`
- Test: `backend/internal/controlplane/http_test.go`

- [ ] Add `PATCH /api/triggers/{id}` with body `{"enabled":true}` or `{"enabled":false}`.
- [ ] Add `POST /api/runs/{id}/retry` that creates a new run with the same workflow/trigger/inputs.
- [ ] Add `POST /api/runs/{id}/cancel` that marks queued/running runs canceled. Do not attempt external cancellation yet; record that limitation in an event.
- [ ] Add `GET /api/scheduler` returning scheduler enabled/status metadata.
- [ ] Test endpoint status codes and state changes.
- [ ] Run backend tests and commit.

### Task 5: HUD Control Wiring

**Files:**
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [ ] Show trigger enabled state, next run, last run, and last error.
- [ ] Add enable/disable buttons using `PATCH /api/triggers/{id}`.
- [ ] Add retry/cancel buttons in run detail where applicable.
- [ ] Show scheduler status panel.
- [ ] Preserve null guards and no-dependency React style.
- [ ] Run `make web-format`, `make web-test`, `make web-build`, and commit.

### Task 6: Full Verification

**Files:**
- No code changes unless checks fail.

- [ ] Run `make verify`.
- [ ] Run `make switchyard-smoke`.
- [ ] Remove `web/dist` and restore `backend/app` if generated.
- [ ] Commit final docs or smoke fixes.

---

## Self-Review

- Spec coverage: execution adapters, scheduling, operator controls, and UI wiring are represented.
- Placeholder scan: Open Pilot and Devlog remain intentionally explicit stubs because no stable executable contract exists in the repo yet.
- Type consistency: new trigger metadata must be added consistently across memory, Postgres, API, and UI.
