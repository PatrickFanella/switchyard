# ADR 0006: Retain linear workflows; define the limit for bounded branching

## Status

Accepted (defer) — 2026-09-24. Resolves Gitea issue #78. The owner named
below may revisit it when the revisit trigger is met.

## Context

The roadmap principle is "Linear first, DAG later", and issue #6 lists "No DAG
engine" as a non-goal. Issue #78 asks whether any concrete workflow now needs
conditions, parallel steps, or a general DAG, and what semantics a limited
extension would have. Its dependency, durable named step outputs (#66), is on
`main`: a step publishes bounded string outputs and later steps bind them with
`{{steps.<step_id>.outputs.<name>}}` (`docs/reference/step-outputs.md`).

This record was written against `main` at
`9a531064b9c93d110ce262345df9e79f67273980`.

### What the linear runtime provides today

- A pipeline is an ordered list of steps (`PipelineDefinition.Steps`, each
  with a unique `position`) captured in an immutable run snapshot with a
  `pipeline_version` hash.
- Step states are `queued`, `running`, `succeeded`, `failed`, `canceled`,
  `timed_out`, and `paused`. A deterministic failure pauses the run.
- Same-run retry after a failure, panic, or expired lease skips succeeded steps
  and reloads their outputs. Operator resume
  (`POST /api/runs/{id}/steps/{step}/retry`) starts a new run at the chosen
  step and restores earlier outputs only when `pipeline_version` matches.
- Cancellation propagates to the active executor, including authenticated
  OpenCode abort.
- Open Pilot adds bounded follow-up runs: a CI failure can hand off to a CI
  repair child run (at most two repair attempts by default,
  `openPilotDefaultMaxRepairAttempts`), and a rejected AI review ends the run
  with a resume cursor at `agent-work` and the review feedback, counting
  against the same attempt limit and pausing when it is reached.
- Default concurrency is one worker (`SWITCHYARD_WORKER_COUNT=1`), one model
  broker queue worker (`MODEL_BROKER_QUEUE_WORKERS=1`), and one verifier slot
  (`SWITCHYARD_VERIFIER_CONCURRENCY=1`) in `.env.example`.

### Candidate workflows

| Workflow | Shape it needs | Expressible with linear steps and named outputs? |
| --- | --- | --- |
| Open Pilot (`backend/internal/controlplane/pipeline.go`, 12 steps) | Validate, plan, prompt, agent work, verify, commit, push, PR, AI review, CI, merge, close. Mode gates (dry-run or allow) are decided inside each step and recorded as evidence | Yes. Its loops (review rejection, CI repair) are cycles, which a DAG cannot express either; they are handled by bounded follow-up runs |
| Devlog (3 steps) | Collect history, generate, attach | Yes |
| Changemaker daily digest (`registry.go`, 3 steps) | Prepare, draft, attach | Yes |
| #82 Grounded Changemaker drafts | Collect sources, draft from them, attach sources and draft. Empty or partial collection must be deterministic | Yes. The issue names one first source adapter. The collector publishes every output name on success, with an explicit empty or partial marker, so the draft step always resolves its references |
| #83 Devlog repository, PR, and release evidence | Collect commits, PRs, and releases for an allowlisted window, deduplicate, generate | Yes. This is the strongest fan-out candidate. Three bounded collection steps (or one collector step) run in sequence. With one worker and one broker slot, parallel collection would save at most the sum of the shorter collection times; no measurement shows that time matters |
| #86 Reviewed Devlog export and delivery | Preview, approval, deliver once, persist receipt | Yes. Approval is a pause point followed by operator resume; rejection ends the run. A generic approval step does not exist yet and belongs to #86, within the linear model. Idempotent delivery is an adapter contract, not a graph property |
| #88 Changemaker review, revision, publication | Review, request revision, redraft, approve, publish | Yes, as a sequence of runs. Revision is a loop, so a DAG does not help. Each revision is a new run or resume bound to the exact draft, as Open Pilot already does after a rejected review |

No candidate needs branching or parallelism to meet its acceptance criteria.
The recurring pressure is loops (review and repair), which neither option
below solves, and multi-source collection, where sequential steps are adequate
at current concurrency.

### Options compared

**A. Keep linear pipelines.** Conditional behavior stays inside a step and is
recorded as that step's evidence. Loops stay as bounded follow-up runs. No
schema, runner, or UI change.

**B. Limited conditions and parallel groups.** This option was specified so a
future proposal has a fixed starting point:

- *Condition.* A step may declare `when`: equality or inequality between one
  earlier step's named output and a literal. A false condition records the
  step as a new terminal state `skipped` with the evaluated value. Skipped
  steps publish no outputs. Definition validation rejects a reference to a
  conditional step's output unless the consumer declares the same condition.
  Conditions are evaluated once; retry and resume reuse the recorded result.
- *Parallel group.* Adjacent steps may share a `group` id. Members may
  reference only steps before the group, never each other. Width is capped
  (for example 4). At most one member may use a mutating executor (`opencode`,
  `git`, `gitea`, or `command` in allow mode), so a worktree or pull request
  never has two concurrent writers. Members run within the owning run's lease
  and existing per-executor limits.
- *Join.* An all-succeed barrier. The step after the group starts only when
  every member is `succeeded` or `skipped`.
- *Failure.* The first member failure cancels running siblings through the
  existing cancel path, records them `canceled`, and pauses the run as a
  linear failure does. There is no partial-success join.
- *Retry.* Same-run retry reruns only members that did not succeed; succeeded
  members keep their persisted outputs. Retry classification is per member.
- *Cancellation.* Canceling the run cancels every running member; canceling a
  member cancels the group and pauses the run.
- *Resume.* The resume cursor is a whole group, never a single member. The new
  run restores outputs of succeeded members under the existing
  `pipeline_version` rule and reruns the rest.
- *Compatibility.* Pipelines without `when` or `group` keep their current
  snapshot, hash, and behavior.

**C. General DAG.** Arbitrary `depends_on` edges, multiple ready cursors,
partial-success joins, and per-node scheduling. It needs cycle detection, a
multi-cursor resume model, graph-aware cancellation, per-edge output
visibility, and a graph UI. It still does not express loops. It is also the
engine #6 excludes.

B costs a schema change, a runner change for groups, one new step state,
store support for group resume, and UI for grouped steps. C costs all of that
plus a scheduler. Neither is justified by the candidates above.

## Decision

Defer. Retain linear pipelines (option A). Option B is the only extension that
may be proposed next; option C remains out of scope.

- **Owner:** the repository owner, the same role that holds external
  monitoring and CI-runner responsibility under issue #25.
- **Revisit trigger:** an accepted workflow issue whose acceptance criteria
  cannot be met with linear steps, named outputs, pause and resume, and
  bounded follow-up runs; or measured run evidence that sequential steps
  exceed an operator-set wall-time budget after worker or broker concurrency
  has been raised above one.
- **If proceeding:** implement option B only, split into separately scoped
  issues (definition schema and validation, runner and join, store and resume,
  cancellation, UI, documentation). Linear pipelines stay valid unchanged, and
  repository admission, immutable-run identity, executor isolation, command
  allowlists, listener boundaries, and CI and merge gates stay as they are.

## Consequences

- #82, #83, #86, and #88 are designed as linear pipelines, with approval and
  revision handled by pause, resume, and follow-up runs.
- Multi-source collection runs sequentially. Its wall time is the sum of its
  collection steps.
- The semantics above are a design record, not an implementation or a
  commitment to build it.
