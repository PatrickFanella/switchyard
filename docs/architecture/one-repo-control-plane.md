# Switchyard One-Repo Control Plane

Switchyard is one repository for one cohesive automation runtime. Imported apps under `services/` and `workflows/` are transitional reference implementations, not permanent product boundaries. Blacktower is planned as Switchyard-native profiles/personas rather than a required external operator layer.

## Product shape

```txt
Switchyard UI
  -> Switchyard runtime API
    -> workflow modules: Open Pilot, Devlog, Changemaker-style policy
    -> native model broker module inspired by llama-line
    -> native OpenCode executor module inspired by OCQ
    -> native profiles/personas inspired by Blacktower
```

## Hard boundaries

- UI talks to Switchyard APIs only.
- Switchyard owns triggers, schedules, run records, events, artifacts, and the workflow registry.
- Workflows own domain policy: Gitea policy belongs to Open Pilot; content policy belongs to Changemaker/Devlog.
- The native model broker owns model routing, inference queues, request IDs, upstream health, and broker observability.
- The native OpenCode executor owns session reuse, request/session correlation, cancellation, and adapter behavior.
- Blacktower-style behavior is a curated Switchyard-native profile/persona set layered on top of the runtime.
- The Switchyard-native orchestrator/subagent layer may assign models from config/API input, but it should stay scoped to workflow execution and avoid recreating a broad plugin marketplace too early.

## Migration sequence

1. Import OCQ and llama-line under `services/` and Changemaker under `workflows/` as reference material.
2. Keep transitional sidecars runnable while native module contracts are built.
3. Build Switchyard's API and UI around stable concepts: workflows, triggers, runs, steps, events, artifacts, logs, sessions, prompts, profiles, and module health.
4. Pull timers out of app-specific workflows. Cron, interval, webhook, manual, and Gitea-poll triggers belong to Switchyard.
5. Move workflow callers to native Switchyard interfaces, then absorb sidecar behavior behind those interfaces.

## Current MVP

The backend has durable Postgres storage when `DATABASE_URL` is configured and an in-memory fallback for local development without a database. It establishes the suite shape without prematurely committing to deep service rewrites:

- `GET /healthz`
- `GET /api/workflows`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `POST /api/runs`
- `POST /api/triggers/{id}/run`

This MVP records run envelopes and events, runs opt-in pipelines, probes transitional sidecars, and prepares for native model broker, OpenCode executor, and profile modules.

See `../roadmap.md` for the next phase: Switchyard Runtime, a workflow-scoped orchestrator with durable steps, native model brokering, native OpenCode execution, and built-in profiles/personas.
