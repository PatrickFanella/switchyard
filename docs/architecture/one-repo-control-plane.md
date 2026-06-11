# Switchyard One-Repo Control Plane

Switchyard is becoming one repository for the suite, but not one blended runtime.

## Product shape

```txt
Switchyard UI
  -> Switchyard control plane
    -> workflow modules: open-pilot, changemaker, devlog
    -> llama-line: model routing, queueing, upstream health, observability
      -> ocq: OpenCode + Blacktower session/tmux adapter
        -> blacktower: OpenCode agents, skills, plugins, operator environment
```

## Hard boundaries

- UI talks to Switchyard APIs only.
- Switchyard owns triggers, schedules, run records, events, artifacts, and the workflow registry.
- Workflows own domain policy: Gitea policy belongs to Open Pilot; content policy belongs to Changemaker/Devlog.
- `llama-line` owns model routing and inference queues, not workflow semantics.
- `ocq` owns OpenCode session reuse, request/session correlation, cancellation, and tmux binding.
- `blacktower` owns the OpenCode distribution, agents, skills, plugins, and operator ergonomics.

## Migration sequence

1. Keep copied `blacktower`, `llama-line`, `ocq`, and `changemaker` checkouts as ignored references.
2. Build Switchyard's control-plane API and UI around stable concepts: workflows, triggers, runs, events, artifacts, sessions, prompts, agents, and skills.
3. Pull timers out of app-specific workflows. Cron, interval, webhook, manual, and Gitea-poll triggers belong to Switchyard.
4. Wrap existing workflow behavior through adapters before rewriting internals.
5. Move first-party workflow code into Switchyard once the API contracts are stable.
6. Remove ignored reference checkouts after their useful code has been ported or replaced.

## Current MVP

The backend has durable Postgres storage when `DATABASE_URL` is configured and an in-memory fallback for local development without a database. It establishes the suite shape without prematurely committing to deep service rewrites:

- `GET /healthz`
- `GET /api/workflows`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `POST /api/runs`
- `POST /api/triggers/{id}/run`

This MVP records run envelopes and events. Adapter execution is the next boundary to implement.
