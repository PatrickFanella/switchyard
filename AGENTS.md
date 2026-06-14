# Agent Instructions

This repository is Switchyard, a Go full-stack monorepo with Vite React TypeScript, Postgres, Docker, Open Pilot conventions, transitional reference service modules, and first-party workflows.

## Priorities

1. Keep Switchyard runnable and minimal.
2. Preserve Open Pilot issue and PR templates.
3. Keep generated caches, secrets, and local data out of git.
4. Update this file and `README.md` when stack conventions change.

## Architecture Direction

Switchyard is one cohesive automation runtime. Imported apps under `services/` and `workflows/` are reference implementations and transitional scaffolding, not permanent product boundaries. Preserve domain boundaries while moving useful behavior behind native Switchyard contracts:

- Switchyard owns triggers, workflow runs, steps, events, artifacts, logs, leases, retries, cancellation, UI/API, and operator summaries.
- The native model broker module should absorb llama-line ideas: model routing, queueing, upstream health, request IDs, cancellation/drop semantics, and observability.
- The native OpenCode executor module should absorb OCQ ideas: session reuse, request/session correlation, aborts, and adapter behavior.
- Changemaker-style behavior should become first-party workflow policy, prompts, and artifacts on top of Switchyard runtime, not a separate scheduler/runtime.
- Blacktower means Switchyard-native agent profiles/personas, prompt contracts, roles, and default model assignments; do not introduce a separate Blacktower app boundary.
- Open Pilot, Changemaker, and Devlog are first-party workflows that own domain policy only; they should not own generic scheduling or runtime mechanics.

Transitional Compose sidecars for OCQ/llama-line are acceptable while native parity is built. New code should prefer Switchyard-native interfaces over direct service-specific coupling. See `docs/adr/0003-native-switchyard-modules.md`.

## Verification

Run:

```bash
make verify
```

Use narrower commands only when an issue explicitly asks for a smaller check.
