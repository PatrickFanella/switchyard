# ADR 0007: Defer dynamic multi-agent orchestration beyond static roles

## Status

Accepted (defer) — 2026-09-24. Resolves Gitea issue #79. The owner named
below may revisit it when the revisit trigger is met.

## Context

The roadmap includes "simple subagent records attached to workflow steps" and
curated profiles in native Blacktower, and excludes "generalized autonomous
daemon swarms". Issue #79 asks Switchyard to separate what exists (profile
names and step roles) from dynamic delegation (child agents, handoffs, parallel
reviewers), and to state what dynamic delegation would require. Its
dependency, versioned agent profiles (#73), is on `main`
(`docs/reference/agent-profiles.md`).

This record was written against `main` at
`9a531064b9c93d110ce262345df9e79f67273980`.

### What exists today

- **Static roles on linear steps.** Four shipped profiles in
  `backend/internal/controlplane/profiles.go`: `content-writer` (writer),
  `devlog-summarizer` (summarizer), `open-pilot-executor` (executor), and
  `operator-reviewer` (reviewer). Each step names at most one profile. The
  run snapshot pins each profile's effective policy, and each step records the
  profile ID, version, contract hash, and model.
- **One bounded parent-to-child handoff.** When required CI fails, Open Pilot
  can create a CI repair child run (`OpenPilotCIRepairRequest` with
  `ParentRunID`, the expected worker, and the expected attempt count). It runs
  only when every mutation mode is `allow`, the pull request and worktree
  identity are revalidated, and fewer than the default two repair attempts
  (`openPilotDefaultMaxRepairAttempts`) have been used. The child reuses the
  parent's branch and pull request; Switchyard, not the agent, runs
  verification, commit, and push. It is serial: one child at a time.
- **Reviewer-to-executor handoff.** A `CHANGES_REQUESTED` AI review ends the
  run with a resume cursor at `agent-work` and the review feedback, under the
  same attempt limit. An operator can also resume with up to 8192 bytes of
  correction feedback.
- **No executor-internal delegation.** The managed OpenCode configuration
  ships Markdown agents named `orchestrator`, `council`, `councillor`,
  `explorer`, `fixer`, and others, but `opencode.defaults.json` sets
  `"task": "deny"` and disables the built-in `explore` and `general` agents.
  In strict mode the executor cannot spawn subagents. Any delegation must be
  owned by Switchyard, not by the executor.

### Concrete needs examined

| Scenario | Source | Needs dynamic delegation? |
| --- | --- | --- |
| Parallel reviewers on an Open Pilot PR (for example correctness and security) | `ai-review` step, `operator-reviewer` profile | No. Two sequential review steps with different profiles express it, but the Open Pilot pipeline is Go-defined and currently reads one verdict. Adding a second reviewer needs an aggregation rule, not a child agent. No retained review evidence yet shows a single reviewer missing defects a second one would catch |
| Executor to reviewer to executor correction | Open Pilot review and CI repair | No. Already served by bounded follow-up runs |
| Planner to executor decomposition of a large issue | `orchestrator` agent file | Not now. An Open Pilot run serves one issue, and the #60 backlog issues ask for refinement into smaller PRs before agent queueing. `task` is denied, so the agent file is inert |
| Writer to editor handoff for content | #82, #88 | No. `content-writer` then a reviewer profile as linear steps; revision is a follow-up run bound to the exact draft (see ADR 0006) |
| Multi-source collection for Devlog | #83 | No. Collection is deterministic HTTP or Gitea reads, not agent work |

No current need requires a child agent created at run time, a handoff chosen by
a model, or reviewers running in parallel.

### Requirements for a future bounded design

A proposal that proceeds must define all of the following, extending the CI
repair pattern rather than replacing it:

- **Ownership.** A child is a Switchyard step or run with a durable
  `parent_run_id`, created only by a step the parent's pipeline snapshot
  declares. The parent owns the child's lifecycle; a child cannot outlive the
  parent, create its own children (depth one), or change the parent's
  pipeline.
- **Budgets.** A declared maximum number of children per parent and per step,
  a per-child time limit no greater than the parent's remaining step time,
  model request and retry limits, and an attempt cap like the repair limit.
  Exceeding a budget pauses the parent; it never silently drops work.
- **Permissions.** A child's profile must be a subset of the parent step's
  profile in `allowed_executors` and `allowed_models`. Parallel children are
  read-only reviewers or collectors. At most one agent writes to a worktree or
  pull request at a time. Gitea and Git writes stay in Switchyard-owned steps.
  Command allowlists, executor isolation, and strict OpenCode mode are
  unchanged.
- **Cancellation.** Canceling the parent cancels every child through the
  existing cancel and OpenCode abort paths. The parent declares how a child
  failure is handled (fail the parent, or pause for an operator).
- **Evidence.** Each child records its parent run, step, profile ID and
  version, contract hash, model, request and session IDs, inputs by hash, and
  artifacts, all in the parent's run evidence and visible in the run cockpit.
- **Aggregation.** A deterministic rule declared in the parent definition (for
  example "any `CHANGES_REQUESTED` blocks" or "all must approve"), recorded as
  an artifact that lists the child results it combined. Child output is
  untrusted input to the parent, like issue text today.
- **Resume.** Succeeded children are not rerun when the parent resumes under
  the same `pipeline_version`; their outputs are restored like step outputs.

## Decision

Defer. Switchyard keeps static profiles on linear steps plus the existing
bounded follow-up runs (CI repair and review correction). It adds no dynamic
child agents, model-chosen handoffs, or parallel reviewers. Generalized
autonomous swarms remain outside the approved v1 scope regardless of this
record.

- **Owner:** the repository owner, the same role that holds external
  monitoring and CI-runner responsibility under issue #25.
- **Defer while:** every delegation scenario can be written as sequential
  profile steps or a bounded follow-up run.
- **Revisit trigger:** a recorded issue that shows, from retained run
  evidence, a quality or latency gap that sequential profile steps cannot
  close, for example review artifacts where a second, independent reviewer
  would have caught defects that merged.
- **Proceed criteria:** the owner accepts a bounded design meeting every
  requirement above, with its own threat model for permission subsetting.
  Parallel children also depend on the parallel-group semantics in ADR 0006
  being accepted first.
- **Implementation issues:** created only after that design is accepted, each
  separately scoped. This record creates none.

## Consequences

- Shipped OpenCode agent files declared as `mode: subagent` cannot be invoked
  as subagents under strict mode while `task` is denied.
- A second Open Pilot reviewer, if wanted, is a pipeline and aggregation
  change, not a multi-agent feature.
- No autonomous workers are introduced by this decision.
