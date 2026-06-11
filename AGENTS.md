# Agent Instructions

This repository is Switchyard, a Go full-stack project with Vite React TypeScript, Postgres, Docker, and Open Pilot conventions.

## Priorities

1. Keep Switchyard runnable and minimal.
2. Preserve Open Pilot issue and PR templates.
3. Keep generated caches, secrets, and local data out of git.
4. Update this file and `README.md` when stack conventions change.

## Architecture Direction

Switchyard is moving toward a one-repo control-plane suite. Preserve hard boundaries:

- Switchyard owns triggers, workflow runs, events, artifacts, and UI/API.
- `llama-line` owns model routing, queueing, upstream health, and observability.
- OCQ owns OpenCode/Blacktower session and tmux adapter behavior.
- Blacktower owns OpenCode agents, skills, plugins, and operator environment.
- Open Pilot, Changemaker, and Devlog are first-party workflows that should not own generic scheduling.

## Verification

Run:

```bash
make verify
```

Use narrower commands only when an issue explicitly asks for a smaller check.
