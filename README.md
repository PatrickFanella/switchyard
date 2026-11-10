# Switchyard

![Switchyard identity](docs/brand/switchyard/readme-lockup.png)

Switchyard routes model requests, runs scheduled workflows and shows what each run did, from one API and one dashboard. It is for a single operator who runs local and hosted models on their own hardware and wants workflows, model traffic and coding sessions in one place.

It is a Go API with a React dashboard, PostgreSQL storage and a managed OpenCode server for coding work. It is pre-1.0 and still changing; the latest release is v0.3.1.

## Features

- **Workflows and runs.** Workflow definitions are versioned: saving creates a draft, activating publishes it, and rollback creates a new active version from an earlier one. Each run keeps a snapshot of the definition it started with. Steps can pass named outputs to later steps (`{{steps.<step_id>.outputs.<name>}}`) and declare an output shape that is checked before the next step reads it.
- **Scheduler and workers.** A built-in scheduler fires five-field cron triggers. Workers claim runs with durable leases, retry failures with backoff, and drain active work on shutdown. Run and step retry, cancel and resume are available from the API and dashboard.
- **Model broker.** Provider models go through OpenCode; local models go to Ollama. A durable priority queue orders requests by client priority, returns `429` with `Retry-After` when full, and records history with credential-shaped values redacted.
- **Ollama and OpenAI compatible ingress.** A separate listener accepts `/v1/chat/completions`, `/api/chat`, `/api/generate`, `/api/tags`, `/v1/models`, `/api/embed` and `/v1/embeddings`, so existing clients can point at Switchyard. It serves no control or admin routes.
- **Managed OpenCode executor.** In strict mode the OpenCode server runs as a non-root user with a read-only root filesystem, no inherited plugins, MCP servers or permissions, and in-memory sessions. A separate verifier container runs test commands against a read-only snapshot of the worktree.
- **Open Pilot.** A built-in workflow that takes a labelled or assigned Gitea issue, creates a Git worktree, has OpenCode implement it, runs the issue's test command, and can then commit, push, open a pull request, wait for CI, merge and close the issue. Every one of those steps has its own `disabled`, `dry-run` or `allow` setting, and commands run only from an allowlist. An overseer page orders queued issues by prerequisites, priority and milestone.
- **Changemaker digest and Devlog.** Two built-in workflows draft a digest or a development log from read-only Gitea activity. Drafts are reviewed in the dashboard; posting to a Gitea issue is off by default and needs an approval tied to the exact draft.
- **Webhook triggers.** An optional third listener accepts HMAC-signed webhooks and exposes only its dispatch route.
- **Dashboard.** Overview, runs and run detail, workflows, inbox, queues, sessions, logs and search, agent profiles, prompts and live activity. A read-only token gives view-only access.
- **CLI.** `switchyard open-pilot run|cancel` and `switchyard models list|pull|refresh-opencode`.
- **Observability.** JSON logs with redaction, Prometheus metrics at `GET /metrics`, readiness at `GET /readyz`, and optional OpenTelemetry tracing over OTLP/HTTP.

## Quick start

You need Go 1.26, Node.js 24 with pnpm 10.33 (through Corepack), Make, and Docker with the Compose plugin.

Run the full local check:

```bash
make verify
```

Run the API and the dashboard in two terminals. Without `DATABASE_URL` the API uses in-memory storage.

```bash
make api-run   # API on :8080
make web-dev   # Vite dev server, proxies /api and /healthz to 127.0.0.1:8080
```

Run the whole stack (PostgreSQL, API, OpenCode, verifier and web) with Docker Compose:

```bash
cp .env.example .env   # then set POSTGRES_PASSWORD and the API tokens
make docker-up         # prepares the local executor workspace, then starts Compose
make docker-down
```

Ports bind to `127.0.0.1` by default. Change them inline if they clash, for example `WEB_PORT=8081 API_PORT=18080 make docker-up`. If you change the PostgreSQL credentials after the first start, reset the volume with `CONFIRM=1 make docker-reset`.

Other useful targets:

```bash
make test               # Go and web tests
make lint
make switchyard-smoke   # smoke-test health, workflow, trigger and run endpoints
make api-soak           # 30-second concurrent API soak
make verify-production  # adds database, race, security, image and release checks
```

## Configuration

Settings come from environment variables; `.env.example` lists every one with a comment. The main groups:

