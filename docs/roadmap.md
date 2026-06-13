# Switchyard Roadmap

Switchyard is now the monorepo for the control plane, imported services, first-party workflows, and the future Blacktower agent/profile layer.

The next major phase is to build **Switchyard Runtime**: a small, durable workflow orchestrator with curated Blacktower agent profiles, scoped to Switchyard workflows instead of cloning a broad plugin/skill/operator system.

## North star

Switchyard should run first-party automation with its own built-in agents/subagents and model assignment, without depending on an external Blacktower checkout.

```txt
Switchyard
  control plane
  scheduler
  durable runs
  step/pipeline runtime
  model executor -> services/llama-line
  opencode executor -> services/ocq
  workflows -> Open Pilot / Devlog / Changemaker

Blacktower profiles
  curated agents/subagents
  model assignments
  workflow roles and prompt contracts
```

## Product principles

1. **Blacktower becomes native profiles.** The name can refer to the curated built-in agent/subagent set inside Switchyard Runtime.
2. **Runtime is workflow-scoped.** Switchyard gets durable runs, steps, executors, artifacts, model assignment, retry, and cancellation. It does not need a full skill/plugin marketplace in the next phase.
3. **Imported services keep their ownership.** `services/llama-line` owns model routing and queues. `services/ocq` owns OpenCode session mechanics. `workflows/changemaker` owns Changemaker content policy.
4. **UI talks to Switchyard only.** The dashboard should not become a pile of direct service consoles.
5. **Linear first, DAG later.** Start with ordered step pipelines. Add branching only after real workflow pressure demands it.

## What native Blacktower includes

Blacktower should be a curated layer on top of **Switchyard Runtime**.

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
- curated agent profiles
- default model assignments
- workflow role prompts

Excluded:

- broad skill installation
- OpenCode plugin management
- agent marketplace
- full operator desktop/tmux environment outside workflow runs
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
- Probes for llama-line, OCQ, external/reference Blacktower when configured, and Changemaker.
- Open Pilot CLI/API contract.
- Devlog Markdown artifact workflow.
- Best-effort cancellation adapters.

Exit criteria before Phase 1:

- `make verify` stays green.
- `make switchyard-smoke` stays green.
- Docker stack remains runnable without any external Blacktower checkout.
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
- Failed step retry creates a resume run that starts registry-backed pipelines at the selected step.

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

- Switchyard can run an OpenCode prompt through OCQ using native runtime profiles.
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

- Pipeline registry. Current: static Go registry exposed through `GET /api/pipelines`.
- Linear pipeline runner. Current: registry-backed ordered runner is active for Devlog, Changemaker, and the Open Pilot prototype.
- Workflow-level defaults for model, timeout, retry policy, and artifact retention.
- Step input/output interpolation.
- UI surface for pipeline definition and current step.

Migration order:

1. Devlog pipeline.
2. Changemaker wrapper pipeline.
3. Open Pilot issue pipeline.

Exit criteria:

- At least Devlog and Changemaker run as pipelines. Current: complete.
- Open Pilot has a visible prototype pipeline with branch and PR planning artifacts, even if mutation remains policy-gated. Current: complete.

## Phase 5 — Open Pilot v1

Goal: make Open Pilot a Switchyard-native workflow that can process one issue end-to-end using native Blacktower profiles when useful.

Build:

- Gitea adapter for issue fetch, labels, comments, branches, and PR creation.
- Open Pilot issue eligibility check from existing template/labels.
- Prompt builder from issue body, acceptance criteria, and test command.
- OCQ/OpenCode step for implementation.
- Verification step that runs allowed test command.
- Push/PR/artifact summary steps.
- Safe abort/cleanup behavior.

Initial sequence:

```txt
issue.fetch
issue.validate
prompt.prepare
agent.implement
tests.run
branch.push
pr.prepare
summary.attach
```

Exit criteria:

- One manually selected issue can become a Switchyard run.
- The run has step history, artifacts, and final operator summary.
- External Blacktower is not required.

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
- Async worker dispatch can claim queued runs with durable leases.
- Startup recovery handles expired running leases.
- Failed runs are diagnosable from the UI.
- Retrying a failed step is safe and predictable.

## Phase 7 — Blacktower profile layer

Goal: turn Blacktower into a named set of built-in Switchyard agents/subagents, model defaults, and workflow roles.

Build after Switchyard Runtime has durable steps and executors:

- Agent profile registry.
- Subagent role definitions.
- Model assignment defaults by workflow/step/role.
- Prompt contracts for planner, executor, reviewer, summarizer, and operator roles.
- UI for selecting profiles and per-run model overrides.
- Optional compatibility import from an external Blacktower checkout only as a migration aid.

Exit criteria:

- Blacktower profiles are available from the native profile registry.
- Open Pilot and Devlog can select Blacktower profiles without external files.
- External Blacktower compatibility is best-effort and not part of the core runtime contract.

## Milestones

### M1: Reliable Control Plane

- Runs/triggers/events/artifacts stable.
- Scheduler stable.
- Retry/cancel endpoints usable.
- Docker/local dev stable.

### M2: Step Runtime

- `RunStep` persisted. Initial generic workflow execution step is complete.
- Step timeline in HUD is complete.
- Devlog now runs through the registry-backed three-step pipeline: collect history, generate markdown, attach artifact.

### M3: Shared Model Executor

- llama-line/OCQ-backed model executor is available.
- Configurable model assignment is available through `DEVLOG_MODEL` for Devlog.
- Devlog uses shared executor for model-backed generation.

### M4: Shared OCQ Executor

- OpenCode prompt/session executor is available.
- OCQ session/request IDs are persisted on Open Pilot agent steps.
- Cancellation propagation works when OCQ request/session IDs are present.

### M5: Native Open Pilot Prototype

- One issue to one visible multi-step run is prototyped, including branch plan/worktree creation, command-policy verification, and PR plan steps.
- Gitea policy remains workflow-owned.
- No external Blacktower requirement.

### M6: Production-Ready Runtime

- Worker pool.
- Resume/shutdown semantics.
- Retry policies.
- Artifact retention.
- API docs and better HUD diagnostics.

## Near-term implementation backlog

1. Expand Open Pilot worktree support with cleanup/status artifacts and stronger retry rehydration.
2. Add per-executor concurrency limits beyond the current worker pool.
3. Add API docs/OpenAPI after the runtime API settles.

## Non-goals for the next phase

- No broad plugin/skill installer inside Switchyard in this phase.
- No full DAG engine.
- No direct provider routing around llama-line.
- No broad shell executor without a permissions model.
- No requirement that external Blacktower be checked out or running.
