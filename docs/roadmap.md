# Switchyard Roadmap

> **How to read this document.** Reconciled against `main` on 2026-09-23
> (Gitea issue #65). The phase and milestone sections below are historical
> planning records: their "Build" lists are the original scope, and their
> "Status" lines were updated during reconciliation. Current capability state,
> evidence levels, release pointers, and the observed state of every cited
> tracker issue live in `docs/status.md`; where this document and that page
> disagree, `docs/status.md` wins. The only current work list is "Open work"
> near the end.

Switchyard is the monorepo for one cohesive automation runtime. OCQ, llama-line, and Changemaker are historical references whose useful ideas have been rebuilt as native Switchyard modules; the imported directories were removed.

Switchyard Runtime phases 1–7 are implemented: durable linear workflows, native model brokering, native OpenCode execution, first-party workflow policy, release qualification tooling, and the static profile layer exist. Scheduled disposable restore-drill evidence, deadline telemetry, and external alerts are implemented. The last qualified release is `v0.1.2`; later tags and `main` are deployed without a qualification record, and qualifying the current candidate is Gitea issue #14. The active phase is to keep autonomous execution controllable and constrained and finish the remaining operator surfaces listed in `docs/ui-feature-migration.md`.

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
5. **Linear first, DAG later.** Start with ordered step pipelines. Add branching only after real workflow pressure demands it. The current decision and its revisit trigger are in `docs/adr/0006-linear-workflows-before-branching.md`.

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

Hardening track (historical list; each item now has a home on `main`):

- Production runbooks and smoke checks for the live `11434` cutover: `docs/runbooks/native-model-broker-cutover.md`, `scripts/smoke-model-broker.sh`.
- Observability UI backed by the durable broker ledger: `web/src/components/ObservabilityPage.tsx` over `/api/observability/model-broker`.
- Model-broker reliability policy for bounded retries, clearer degradation, and safer async recovery: `backend/internal/controlplane/model_service.go`; plan record `docs/superpowers/plans/2026-06-17-model-broker-reliability.md`.
- Feature-tab UI redesign, starting with Queues/Requests: `web/src/components/QueuesPage.tsx`; plan record `docs/superpowers/plans/2026-06-17-ui-queues-requests-sprint.md`.

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

- `backend/internal/controlplane/model_executor.go` defines the narrow native `ModelGeneration` contract, and `model_service.go` owns durable queue admission, recovery, cancellation, dropping, and execution.
- `EnvModelBroker` handles compatibility ingress requests and marks responses with `model_broker=switchyard` correlation metadata.
- Native routing sends provider-style models to OpenCode and Ollama/local models to `OLLAMA_BASE_URL` when configured; `ollama/<model>` forces Ollama routing.
- Authenticated `/api/model-broker/status`, `/queue`, and `/requests` routes expose separate upstream and queue health plus bounded lifecycle controls. These routes are unavailable on model ingress; `/broker/status` preserves the compatibility envelope.

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

Status: code-complete and qualified at `v0.1.2`. Production backup, isolated
restore, monitoring scrape/rules, and live application rollback are proven for
that release only. A newer provider-neutral restore-evidence contract now
requires manifest-last policy-bound bundles and a disposable PostgreSQL
adapter; old evidence does not qualify the new contract.
Acknowledged alert delivery (#16, closed) and encrypted proxy transport (#18
and #19, closed with smoke evidence on revision
`9521e5439870e5661a4fa197e30ae8febc66aa24`) are in place for that revision;
each later candidate must re-prove them under #14.

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

## Completed foundations

Closed tracker issues whose code or evidence is on `main`. Each closure is
bound to the revision its issue names; none qualifies a later release. The
full table with what each one does not prove is in `docs/status.md`.

- #13 release evidence validator; #17 static production configuration contract.
- #16 alert owner and acknowledgement drill; #25 monitoring and CI-runner responsibility.
- #18 encrypted Almaz-to-Kvant path; #19 encrypted proxy production smoke.
- #24 scheduled restore-drill evidence capture.
- #28 inline-artifact storage decision (`docs/adr/0004-inline-artifact-storage-threshold.md`); #29 deprecated-selector retirement gate; #30 documentation drift checks.
- #61 reconciliation of the production Open Pilot branch with `main`.

## Open work

Reconciled 2026-09-23. Items that were on this list before and are now closed
appear above; the earlier list is preserved in Git history.

1. Qualify the current `main` candidate and publish its release record (#14,
   open, human-run on the production host). Its former blockers #13, #16, #18
   and #19 are closed. Until this lands, `docs/status.md` keeps `v0.1.2` as
   the last qualified pointer.
2. Complete the operational deprecated-selector observation: 90 days of
   durable usage data and two qualifying zero-use releases before selector
   removal. The eligibility gate itself is enforced (#29, closed); the
   observation window is production runtime, not a code change.
3. Continue measuring retained inline-artifact growth against the decision in
   `docs/adr/0004-inline-artifact-storage-threshold.md` (#28, closed). Keep
   PostgreSQL until an operator-supplied byte budget, observation window, and
   sustained threshold are exceeded; none has been.
4. Obtain live production timer evidence for the restore-drill scheduler. The
   implementation and tests landed under #24 (closed); no live timer execution
   is claimed in this repository.
5. Finish the remaining native operator surfaces listed under "Remaining
   adapter-backed surfaces" in `docs/ui-feature-migration.md` (parent #60,
   open).
6. Keep `docs/openapi.yaml`, codemaps, release pointers, tracked-issue states,
   and environment defaults aligned with implementation; `make docs-check`
   enforces the parts it can read from the repository (#30, closed, extended
   under #65).

## Recorded decisions

Deferred capability decisions from roadmap #60. Each keeps the current
boundary, names the repository owner, and states the trigger for revisiting
it. None authorizes building the deferred feature.

- Plugin and skill installation stays outside Switchyard:
  [ADR 0005](adr/0005-plugin-and-skill-installation-boundary.md) (#62).
- Workflows stay linear; limited conditions and parallel groups are specified
  but not built, and a general DAG stays out of scope:
  [ADR 0006](adr/0006-linear-workflows-before-branching.md) (#78).
- Dynamic child agents, model-chosen handoffs, and parallel reviewers are
  deferred; static profiles and bounded follow-up runs remain:
  [ADR 0007](adr/0007-bounded-multi-agent-orchestration.md) (#79).

## Non-goals for the next phase

- No broad plugin/skill installer inside Switchyard in this phase (ADR 0005).
- No full DAG engine (ADR 0006).
- No direct provider routing around compatibility ingress.
- No broad shell executor without a permissions model.
- No requirement that external Blacktower be checked out or running.
