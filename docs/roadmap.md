# Switchyard Roadmap

Switchyard is now the monorepo for the control plane, imported services, and first-party workflows. Blacktower remains a separate optional operator environment.

The next major phase is to build **Switchyard Runtime**: a small, durable workflow orchestrator inspired by the useful parts of Blacktower, but scoped to Switchyard workflows instead of cloning Blacktower's full plugin, skill, and operator system.

## North star

Switchyard should run first-party automation without requiring Blacktower to be installed or kept in sync.

```txt
Switchyard
  control plane
  scheduler
  durable runs
  step/pipeline runtime
  model executor -> services/llama-line
  opencode executor -> services/ocq
  workflows -> Open Pilot / Devlog / Changemaker

Blacktower
  optional external operator environment
  OpenCode ergonomics
  skills/plugins/install/diagnostics
```

## Product principles

1. **Switchyard must work without Blacktower.** Blacktower may enrich operator sessions, but it cannot be required for core workflow execution.
2. **Runtime is workflow-scoped.** Switchyard gets durable runs, steps, executors, artifacts, model assignment, retry, and cancellation. It does not get a full skill/plugin marketplace.
3. **Imported services keep their ownership.** `services/llama-line` owns model routing and queues. `services/ocq` owns OpenCode session mechanics. `workflows/changemaker` owns Changemaker content policy.
4. **UI talks to Switchyard only.** The dashboard should not become a pile of direct service consoles.
5. **Linear first, DAG later.** Start with ordered step pipelines. Add branching only after real workflow pressure demands it.

## What the built-in mini-Blacktower includes

Call this **Switchyard Runtime**, not Blacktower.

Included:

- durable run steps
- linear pipeline definitions
- executor interface
- model assignment per workflow/step
- OCQ/OpenCode executor
- llama-line model executor
- HTTP executor for service workflows
- artifact and log capture
- retry and cancellation propagation
- session/request correlation IDs
- simple subagent records attached to workflow steps

Excluded:

- Blacktower skill installation
- OpenCode plugin management
- agent marketplace
- full operator desktop/tmux environment
- arbitrary plugin hooks
- generalized autonomous daemon swarms
- complex DAG engine in v1
- direct model provider routing that bypasses llama-line

## Phase 0 — stabilize the current control plane

Status: mostly complete.

Already done:

- Postgres-backed runs, triggers, events, artifacts.
- Scheduler with enabled cron/poll triggers.
- API-backed HUD dashboard.
- Imported `services/ocq`, `services/llama-line`, and `workflows/changemaker`.
- Optional probes for llama-line, OCQ, Blacktower, and Changemaker.
- Open Pilot CLI/API contract.
- Devlog Markdown artifact workflow.
- Best-effort cancellation adapters.

Exit criteria before Phase 1:

- `make verify` stays green.
- `make switchyard-smoke` stays green.
- Docker stack remains runnable with Blacktower absent.
- Current adapter env vars are documented and stable.

## Phase 1 — durable step runtime

Goal: turn a run from one opaque executor call into an inspectable series of steps.

Build:

- `RunStep` domain model and Postgres table.
- Step statuses: `queued`, `running`, `succeeded`, `failed`, `canceled`, `timed_out`.
- Step fields: run ID, name, executor, model, inputs, output summary, error, attempt count, timestamps, correlation IDs.
- Store methods for creating, starting, finishing, retrying, and listing steps.
- API endpoints:
  - `GET /api/runs/{id}/steps`
  - `POST /api/runs/{id}/steps/{step_id}/retry`
  - `POST /api/runs/{id}/steps/{step_id}/cancel`
- HUD run-detail step timeline.

First target:

```txt
manual-devlog
  step 1: collect recent run history
  step 2: generate markdown locally or through model executor
  step 3: attach markdown artifact
```

Exit criteria:

- Devlog runs through `RunStep` records.
- Step events and artifacts are visible in the HUD.
- Failed step retry works without retrying the whole run.

## Phase 2 — model executor through llama-line

Goal: remove one-off LLM plumbing from workflow code and route model work through a shared executor.

Build:

- `model` executor type.
- Configurable model per workflow and per step.
- Safe API override for model selection.
- llama-line request ID capture.
- Response artifact capture.
- Timeout/retry policy.
- Cancellation via llama-line queue drop where possible.

First target:

```txt
devlog.generate
  executor: model
  model: DEVLOG_MODEL or workflow config
  route: services/llama-line /v1/chat/completions
```

Exit criteria:

- Devlog uses model executor instead of bespoke code in `executor.go`.
- Model choice is visible in run-step detail.
- Request IDs are stored for cancellation/search.

## Phase 3 — OCQ/OpenCode executor

Goal: make OpenCode work a first-class Switchyard step while keeping OCQ as the session gateway.

Build:

