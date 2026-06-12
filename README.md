# Switchyard

Switchyard is a Go backend and Vite React TypeScript monorepo control-plane workspace for OpenCode workflows, `llama-line` model routing, OCQ session execution, built-in Blacktower agent profiles, and first-party workflows such as Open Pilot, Changemaker, and Devlog.

The product direction is one monorepo with hard runtime boundaries: Switchyard owns triggers, workflow runs, events, artifacts, runtime orchestration, agent profiles, and UI; `services/llama-line` remains the model broker; `services/ocq` remains the OpenCode/session adapter; workflow modules such as `workflows/changemaker` own domain policy. Blacktower is now planned as a Switchyard-native set of agents/profiles rather than an external dependency.

## Monorepo layout

```txt
backend/              Switchyard API, scheduler, runs, triggers, adapters
web/                  Switchyard HUD dashboard
services/ocq/         OpenCode session gateway
services/llama-line/  model broker, queueing, upstream health
workflows/changemaker/ first-party Changemaker workflow
blacktower/           legacy/reference checkout only, ignored when present
```

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
- Open Pilot: runs validate `repo`/`issue`, optionally fetch the issue from Gitea when `GITEA_BASE_URL` is configured, enforce required labels from `OPEN_PILOT_REQUIRED_LABELS`, attach issue/prompt artifacts, and call OCQ through the OpenCode executor when `OCQ_BASE_URL` is configured. `OPEN_PILOT_MODEL` controls the model. Set `OPEN_PILOT_REQUIRE_ISSUE_SPEC=true` to require issue text with acceptance/test instructions. `OPEN_PILOT_COMMAND` remains available for CLI compatibility/cancel hooks.
- Devlog: `manual-devlog` runs a three-step pipeline that collects recent Switchyard run history, generates Markdown through the shared model executor when `LLAMA_LINE_BASE_URL` or `OCQ_BASE_URL` is configured, and attaches `devlog.md` as an artifact. `DEVLOG_MODEL` controls the model.

Switchyard also ships its own CLI contract:

```bash
switchyard open-pilot run --repo OWNER/REPO --issue 123 --json --api-url http://127.0.0.1:8080
switchyard open-pilot cancel --run RUN_ID --json --api-url http://127.0.0.1:8080
```

Run cancellation is Switchyard-side plus best-effort upstream cancellation. Switchyard calls Changemaker `POST /api/runs/cancel`, OCQ abort endpoints when `ocq_request_id` is present, llama-line `DELETE /admin/queue/{request_id}` when `llama_line_request_id` is present, and `OPEN_PILOT_COMMAND cancel --run <id> --json` when configured.

Postgres stores its initial credentials in the Docker volume. If you change `POSTGRES_USER`, `POSTGRES_PASSWORD`, or `POSTGRES_DB` after the volume already exists and see password authentication failures, reset the local database volume:

```bash
CONFIRM=1 make docker-reset
make docker-up
```

## Control-plane MVP

The backend exposes a workflow/run API:

- `GET /healthz`
- `GET /api/workflows`
- `POST /api/workflows/open-pilot/run`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `GET /api/runs/{id}/steps`
- `GET /api/adapters`
- `GET /api/scheduler`
- `POST /api/runs`
- `POST /api/runs/{id}/retry`
- `POST /api/runs/{id}/cancel`
- `POST /api/runs/{id}/steps/{step_id}/retry`
- `POST /api/runs/{id}/steps/{step_id}/cancel`
- `POST /api/triggers/{id}/run`
- `PATCH /api/triggers/{id}`

The web dashboard reads these endpoints directly, shows workflow/trigger/run state, and can dispatch enabled triggers with **Run now**. It also includes HUD-styled module shells for queues, sessions, logs/search, prompts, config, agents, and skills so the imported `llama-line`, OCQ, Changemaker, and planned Blacktower agent surfaces have Switchyard homes. In Docker, nginx serves the built UI and proxies `/api/*` plus `/healthz` to the internal `api:8080` service.

See `docs/architecture/one-repo-control-plane.md` for the monorepo architecture, `docs/roadmap.md` for the next major development phase, and `docs/ui-feature-migration.md` for the imported UI feature map.

## Open Pilot

This repository keeps the Open Pilot issue template and PR template.
Bootstrap labels after creating or moving the repository:

```bash
open-pilot labels bootstrap PatrickFanella/switchyard
```

## License

GPL-3.0-only. See `LICENSE`.
