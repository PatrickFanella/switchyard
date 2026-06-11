# Switchyard One-Repo Control Plane

Switchyard is one repository for the suite, but not one blended runtime. Blacktower remains a separate optional operator layer.

## Product shape

```txt
Switchyard UI
  -> Switchyard control plane
    -> workflow modules: open-pilot, changemaker, devlog
    -> services/llama-line: model routing, queueing, upstream health, observability
      -> services/ocq: OpenCode + optional Blacktower session/tmux adapter
        -> external blacktower: OpenCode agents, skills, plugins, operator environment
```

## Hard boundaries

- UI talks to Switchyard APIs only.
- Switchyard owns triggers, schedules, run records, events, artifacts, and the workflow registry.
- Workflows own domain policy: Gitea policy belongs to Open Pilot; content policy belongs to Changemaker/Devlog.
- `services/llama-line` owns model routing and inference queues, not workflow semantics.
- `services/ocq` owns OpenCode session reuse, request/session correlation, cancellation, and tmux binding.
- Blacktower owns the OpenCode distribution, agents, skills, plugins, and operator ergonomics. It is optional; Switchyard must run without it.
- A future small Switchyard-native orchestrator/subagent layer may assign models from config/API input, but it should stay scoped to workflow execution and not replace Blacktower's full operator environment.

## Migration sequence

1. Import OCQ and llama-line under `services/` and Changemaker under `workflows/`.
2. Keep Blacktower as an ignored optional checkout/integration.
3. Build Switchyard's control-plane API and UI around stable concepts: workflows, triggers, runs, events, artifacts, sessions, prompts, agents, and skills.
4. Pull timers out of app-specific workflows. Cron, interval, webhook, manual, and Gitea-poll triggers belong to Switchyard.
5. Wrap existing behavior through adapters before rewriting internals.

## Current MVP

The backend has durable Postgres storage when `DATABASE_URL` is configured and an in-memory fallback for local development without a database. It establishes the suite shape without prematurely committing to deep service rewrites:

- `GET /healthz`
- `GET /api/workflows`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `POST /api/runs`
- `POST /api/triggers/{id}/run`

This MVP records run envelopes and events, runs opt-in adapters, probes imported services, and keeps external Blacktower optional.
