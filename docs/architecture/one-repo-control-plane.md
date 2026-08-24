# Switchyard One-Repo Control Plane

Switchyard is one repository for one cohesive automation runtime. OCQ, llama-line, and Changemaker are historical references that were absorbed into native Switchyard modules; the imported directories were removed. Blacktower is planned as Switchyard-native profiles/personas rather than a required external operator layer.

> **Migration status:** OCQ, llama-line, and Changemaker directories have been removed. Switchyard now owns the native model broker, OpenCode executor, compatibility ingress, durable request ledger, and first-party workflow policy.

## Product shape

```txt
Switchyard UI
  -> Switchyard runtime API
    -> workflow modules: Open Pilot, Devlog, workflow-policy templates
    -> native model broker module
    -> native OpenCode executor module
    -> native profiles/personas inspired by Blacktower
```

## Hard boundaries

- UI talks to Switchyard APIs only.
- Switchyard owns triggers, schedules, run records, events, artifacts, and the workflow registry.
- Workflows own domain policy: Gitea policy belongs to Open Pilot; content policy belongs to workflow-policy templates and Devlog.
- The native model broker owns model routing, inference queues, request IDs, upstream health, and broker observability.
- The native OpenCode executor owns session reuse, request/session correlation, cancellation, and adapter behavior.
- Blacktower-style behavior is a curated Switchyard-native profile/persona set layered on top of the runtime.
- The Switchyard-native orchestrator/subagent layer may assign models from config/API input, but it should stay scoped to workflow execution and avoid recreating a broad plugin marketplace too early.

## Migration sequence

1. Treat OCQ, llama-line, and Changemaker as historical reference material only.
2. Keep compatibility ingress and adapters available for callers while native module contracts remain the runtime source of truth.
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

This MVP records run envelopes and events, runs opt-in pipelines, serves native model broker/OpenCode compatibility surfaces, and prepares for deeper profile modules.

Cutover note: the API exposes llama-line/Ollama-compatible model ingress on `LLAMA_LINE_PORT` for `/broker/status`, `/v1/chat/completions`, `/api/chat`, `/api/generate`, `/api/tags`, `/v1/models`, `/api/embed`, and `/v1/embeddings`. Admin queue/inflight and durable history/search/log/config compatibility surfaces remain on the authenticated control listener; model ingress cannot reach them. Production pins the API on the Compose bridge and requires `OLLAMA_BASE_URL` to equal the host bridge-gateway TCP/11435 endpoint protected by an exact UFW input rule.

See `../roadmap.md` for the next phase: Switchyard Runtime, a workflow-scoped orchestrator with durable steps, native model brokering, native OpenCode execution, and built-in profiles/personas.