| Area | Variables |
| --- | --- |
| Storage | `DATABASE_URL`, `POSTGRES_DB`, `POSTGRES_USER`, `POSTGRES_PASSWORD` |
| Listeners | `API_PORT`, `WEB_PORT`, `LLAMA_LINE_PORT`, `SWITCHYARD_BIND_HOST`, `SWITCHYARD_WEB_BIND_HOST`, `SWITCHYARD_MODEL_BIND_HOST` |
| Access | `SWITCHYARD_CONTROL_API_TOKEN`, `SWITCHYARD_CONTROL_READ_TOKEN`, `SWITCHYARD_REVERSE_PROXY_AUTH`, `SWITCHYARD_REVERSE_PROXY_TOKEN`, `SWITCHYARD_CORS_ALLOWED_ORIGINS`, `SWITCHYARD_PUBLIC_URL` |
| Scheduler and workers | `SWITCHYARD_SCHEDULER_*`, `SWITCHYARD_WORKER_*`, `SWITCHYARD_RUN_*`, `SWITCHYARD_EXECUTOR_LIMITS`, `SWITCHYARD_RETENTION` |
| Models | `OLLAMA_BASE_URL`, `MODEL_BROKER_*`, `OPENCODE_SERVER_URL` |
| OpenCode executor | `OPENCODE_SECURITY_MODE`, `OPENCODE_WORKSPACE`, `OPENCODE_CONFIG_DIR`, `OPENCODE_DATA_DIR`, `OPENCODE_RUNTIME_UID`, `OPENCODE_RUNTIME_GID` |
| Open Pilot | `GITEA_BASE_URL`, `GITEA_TOKEN`, `OPEN_PILOT_*` |
| Webhooks | `SWITCHYARD_WEBHOOK_*` |
| Tracing | `OTEL_EXPORTER_OTLP_ENDPOINT`, `OTEL_EXPORTER_OTLP_HEADERS`, `SWITCHYARD_TRACE_UI_URL` |

Every control-plane request except health checks needs a bearer token or trusted reverse-proxy authentication. Keep `OLLAMA_BASE_URL` pointed at the real Ollama server, never at Switchyard's own compatible port.

References:

- [Runtime configuration](docs/reference/runtime-configuration.md)
- [Open Pilot](docs/open-pilot.md)
- [Client compatibility](docs/reference/client-compatibility.md)
- [Webhook ingress](docs/webhook-ingress.md)
- [Step outputs](docs/reference/step-outputs.md) and [source collection](docs/reference/source-collection.md)
- [Workflow catalog](docs/reference/workflow-catalog.md)
- API: [control plane](docs/openapi.yaml), [model ingress](docs/openapi-model-ingress.yaml), [webhook ingress](docs/openapi-webhook-ingress.yaml)

## Deploy

Switchyard runs as a Docker Compose stack on one Linux host. All external runtime and Dockerfile base images are anonymously pullable public images; the API, web, verifier and OpenCode images are built locally. Install Docker Compose system-wide, normally at `/usr/libexec/docker/cli-plugins/docker-compose`.

1. Copy `.env.example` to `.env` and fill it in. Use `OPENCODE_SECURITY_MODE=strict`, long random tokens, an HTTPS `SWITCHYARD_PUBLIC_URL`, and a dedicated Git workspace outside this repository for `OPENCODE_WORKSPACE`.
2. Check the file: `python3 scripts/validate_production_config.py --env-file .env`. `make production-config-check` validates the values exported in your shell instead, so export the same values first if you use it.
3. Install the host firewall gate and the systemd unit: `sudo ./deploy/install-web-gate.sh`, then `sudo ./deploy/install-systemd.sh`. After pulling new code, `sudo systemctl reload switchyard` validates `.env` again and rebuilds the stack.
4. Put the dashboard behind an authenticating reverse proxy and keep the control API and PostgreSQL bound to loopback.

`deploy/` also has systemd units for scheduled backups and restore drills (`make postgres-backup`, `make restore-drill`). `make release-artifacts` and `make release-verify` build OCI image archives with SBOM and provenance attestations and write `release/SHA256SUMS`. [SECURITY.md](SECURITY.md) describes the network boundaries in detail.

## Project layout

```txt
backend/   Go API, scheduler, workers, model broker, OpenCode executor, CLI and verifier
web/       React and TypeScript dashboard (Vite)
deploy/    systemd units, install scripts, monitoring and CI runner image
migrations/, scripts/, services/   database migrations, checks and service images
docs/      references, OpenAPI specs, architecture decisions and brand assets
```

`codemap.md` maps each directory in more detail.

## Contributing

Report bugs and request features in [GitHub issues](https://github.com/subculture-collective/switchyard/issues). Ask questions and share ideas in [GitHub Discussions](https://github.com/subculture-collective/switchyard/discussions). Open pull requests on GitHub against `main`; read [CONTRIBUTING.md](CONTRIBUTING.md) first. CI runs `make verify` (formatting, lint, Go and web tests, builds, Compose and documentation checks) and builds the frontend image on every pull request. Report security issues as described in [SECURITY.md](SECURITY.md).

## License

GPL-3.0-only. See [LICENSE](LICENSE).
