# Agent Instructions

This repository is Switchyard, a Go full-stack monorepo with Vite React TypeScript, Postgres, Docker, Open Pilot conventions, native Switchyard modules, and first-party workflows.

## Priorities

1. Keep Switchyard runnable and minimal.
2. Preserve Open Pilot issue and PR templates.
3. Keep generated caches, secrets, and local data out of git.
4. Update this file and `README.md` when stack conventions change.

## Architecture Direction

Switchyard is one cohesive automation runtime. OCQ, llama-line, and Changemaker are historical references that were absorbed into native Switchyard modules; their imported directories were removed. Preserve domain boundaries while moving useful behavior behind native Switchyard contracts:

- Switchyard owns triggers, workflow runs, steps, events, artifacts, logs, leases, retries, cancellation, UI/API, and operator summaries.
- The native model broker module should absorb llama-line ideas: model routing, queueing, upstream health, request IDs, cancellation/drop semantics, and observability.
- The native OpenCode executor module should absorb OCQ ideas: session reuse, request/session correlation, aborts, and adapter behavior.
- Changemaker-style behavior should become first-party workflow policy, prompts, and artifacts on top of Switchyard runtime, not a separate scheduler/runtime.
- Blacktower means Switchyard-native agent profiles/personas, prompt contracts, roles, and default model assignments; do not introduce a separate Blacktower app boundary.
- Open Pilot, Changemaker, and Devlog are first-party workflows that own domain policy only; they should not own generic scheduling or runtime mechanics.

Compatibility backends and LLAMA_LINE_PORT ingress are cutover aids only. New code should prefer Switchyard-native interfaces over direct service-specific coupling. See `docs/adr/0003-native-switchyard-modules.md`.

Keep the operator control plane and model-compatible ingress on separate listeners. Model ingress must not expose workflow, run, trigger, history, audit, or administrative routes. Published development ports bind to loopback by default, and command execution remains deny-by-default with argv allowlists even in autonomous modes.

Treat the managed OpenCode sidecar as an untrusted executor. Production uses
`OPENCODE_SECURITY_MODE=strict`, a dedicated worktree root, a non-root runtime
identity, pinned plugins, a read-only root filesystem, and no inherited MCP or
global permission configuration. Development-only capability expansion must be
explicit and must not become the production default.

## Verification

Run:

```bash
make verify
```

Use narrower commands only when an issue explicitly asks for a smaller check.

## Repository Map

A full codemap is available at `codemap.md` in the project root.

Before working on any task, read `codemap.md` to understand:
- Project architecture and entry points
- Directory responsibilities and design patterns
- Data flow and integration points between modules

For deep work on a specific folder, also read that folder's `codemap.md`.
