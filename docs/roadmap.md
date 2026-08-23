# Switchyard Roadmap

Switchyard is the monorepo for one cohesive automation runtime. OCQ, llama-line, and Changemaker are historical references whose useful ideas have been rebuilt as native Switchyard modules; the imported directories were removed.

Switchyard Runtime phases 1–6 are implemented: durable linear workflows, native model brokering, native OpenCode execution, first-party workflow policy, and release qualification exist. Scheduled disposable restore-drill evidence, deadline telemetry, and external alerts are implemented. The active phase is to make autonomous execution controllable and constrained and finish selected operator surfaces.

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

Status: complete; ongoing compatibility and operational maintenance remains.

Already done:

- Postgres-backed runs, triggers, events, artifacts.
- Scheduler with enabled cron/poll triggers.
- API-backed HUD dashboard.
- Imported OCQ, llama-line, and Changemaker as transitional reference implementations; those directories are now removed.
- Probes for compatibility backends, external/reference Blacktower when configured, and native workflow policy.
- Native llama-line/Ollama/OpenAI-compatible ingress on `LLAMA_LINE_PORT`.
- Durable model-broker request ledger, history/search/log/config compatibility endpoints, admin queue/inflight aliases, and OCQ session/request/settings compatibility views.
- Phoenix OTLP tracing for model-broker request spans.
- Open Pilot CLI/API contract.
- Devlog Markdown artifact workflow.
- Best-effort cancellation adapters.

Current hardening track:

- Production runbooks and smoke checks for the live `11434` cutover.
- Observability UI backed by the durable broker ledger and Phoenix trace correlation.
- Model-broker reliability policy for bounded retries, clearer degradation, and safer async recovery.
- Feature-tab UI redesign, starting with Queues/Requests.

Exit criteria before Phase 1:

- `make verify` stays green.
- `make switchyard-smoke` stays green.
- Docker stack remains runnable without any external Blacktower checkout.
- Current adapter env vars are documented and stable.

## Phase 1 — durable step runtime

Status: complete.

Goal: turn a run from one opaque executor call into an inspectable series of steps.

Build:

- `RunStep` domain model and Postgres table.
- Step statuses: `queued`, `running`, `succeeded`, `failed`, `canceled`, `timed_out`, `paused`.
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

Status: complete.

Goal: remove one-off LLM plumbing from workflow code and route model work through a Switchyard-native `ModelBroker` contract while llama-line-compatible ingress remains available for cutover.

Build:

- `ModelBroker` interface as the native model-generation seam.
- `model` executor type backed by the broker.
- Configurable model per workflow and per step.
- Safe API override for model selection.
- compatibility request ID capture through the cutover backend.
- Response artifact capture.
- Timeout/retry policy.
- Cancellation via queue-drop compatibility where possible.

First target:

```txt
devlog.generate
  executor: model
  model: DEVLOG_MODEL or workflow config
  route: llama-line-compatible ingress on LLAMA_LINE_PORT -> ModelBroker -> OpenCode or upstream Ollama
```

Exit criteria:

- Devlog uses the native `ModelBroker` path instead of bespoke code in `executor.go`.
- Model choice is visible in run-step detail.
- Request IDs are stored for cancellation/search.

Current status:

- `backend/internal/controlplane/model_executor.go` defines the native `ModelBroker` contract.
- `EnvModelBroker` handles compatibility ingress requests and marks responses with `model_broker=switchyard` correlation metadata.
- Native routing sends provider-style models to OpenCode and Ollama/local models to `OLLAMA_BASE_URL` when configured; `ollama/<model>` forces Ollama routing.
- `GET /api/model-broker/status` exposes a native Switchyard broker status surface; `/broker/status` mirrors that status on the compatibility ingress.

## Phase 3 — OpenCode executor

Status: complete. Native sessions are correlated before dispatch and operator
cancellation propagates to authenticated OpenCode abort.

