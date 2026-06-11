# Switchyard

Switchyard is a Go backend and Vite React TypeScript control-plane workspace for OpenCode/Blacktower workflows, `llama-line` model routing, OCQ session execution, and first-party workflows such as Open Pilot, Changemaker, and Devlog.

The product direction is one repo with hard runtime boundaries: Switchyard owns triggers, workflow runs, events, artifacts, and UI; `llama-line` remains the model broker; OCQ remains the OpenCode/Blacktower session adapter; workflow modules own domain policy.

## Quick start

```bash
make verify
```

Useful Switchyard-specific commands:

```bash
make api-run          # run the Switchyard API on :8080
make web-dev          # run the Vite UI
make switchyard-smoke # smoke-test health/workflow/trigger/run endpoints
make docker-up        # start the local stack
make docker-down      # stop the local stack
```

Run `make api-run` and `make web-dev` in separate terminals for local dashboard work. The Vite dev server proxies `/api` and `/healthz` to `http://127.0.0.1:8080`; set `VITE_SWITCHYARD_API_URL` only when the API is hosted elsewhere.

Docker port defaults are configurable through `.env` or inline variables:

```bash
WEB_PORT=8081 API_PORT=18080 POSTGRES_PORT=5432 make docker-up
```

## Control-plane storage

The API uses Postgres when `DATABASE_URL` is set and runs embedded migrations on startup. Without `DATABASE_URL`, it falls back to in-memory storage for local UI/API development.

The Docker Compose stack sets `DATABASE_URL` for the API service using libpq keyword syntax so generated passwords with URL-significant characters do not need escaping.

Set optional adapter probe variables to replace placeholder HUD data with live external state:

- `LLAMA_LINE_BASE_URL` reads llama-line `/broker/status`.
- `OCQ_BASE_URL` and optional `OCQ_GATEWAY_KEY` read OCQ `/healthz` and `/ocq/sessions`.
- `BLACKTOWER_BASE_URL` reads Blacktower control-center `/api/snapshot`.
- `CHANGEMAKER_BASE_URL` and optional `CHANGEMAKER_ADMIN_TOKEN` read Changemaker `/health`, `/api/posts`, and `/api/runs/status`.

Set `SWITCHYARD_SCHEDULER_ENABLED=true` to enable the in-process trigger scheduler. `SWITCHYARD_SCHEDULER_INTERVAL` defaults to `1m`.

Workflow execution adapters are opt-in:

- Changemaker: `CHANGEMAKER_BASE_URL` dispatches `POST /api/runs`.
- Open Pilot: `OPEN_PILOT_COMMAND` enables `open-pilot run --repo <repo> --issue <issue> --json`; runs must provide `repo` and `issue` inputs.
- Devlog: `manual-devlog` generates a Markdown artifact from recent Switchyard run history. If `LLAMA_LINE_BASE_URL` or `OCQ_BASE_URL` is configured, it asks the model `DEVLOG_MODEL` to polish the Markdown; otherwise it writes a deterministic local summary.

Run cancellation is currently Switchyard-side: queued/running runs can be marked canceled and an event is recorded. True upstream cancellation will be added per adapter when Changemaker, OCQ, llama-line, and Open Pilot expose safe cancel/abort APIs.

Postgres stores its initial credentials in the Docker volume. If you change `POSTGRES_USER`, `POSTGRES_PASSWORD`, or `POSTGRES_DB` after the volume already exists and see password authentication failures, reset the local database volume:

```bash
CONFIRM=1 make docker-reset
make docker-up
```

## Control-plane MVP

The backend exposes a workflow/run API:

- `GET /healthz`
- `GET /api/workflows`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `GET /api/adapters`
- `GET /api/scheduler`
- `POST /api/runs`
- `POST /api/runs/{id}/retry`
- `POST /api/runs/{id}/cancel`
- `POST /api/triggers/{id}/run`
- `PATCH /api/triggers/{id}`

The web dashboard reads these endpoints directly, shows workflow/trigger/run state, and can dispatch enabled triggers with **Run now**. It also includes HUD-styled module shells for queues, sessions, logs/search, prompts, config, agents, and skills so the existing `llama-line`, OCQ, Changemaker, and Blacktower UI surfaces have Switchyard homes. In Docker, nginx serves the built UI and proxies `/api/*` plus `/healthz` to the internal `api:8080` service.

See `docs/architecture/one-repo-control-plane.md` for the monorepo migration plan and `docs/ui-feature-migration.md` for the imported UI feature map.

## Open Pilot

This repository keeps the Open Pilot issue template and PR template.
Bootstrap labels after creating or moving the repository:

```bash
open-pilot labels bootstrap PatrickFanella/switchyard
```

## License

GPL-3.0-only. See `LICENSE`.
