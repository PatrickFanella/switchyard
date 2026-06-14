# Switchyard Roadmap

Switchyard is the monorepo for one cohesive automation runtime. Imported services are transitional reference implementations whose useful ideas should be rebuilt as native Switchyard modules.

The next major phase is to build **Switchyard Runtime**: a small, durable workflow orchestrator with native model brokering, native OpenCode execution, curated profiles, and first-party workflow modules instead of a pile of taped-together apps.

## North star

Switchyard should run first-party automation with its own built-in runtime, agents/subagents, model assignment, model broker, and OpenCode executor. It should learn from OCQ, llama-line, Blacktower, and Changemaker without preserving them as separate product boundaries.

```txt
Switchyard
  control plane
  scheduler
  durable runs
  step/pipeline runtime
  native model broker module
  native opencode executor module
  native profiles/personas
  workflow modules -> Open Pilot / Devlog / Changemaker-style policy
```

## Product principles

1. **Switchyard owns the runtime.** Runs, steps, queues, retries, cancellation, logs, artifacts, and operator summaries belong to Switchyard.
2. **Reference apps become native modules.** llama-line ideas become the model broker module; OCQ ideas become the OpenCode executor module; Blacktower ideas become native profiles; Changemaker ideas become workflow policy.
3. **Workflows own domain policy only.** Open Pilot owns issue/PR policy; Changemaker-style workflows own content policy; generic scheduling and execution stay in Switchyard.
4. **UI talks to Switchyard only.** The dashboard should not become a pile of direct service consoles.
5. **Linear first, DAG later.** Start with ordered step pipelines. Add branching only after real workflow pressure demands it.

## What native Blacktower includes

Blacktower should be a curated layer on top of **Switchyard Runtime**.

Included:

- durable run steps
- linear pipeline definitions
- executor interface
- model assignment per workflow/step
- native OpenCode executor inspired by OCQ
- native model broker inspired by llama-line
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
- standalone OCQ/llama-line/Changemaker/Blacktower product boundaries

## Phase 0 — stabilize the current control plane

Status: mostly complete.

Already done:

- Postgres-backed runs, triggers, events, artifacts.
- Scheduler with enabled cron/poll triggers.
- API-backed HUD dashboard.
- Imported `services/ocq`, `services/llama-line`, and `workflows/changemaker` as transitional reference implementations.
- Probes for transitional llama-line/OCQ sidecars, external/reference Blacktower when configured, and Changemaker.
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
- Failed step retry creates a resume run that starts registry-backed pipelines at the selected step; Open Pilot rehydrates branch/agent/push state from preserved resume metadata for downstream retries.

## Phase 2 — native model broker contract

Goal: remove one-off LLM plumbing from workflow code and route model work through a Switchyard-native `ModelBroker` contract while llama-line remains a transitional backend.

Build:

- `ModelBroker` interface as the native model-generation seam.
- `model` executor type backed by the broker.
- Configurable model per workflow and per step.
- Safe API override for model selection.
- llama-line request ID capture through the transitional backend.
- Response artifact capture.
- Timeout/retry policy.
- Cancellation via llama-line queue drop where possible.

First target:

```txt
devlog.generate
  executor: model
  model: DEVLOG_MODEL or workflow config
  route: ModelBroker -> transitional llama-line /v1/chat/completions
```

Exit criteria:

- Devlog uses the native `ModelBroker` path instead of bespoke code in `executor.go`.
- Model choice is visible in run-step detail.
- Request IDs are stored for cancellation/search.

Current status:

- `backend/internal/controlplane/model_executor.go` defines the native `ModelBroker` contract.
- `EnvModelBroker` routes to transitional llama-line and marks responses with `model_broker=switchyard` correlation metadata. OCQ remains the transitional OpenCode executor path and should be reached through llama-line routing when model-broker traffic targets OpenCode-backed models.
- `GET /api/model-broker/status` exposes a native Switchyard broker status surface backed by transitional llama-line `/broker/status` data.

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

Current status:

- `backend/internal/controlplane/ocq_executor.go` is the current native call seam for OpenCode prompt/session execution while OCQ remains the transitional gateway backend.
- Open Pilot `agent-work` runs through the `opencode` pipeline step and persists OCQ request/session correlation IDs on run steps.
- `GET /api/opencode-executor/status` exposes a native Switchyard OpenCode executor status surface backed by transitional OCQ health/session data.

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
- Open Pilot resume now derives its cursor from persisted DB state and records actionable failure diagnostics for verify, push, PR, CI, merge, and close failures.

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

### M3: Native ModelBroker

- Switchyard-native `ModelBroker` contract is available with env-backed llama-line transport.
- Configurable model assignment is available through `DEVLOG_MODEL` for Devlog.
- Devlog uses the shared broker path for model-backed generation.

### M4: Shared OCQ Executor

- OpenCode prompt/session executor is available.
- OCQ session/request IDs are persisted on Open Pilot agent steps.
- Cancellation propagation works when OCQ request/session IDs are present.

### M5: Native Open Pilot Prototype

- One issue to one visible multi-step run is prototyped, including branch plan/worktree creation, command-policy verification, and PR plan steps.
- Gitea policy remains workflow-owned.
- No external Blacktower requirement.

### M6: Production-Ready Runtime

- Worker pool with leases, heartbeat renewal, claimed-run finish guards, and optional per-executor concurrency limits.
- Resume/shutdown semantics.
- Retry policies.
- Artifact retention.
- API docs and better HUD diagnostics.

## Near-term implementation backlog

1. Harden full Open Pilot autonomy: richer artifact rehydration and better agent commit diagnostics. Existing-branch detection for externally published heads is implemented by probing `origin/<branch>` during the push step and allowing PR publication when that remote head already exists.
2. Keep `docs/openapi.yaml` aligned as route shapes settle and decide whether to serve it from the API/UI.

## Non-goals for the next phase

- No broad plugin/skill installer inside Switchyard in this phase.
- No full DAG engine.
- No direct provider routing around llama-line.
- No broad shell executor without a permissions model.
- No requirement that external Blacktower be checked out or running.