Goal: make OpenCode work a first-class Switchyard step while keeping compatibility correlation available for cutover only.

Build:

- `opencode` executor type.
- Prompt input contract.
- OpenCode session selection/creation.
- Store compatibility session/request IDs on step metadata when present.
- Capture response as artifact/event.
- Propagate cancellation to compatibility abort endpoints when present.
- Optional session pinning per workflow/run.

First target:

```txt
open-pilot.agent-work
  executor: opencode
  session: repo/issue scoped
  model: assigned through native routing
```

Exit criteria:

- Switchyard can run an OpenCode prompt through the native executor using native runtime profiles.
- HUD shows compatibility session/request correlation only when a cutover run still has it.
- Cancel aborts the active request when possible.

Current status:

- `backend/internal/controlplane/ocq_executor.go` defines the native `OpenCodeExecutor` contract.
- `EnvOpenCodeExecutor` routes prompt/session execution through the native executor and preserves historical request/session IDs only for cancellation compatibility when they exist.
- Open Pilot `agent-work` runs through the `opencode` pipeline step and persists OpenCode request/session correlation IDs on run steps.
- `GET /api/opencode-executor/status` exposes a native Switchyard OpenCode executor status surface.

## Phase 4 — workflow pipelines

Status: complete for safe linear pipelines.

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

Status: implemented behind deny-by-default mutation gates; unrestricted production autonomy is not the default.

Goal: make Open Pilot a Switchyard-native workflow that can process one issue end-to-end using native Blacktower profiles when useful.

Build:

- Gitea adapter for issue fetch, labels, comments, branches, and PR creation.
- Open Pilot issue eligibility check from existing template/labels.
- Prompt builder from issue body, acceptance criteria, and test command.
- OpenCode step for implementation.
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

Status: code-complete and deployed at `v0.1.2`. Production backup, isolated
restore, monitoring scrape/rules, and live application rollback are proven for
that release. A newer provider-neutral restore-evidence contract now requires
manifest-last policy-bound bundles and a disposable PostgreSQL adapter; old
evidence does not qualify the new contract.
Acknowledged alert delivery and encrypted proxy transport remain operational
prerequisites.

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

Status: complete for the static v1 profile layer. Profiles are enforced during
definition validation and execution, `content-writer` is present, prompt
contracts/default models are applied, and effective policy is visible in run
evidence and the native operator UI.

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

- Switchyard-native `ModelBroker` contract is available with compatibility ingress on `LLAMA_LINE_PORT`.
- Configurable model assignment is available through `DEVLOG_MODEL` for Devlog.
- Devlog uses the shared broker path for model-backed generation.

### M4: OpenCode Executor

- OpenCode prompt/session executor is available.
- Historical compatibility request/session IDs are persisted on Open Pilot agent steps when present.
- Cancellation propagation works when historical request/session IDs are present.

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

1. Assign a human receiver/escalation owner for the active NTFY route, then
   record a delivered, acknowledged, and resolved Switchyard test alert.
2. Replace the Almaz-to-Kvant plaintext dashboard hop with the approved
   encrypted Tailscale path after the central policy permits Almaz
   (`100.77.128.80`) to reach Kvant (`100.65.164.66`) on TCP/28081, while
   preserving the control/model listener split.
3. Observe durable deprecated-selector usage for 90 days and require two
   qualifying zero-use releases before selector removal.
4. Measure retained inline-artifact growth before funding an object-storage
   backend.
5. Keep `docs/openapi.yaml`, codemaps, current release evidence, and
   environment defaults aligned with implementation.
6. Obtain live timer evidence for the bounded restore-drill scheduler under
   Gitea #24. The implementation and automated tests are verified; no live
   timer execution is claimed.

## Non-goals for the next phase

- No broad plugin/skill installer inside Switchyard in this phase.
- No full DAG engine.
- No direct provider routing around compatibility ingress.
- No broad shell executor without a permissions model.
- No requirement that external Blacktower be checked out or running.