- `opencode` executor type.
- Prompt input contract.
- OCQ session selection/creation.
- Store `ocq_session_id` and `ocq_request_id` on step metadata.
- Capture response as artifact/event.
- Propagate cancellation to OCQ abort endpoints.
- Optional session pinning per workflow/run.

First target:

```txt
open-pilot.agent-work
  executor: opencode
  session: repo/issue scoped
  model: assigned through llama-line/OCQ path
```

Exit criteria:

- Switchyard can run an OpenCode prompt through OCQ without Blacktower.
- HUD shows OCQ session/request correlation.
- Cancel aborts the active OCQ request when possible.

## Phase 4 — workflow pipelines

Goal: move first-party workflows from ad hoc executors into declarative or typed linear pipelines.

Pipeline shape:

```yaml
id: manual-devlog
steps:
  - id: collect-history
    executor: internal
  - id: generate-markdown
    executor: model
  - id: attach-artifact
    executor: internal
```

Build:

- Pipeline registry.
- Linear pipeline runner.
- Workflow-level defaults for model, timeout, retry policy, and artifact retention.
- Step input/output interpolation.
- UI surface for pipeline definition and current step.

Migration order:

1. Devlog pipeline.
2. Changemaker wrapper pipeline.
3. Open Pilot issue pipeline.

Exit criteria:

- At least Devlog and Changemaker run as pipelines.
- Open Pilot has a visible prototype pipeline, even if some steps remain policy stubs.

## Phase 5 — Open Pilot v1

Goal: make Open Pilot a Switchyard-native workflow that can process one issue end-to-end without Blacktower.

Build:

- Gitea adapter for issue fetch, labels, comments, branches, and PR creation.
- Open Pilot issue eligibility check from existing template/labels.
- Prompt builder from issue body, acceptance criteria, and test command.
- OCQ/OpenCode step for implementation.
- Verification step that runs allowed test command.
- PR/artifact summary step.
- Safe abort/cleanup behavior.

Initial sequence:

```txt
issue.fetch
issue.validate
prompt.prepare
agent.implement
tests.run
pr.prepare
summary.attach
```

Exit criteria:

- One manually selected issue can become a Switchyard run.
- The run has step history, artifacts, and final operator summary.
- Blacktower is not required.

## Phase 6 — runtime hardening

Goal: make Switchyard Runtime reliable enough for unattended scheduled workflows.

Build:

- Worker pool with per-executor concurrency limits.
- Safe shutdown and resume behavior.
- Retry policies with backoff.
- Timeouts per step.
- Artifact retention policy.
- Structured logs/search.
- Permission model for shell/test commands.
- API docs/OpenAPI.
- Dashboard improvements for step logs, artifacts, retries, and cancellation propagation.

Exit criteria:

- Multiple scheduled workflows can run without blocking the API.
- Failed runs are diagnosable from the UI.
- Retrying a failed step is safe and predictable.

## Phase 7 — optional Blacktower integration

Goal: let Blacktower enhance Switchyard without being required.

Build only after Switchyard Runtime is useful on its own:

- Blacktower status probe improvements.
- Optional handoff from Switchyard run to Blacktower operator session.
- Optional import of Blacktower agent/skill metadata for display only.
- No hard dependency on Blacktower config, plugins, or install state.

Exit criteria:

- Switchyard works the same when `blacktower/` is absent.
- When Blacktower is present, it adds richer operator ergonomics but does not own Switchyard run lifecycle.

## Milestones

### M1: Reliable Control Plane

- Runs/triggers/events/artifacts stable.
- Scheduler stable.
- Retry/cancel endpoints usable.
- Docker/local dev stable.

### M2: Step Runtime

- `RunStep` persisted.
- Devlog implemented as steps.
- Step timeline in HUD.

### M3: Shared Model Executor

- llama-line-backed model steps.
- Configurable model assignment.
- Devlog uses shared executor.

### M4: Shared OCQ Executor

- OpenCode prompt/session steps.
- OCQ session/request IDs persisted.
- Cancellation propagation works.

### M5: Native Open Pilot Prototype

- One issue to one visible multi-step run.
- Gitea policy remains workflow-owned.
- No Blacktower requirement.

### M6: Production-Ready Runtime

- Worker pool.
- Resume/shutdown semantics.
- Retry policies.
- Artifact retention.
- API docs and better HUD diagnostics.

## Near-term implementation backlog

1. Add `RunStep` schema and Store methods.
2. Add step timeline to run detail API/UI.
3. Convert Devlog into a step pipeline.
4. Extract current Devlog LLM call into `model` executor.
5. Add workflow model config and API override.
6. Add OCQ executor for prompt/session steps.
7. Prototype Open Pilot issue pipeline.
8. Add worker pool and safe shutdown after long-running steps exist.

## Non-goals for the next phase

- No Blacktower plugin/skill installer inside Switchyard.
- No full DAG engine.
- No direct provider routing around llama-line.
- No broad shell executor without a permissions model.
- No requirement that Blacktower be checked out or running.
