# Agent Instructions

This repository is Switchyard, a Go full-stack monorepo with Vite React TypeScript, Postgres, Docker, Open Pilot conventions, imported service modules, and first-party workflows.

## Priorities

1. Keep Switchyard runnable and minimal.
2. Preserve Open Pilot issue and PR templates.
3. Keep generated caches, secrets, and local data out of git.
4. Update this file and `README.md` when stack conventions change.

## Architecture Direction

Switchyard is a one-repo control-plane suite. Preserve hard boundaries even when code lives in one Git repository:

- Switchyard owns triggers, workflow runs, events, artifacts, and UI/API.
- `services/llama-line` owns model routing, queueing, upstream health, and observability.
- `services/ocq` owns OpenCode session and tmux adapter behavior.
- `workflows/changemaker` owns Changemaker content/workflow policy.
- Blacktower is planned as a Switchyard-native set of agent profiles/personas for workflow execution, not as a required external checkout.
- Open Pilot, Changemaker, and Devlog are first-party workflows that should not own generic scheduling.

Future direction: Switchyard should grow a small built-in orchestrator/subagent runtime with assignable models from config or API input. The Blacktower name can refer to the first curated agent/profile set inside that runtime.

## Verification

Run:

```bash
make verify
```

Use narrower commands only when an issue explicitly asks for a smaller check.
