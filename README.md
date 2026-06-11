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

## Control-plane MVP

The backend exposes an in-memory workflow/run API:

- `GET /healthz`
- `GET /api/workflows`
- `GET /api/triggers`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `POST /api/runs`
- `POST /api/triggers/{id}/run`

See `docs/architecture/one-repo-control-plane.md` for the monorepo migration plan.

## Open Pilot

This repository keeps the Open Pilot issue template and PR template.
Bootstrap labels after creating or moving the repository:

```bash
open-pilot labels bootstrap PatrickFanella/switchyard
```

## License

GPL-3.0-only. See `LICENSE`.
