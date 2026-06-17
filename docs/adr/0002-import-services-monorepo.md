# ADR 0002: Import OCQ, llama-line, and Changemaker into Switchyard

## Status

Accepted — 2026-06-11. Historical import phase; superseded for long-term architecture by [ADR 0003](0003-native-switchyard-modules.md). The imported directories were later removed.

## Context

Switchyard started with ignored local checkouts of four related repositories: `ocq`, `llama-line`, `changemaker`, and `blacktower`. The desired product shape is a Switchyard monorepo with native model broker, OpenCode executor, workflow policy, and a future Switchyard-native Blacktower agent/profile set.

## Decision

Import three modules into Switchyard as tracked source during migration:

- `services/ocq/` from local `ocq` commit `6543ea7 feat: add explicit abort endpoints`
- `services/llama-line/` from local `llama-line` commit `db93967 fix: propagate ocq contract headers`
- `workflows/changemaker/` from local `changemaker` commit `2e2b468 feat: add run cancellation endpoint`

Do not import the external Blacktower checkout. A local `blacktower/` checkout remains ignored as legacy/reference material while Switchyard defines its own Blacktower agent/profile layer.

The imports are copy-based rather than subtree-based. This keeps the migration simple and reviewable while the original repositories remain available for full history if needed.

## Original Boundaries Superseded by ADR 0003

These boundaries describe the copy/import phase only. They should not be treated as the long-term product architecture.

- Switchyard core owned workflow runs, triggers, events, artifacts, API, UI, scheduler, and adapter contracts.
- `services/llama-line` owned model routing, queueing, upstream health, and observability.
- `services/ocq` owned OpenCode/Blacktower session adapter behavior, request/session correlation, and abort semantics.
- `workflows/changemaker` owned Changemaker content generation and domain policy.
- Blacktower in Switchyard will mean a curated set of built-in agent profiles/personas on top of Switchyard Runtime. It does not imply importing the external Blacktower plugin/skill/operator environment.

## Future direction

Switchyard may add a small built-in orchestrator/subagent workflow runtime with assignable models from config or API calls. Blacktower can become the first curated profile set for that runtime.

Update: ADR 0003 clarifies that OCQ, llama-line, Changemaker, and Blacktower are sources of proven ideas and transitional scaffolding, not permanent separate product boundaries. Their useful behavior should be rebuilt as native Switchyard modules over time. The imported `services/ocq`, `services/llama-line`, and `workflows/changemaker` directories are no longer present.

## Consequences

- There is one product repository: Switchyard monorepo.
- External Blacktower history remains reference material, not a runtime dependency.
- Build/test consolidation can happen gradually; do not force all imported modules into root `make verify` until their dependency setup is normalized.
