# ADR 0002: Import OCQ, llama-line, and Changemaker into Switchyard

## Status

Accepted — 2026-06-11

## Context

Switchyard started with ignored local checkouts of four related repositories: `ocq`, `llama-line`, `changemaker`, and `blacktower`. The desired product shape is a Switchyard monorepo plus a separate Blacktower repository.

## Decision

Import three modules into Switchyard as tracked source:

- `services/ocq/` from local `ocq` commit `6543ea7 feat: add explicit abort endpoints`
- `services/llama-line/` from local `llama-line` commit `db93967 fix: propagate ocq contract headers`
- `workflows/changemaker/` from local `changemaker` commit `2e2b468 feat: add run cancellation endpoint`

Keep Blacktower separate and optional. A local `blacktower/` checkout remains ignored by Switchyard.

The imports are copy-based rather than subtree-based. This keeps the migration simple and reviewable while the original repositories remain available for full history if needed.

## Boundaries

- Switchyard core owns workflow runs, triggers, events, artifacts, API, UI, scheduler, and adapter contracts.
- `services/llama-line` owns model routing, queueing, upstream health, and observability.
- `services/ocq` owns OpenCode/Blacktower session adapter behavior, request/session correlation, and abort semantics.
- `workflows/changemaker` owns Changemaker content generation and domain policy.
- Blacktower owns the full OpenCode plugin/agent/skill/operator environment and remains optional.

## Future direction

Switchyard may add a small built-in orchestrator/subagent workflow runtime with assignable models from config or API calls. That layer should stay workflow-scoped and should not absorb Blacktower's full plugin, skill, and operator environment.

## Consequences

- There are now two conceptual repositories: Switchyard monorepo and Blacktower.
- Switchyard can run without Blacktower.
- Build/test consolidation can happen gradually; do not force all imported modules into root `make verify` until their dependency setup is normalized.
