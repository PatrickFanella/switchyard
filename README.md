# Switchyard

Switchyard is a Go backend and Vite React TypeScript monorepo for a cohesive automation runtime: workflow orchestration, model routing, OpenCode execution, built-in agent profiles, operator HUD/API, and first-party workflows such as Open Pilot, native workflow-creator templates, and Devlog.

OCQ, llama-line, and Changemaker are historical references that have been absorbed into native Switchyard modules. The product direction is to keep their useful ideas as first-party contracts: model broker, OpenCode executor, profiles/personas, workflow policy, logs, artifacts, and operator controls.

## Monorepo layout

```txt
backend/              Switchyard API, scheduler, runs, triggers, native runtime
web/                  Switchyard HUD dashboard
blacktower/           legacy/reference checkout only, ignored when present
docs/                 ADRs, references, and migration notes
```

Historical OCQ/llama-line/Changemaker directories have been removed; their behavior now lives behind native Switchyard modules.

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
make verify-production # run database, race, security, image, smoke, and release gates
```

## Verification gates

`make verify` is the required baseline gate for every push and pull request; it is run by the `switchyard-check` Gitea workflow. `make verify-production` is the full release-qualification gate, available manually and on the weekly `switchyard-production-verify` workflow schedule.

Both workflows target the dedicated `switchyard-production` runner label. Its reproducible job image is defined in `deploy/runner/Dockerfile` and must be published and configured by immutable digest. The baseline CI runner contract is Go `1.26.5`, Node.js major `24`, Corepack-provided pnpm `10.33.0`, Make, Docker Engine, and the Docker Compose plugin. The production runner contract adds Docker Buildx with a bootstrappable BuildKit builder that can produce an OCI archive with the pinned SBOM generator and `--provenance=mode=max` options used by `make release-artifacts`. Both workflows validate these contracts before running Make; CI supplies only the non-secret `POSTGRES_PASSWORD=switchyard-ci-only-not-a-secret` needed for clean-checkout Compose validation. No CI setup action supplies these tools.

Production releases are pinned OCI archives with BuildKit SBOM and provenance attestations. `make release-verify` writes and verifies `release/SHA256SUMS`; generated release artifacts remain outside git. Runtime logs are newline-delimited JSON with credential-shaped values redacted. `GET /metrics` exports Prometheus-format workflow, model-request, queue, latency, and worker metrics.

`make api-soak` runs a 30-second concurrent authenticated API soak by default. Increase `SWITCHYARD_SOAK_DURATION`, `SWITCHYARD_SOAK_CLIENTS`, and `SWITCHYARD_SOAK_MIN_REQUESTS` for extended release qualification.

Run `make api-run` and `make web-dev` in separate terminals for local dashboard work. The Vite dev server proxies `/api` and `/healthz` to `http://127.0.0.1:8080`; set `VITE_SWITCHYARD_API_URL` only when the API is hosted elsewhere.

Docker port defaults are configurable through `.env` or inline variables:

```bash
WEB_PORT=8081 API_PORT=18080 POSTGRES_PORT=5432 make docker-up
```

Published ports bind to `127.0.0.1` by default. `SWITCHYARD_BIND_HOST` controls the API and Postgres, `SWITCHYARD_WEB_BIND_HOST` controls the web UI, and `SWITCHYARD_MODEL_BIND_HOST` controls model-compatible ingress. Keep API/control and Postgres loopback-bound. For the production dashboard, bind web only on Kvant's Tailscale address (`SWITCHYARD_WEB_BIND_HOST=100.65.164.66`); Almaz Caddy proxies to `100.65.164.66:28081`, and web accepts requests only from Almaz's exact Tailscale address (`SWITCHYARD_WEB_TRUSTED_PROXY_CIDR=100.77.128.80/32`). Direct web access is denied. For LAN clients, set `SWITCHYARD_MODEL_BIND_HOST` to the host's LAN address to expose only model-compatible ingress. The control-plane API and model-compatible ingress use separate in-container listeners; the model port does not serve run, trigger, workflow, history, audit, or administrative routes.

## Control-plane storage

The API uses Postgres when `DATABASE_URL` is set and runs embedded migrations on startup. Without `DATABASE_URL`, it falls back to in-memory storage for local UI/API development.

The Docker Compose stack sets `DATABASE_URL` for the API service using libpq keyword syntax so generated passwords with URL-significant characters do not need escaping.

The local stack now runs Switchyard-native workflow, model-broker, and OpenCode executor paths by default:

- The root Docker stack includes Postgres, API, web, and a local OpenCode server sidecar. Compatibility backends are cutover-only.
- Switchyard API also exposes llama-line/Ollama-compatible model ingress on `LLAMA_LINE_PORT` for cutover: `/broker/status`, `/v1/chat/completions`, `/api/chat`, `/api/generate`, `/api/tags`, `/v1/models`, `/api/embed`, and `/v1/embeddings`. Admin queue/inflight and durable history/search/log/config compatibility surfaces remain on the authenticated control listener (`API_PORT`) and are never exposed by the model listener.
- Workflow code targets Switchyard-native `ModelBroker` and `OpenCodeExecutor` contracts. The native broker routes provider-style models through `OPENCODE_SERVER_URL` (default `http://opencode:4096`) and routes Ollama/local models to `OLLAMA_BASE_URL` when configured. Use `ollama/<model>` to force Ollama routing, or tune automatic local-model prefixes with `MODEL_BROKER_OLLAMA_MODELS`. Do not point `OLLAMA_BASE_URL` at Switchyard's own `LLAMA_LINE_PORT`; it must be the real upstream Ollama server.
- `MODEL_BROKER_RETRY_ATTEMPTS`, `MODEL_BROKER_RETRY_BACKOFF`, and `MODEL_BROKER_RECOVERY_STALE_AFTER` tune bounded upstream retry and async recovery behavior; keep attempts low for interactive clients.
- llama-line-compatible client attribution uses `Authorization: Bearer <key>` mapped by `MODEL_BROKER_CLIENT_KEYS` (`client-name=token:priority`) or trusted headers `X-Switchyard-Client`/`X-Llama-Line-Client` plus `X-Switchyard-Priority`/`X-Llama-Line-Priority`. Broker history, status, logs, and audit surfaces expose the resolved client and priority.

### Compatibility lifecycle

The llama-line/Ollama/OpenAI-compatible ingress is supported. Only external cutover selectors are deprecated: `MODEL_BROKER_BACKEND=llama-line` and nested `OPENCODE_EXECUTOR_BACKEND=ocq`; native defaults and Ollama routing remain supported. Actual deprecated generation attempts expose `X-Switchyard-Backend` and `X-Switchyard-Compatibility-Warning: deprecated-external-backend` before JSON/SSE commitment, while async/workflow attempts persist namespaced decision correlations and events. `/broker/status` reports `effective_selector` plus the optional raw `configured_selector`; `/api/config` reports effective broker/executor selectors plus their optional raw configured values. Unknown or blank selectors route as `native`, and warnings appear only for llama-line or nested OCQ selection; no standard HTTP deprecation headers are emitted from status/config. Selectors will not be removed before v1.0 and require migration docs, at least 90 days, and two consecutive releases with zero observed deprecated-backend correlations/events; removal may then occur in v1.0 or a later major release. Ingress remains supported. External Changemaker adapter probes remain read-only migration visibility and do not affect the native Changemaker pipeline.
- The OpenCode image includes the basic Switchyard toolchain (`git`, Go, Make, Node/npm/Corepack, shell utilities), installs the latest `opencode-ai`, RTK, DCP/quota/tasks plugins, and materializes Switchyard-managed OpenCode defaults at container start. It mounts `OPENCODE_WORKSPACE` (default `.`) at `/workspace`, mounts `OPENCODE_CONFIG_DIR` (default ignored `./.opencode`) at `/root/.config/opencode`, and mounts `OPENCODE_DATA_DIR` (default ignored `./opencode-data`) at `/root/.local/share/opencode` for auth data such as `auth.json`. The API mounts the same `OPENCODE_WORKSPACE` at `/workspace` so Open Pilot can create isolated worktrees that OpenCode can edit; API/OpenCode containers mark mounted workspaces as Git safe directories to avoid UID-mismatch failures. Set those paths to your host OpenCode directories to reuse `opencode auth login` credentials. Override `OPENCODE_SERVER_URL=http://host.docker.internal:4096` only when deliberately using a host-run OpenCode server. `OPENCODE_EXECUTOR_TIMEOUT` and `MODEL_BROKER_TIMEOUT` default to `10m` for long-running agent prompts and model-broker gates; `OPENCODE_SESSION_CREATE_TIMEOUT`, `OPENCODE_MESSAGE_TIMEOUT`, and `OPENCODE_ABORT_TIMEOUT` tune native session control.
- External `BLACKTOWER_BASE_URL` probes remain optional/reference-only. Changemaker is now native workflow-template policy, not an external runtime dependency.

For live cutover operation, rollback, and smoke commands, see [`docs/runbooks/native-model-broker-cutover.md`](docs/runbooks/native-model-broker-cutover.md).

The in-process trigger scheduler is enabled by default; set `SWITCHYARD_SCHEDULER_ENABLED=false` to disable it. It supports numeric five-field cron lists, ranges, and steps across minute, hour, day-of-month, month, and day-of-week. `SWITCHYARD_SCHEDULER_TIMEZONE` defaults to `UTC`. Missed occurrences coalesce into one run, after which the next future occurrence is scheduled. `SWITCHYARD_SCHEDULER_INTERVAL` defaults to `1m`.

Async worker dispatch is enabled by default, so `POST /api/runs`, trigger runs, retries, and Open Pilot dispatch return `202 Accepted` with queued state. Set `SWITCHYARD_WORKER_ENABLED=false` only for local synchronous debugging. Workers claim runs using durable leases and retry failed or timed-out runs up to `SWITCHYARD_RUN_MAX_ATTEMPTS` (default `3`) with exponential backoff bounded by `SWITCHYARD_RUN_RETRY_MAX_BACKOFF`; `SWITCHYARD_RUN_TIMEOUT` defaults to `10m`. Configure concurrency with `SWITCHYARD_WORKER_COUNT`, `SWITCHYARD_WORKER_LEASE`, and `SWITCHYARD_WORKER_POLL_INTERVAL`. On termination, new claims stop and active work drains for `SWITCHYARD_SHUTDOWN_GRACE` before forced cancellation. Use `SWITCHYARD_EXECUTOR_LIMITS` for optional per-executor pipeline concurrency caps such as `model=1,opencode=1,command=2,git=1,gitea=2,http=2`; omitted or invalid entries are ignored.

Asynchronous model-compatible requests use a durable priority queue. `MODEL_BROKER_QUEUE_MAX_DEPTH` (default `100`, maximum `200`) bounds queued, running, and cancel-requested work; full queues return `429` with `Retry-After`. `MODEL_BROKER_QUEUE_WORKERS` controls concurrent claims. Claims order higher client priority first and FIFO within equal priority, using atomic row locking under Postgres.

List runs with bounded cursor pagination using `GET /api/runs?limit=50&cursor=...`; the response includes `next_cursor`. `GET /readyz` checks persistence rather than only process liveness. Terminal runs and model requests are pruned after `SWITCHYARD_RETENTION` (default `720h`) on `SWITCHYARD_RETENTION_INTERVAL`. Request, prompt, model-response, and artifact sizes are bounded by the corresponding `SWITCHYARD_MAX_*_BYTES` settings, model history redacts credential-shaped values before persistence, and per-client request rate is capped by `SWITCHYARD_RATE_LIMIT_PER_SECOND`.

Workflow-definition PUTs create immutable draft versions. Validate and publish the latest draft with `POST /api/workflow-definitions/{id}/activate`, inspect history at `GET .../{id}/versions`, and restore an earlier snapshot with `POST .../{id}/rollback` and `{"version": N}`; rollback creates a new active version rather than rewriting history. Generic HTTP steps are supported only when the definition opts in with `allow_http=true`, the target host matches `SWITCHYARD_HTTP_ALLOWED_HOSTS`, and HTTPS is used (`SWITCHYARD_HTTP_ALLOW_INSECURE=true` is intended only for controlled local testing). Redirects, methods, timeout, and response size are constrained. Compatibility feedback now requires `request_id`, a rating from `-1` to `1`, and an optional redacted comment, and is durably queryable from `/admin/feedback`.

Every control-plane request except health checks requires authentication. Set `SWITCHYARD_CONTROL_API_TOKEN` to a long random bearer token; the CLI reads the same variable automatically. `SWITCHYARD_CONTROL_READ_TOKEN` optionally grants GET/HEAD-only access. Browser deployments may instead set `SWITCHYARD_REVERSE_PROXY_AUTH=true` after an authenticated proxy reliably supplies `X-Forwarded-User` or `Remote-User`; never enable that trust flag on an independently reachable listener. Cross-origin access is disabled by default and may be allowlisted with comma-separated `SWITCHYARD_CORS_ALLOWED_ORIGINS`. Set `SWITCHYARD_PUBLIC_URL` for operator metadata. `GET /api/operator-safety` reports the proxy assumption and warns when full Open Pilot autonomy is enabled without it.

Phoenix / OTLP tracing is optional. Set `OTEL_EXPORTER_OTLP_ENDPOINT` (for example `http://10.0.0.200:6006`) plus optional `OTEL_EXPORTER_OTLP_HEADERS=x-project-name=switchyard` to export traces over OTLP/HTTP; leave the endpoint unset for no-op tracing.

Workflow execution modules are opt-in while native module parity is built:

- Changemaker: the native `changemaker-daily-digest` workflow-creator template is seeded by default and runs through workflow definitions plus the native model broker.
- Open Pilot: the `open-pilot-prototype` pipeline validates `repo`/`issue`, optionally fetches the issue from Gitea when `GITEA_BASE_URL` is configured, enforces required labels from `OPEN_PILOT_REQUIRED_LABELS`, attaches issue/prompt/branch/commit/push/PR/CI/merge/issue-close artifacts, and calls the native OpenCode executor. `OPEN_PILOT_MODEL` controls the default model; run input `model` can override it only when present in `OPEN_PILOT_ALLOWED_MODELS`. Set `OPEN_PILOT_REQUIRE_ISSUE_SPEC=true` to require issue text with acceptance/test instructions. `OPEN_PILOT_WORKTREE_MODE=disabled|dry-run|allow` controls isolated git worktree creation from `OPEN_PILOT_REPO_PATH` into `OPEN_PILOT_WORKTREE_ROOT`; `OPEN_PILOT_WORKTREE_CLEANUP=disabled|on-success` can remove a clean successful worktree after PR planning and records `open-pilot-worktree-cleanup.json`. `OPEN_PILOT_AUTO_COMMIT_MODE=disabled|dry-run|allow` controls Switchyard-owned commits after local verification and before push. `OPEN_PILOT_GIT_PUSH_MODE=disabled|dry-run|allow` controls pushing the worktree branch to `origin`. `OPEN_PILOT_GITEA_WRITE_MODE=disabled|dry-run|allow` controls PR creation; disabled/dry-run only attach PR plans. `OPEN_PILOT_CI_CHECK_MODE=disabled|poll|require-pass` controls the CI gate after AI review. `OPEN_PILOT_MERGE_MODE=disabled|dry-run|allow` controls the policy-gated merge step and `OPEN_PILOT_CLOSE_ISSUE_MODE=disabled|dry-run|allow` controls the final issue-close step. `OPEN_PILOT_REQUIRED_CHECKS` defaults to `verify,lint,test` when CI checks are enabled and may be empty only when CI checks are disabled. `OPEN_PILOT_CI_CHECK_TIMEOUT` defaults to `10m` and `OPEN_PILOT_CI_CHECK_INTERVAL` defaults to `15s`. `OPEN_PILOT_COMMAND` remains available for CLI compatibility/cancel hooks.
- Devlog: `manual-devlog` runs through the registry-backed three-step pipeline (`collect-history`, `generate-markdown`, `attach-artifact`), generates Markdown through the native `ModelBroker`, and attaches `devlog.md` as an artifact. `DEVLOG_MODEL` controls the default model; run input `model` can override it for a single run only when present in `DEVLOG_ALLOWED_MODELS`.

Open Pilot command execution is deny-by-default. If a run includes `test_command`, Switchyard evaluates it through `OPEN_PILOT_COMMAND_POLICY_MODE` (`deny`, `dry-run`, or `allow`), exact allowed command lists, allowed git verbs, denied commands, timeout, and optional workdir. Both `dry-run` and `allow` reject shell syntax and execute an argument vector directly; `allow` does not bypass the configured allowlists. When worktree mode is `allow`, verification runs in the created per-run worktree. If `OPEN_PILOT_AUTO_COMMIT_MODE=allow`, Switchyard then runs `git diff --check`, stages the worktree, commits with local Switchyard Open Pilot author metadata, records status/diffstat/name-status/commit SHA artifacts, and only then allows push. The push step records `git status --porcelain`, branch-ahead count, probes `origin/<branch>`, and records `remote_head_exists` when an externally published head already exists. `OPEN_PILOT_GIT_PUSH_MODE=allow` refuses to push when the worktree is dirty or when the branch has no commits ahead of the base branch. `OPEN_PILOT_GITEA_WRITE_MODE=allow` refuses to create a PR from a Switchyard-created worktree branch unless the push step published it first or detected an existing remote head. Cleanup defaults to disabled; `on-success` only removes clean worktrees after a durable handoff (branch pushed, remote head detected, or PR created) and skips dirty or unpublished worktrees. Use `dry-run` before enabling `allow`. PR creation is independently gated by `OPEN_PILOT_GITEA_WRITE_MODE`; keep it `disabled` until branch/test behavior is proven.

`OPEN_PILOT_AUTONOMY_MODE=full` is the single-user homelab preset for full Open Pilot autonomy. Unless overridden, it makes worktree creation `allow`, command policy `allow`, auto-commit `allow`, git push `allow`, Gitea PR creation `allow`, merge `allow`, issue close `allow`, CI checks `require-pass`, and worktree cleanup `on-success`; command and Git allowlists remain enforced. Use it only behind reverse-proxy authentication and with a repo/worktree path visible to the API container.

For Docker, `OPEN_PILOT_REPO_PATH` and `OPEN_PILOT_WORKTREE_ROOT` must point to paths visible inside the `api` container, typically via explicit bind mounts. Worktree creation uses `git worktree add -b` and fails if the target branch already exists rather than resetting existing work.

Switchyard also ships its own CLI contract:

```bash
switchyard open-pilot run --repo OWNER/REPO --issue 123 --json --api-url http://127.0.0.1:8080
switchyard open-pilot cancel --run RUN_ID --json --api-url http://127.0.0.1:8080
```

The run command waits up to 15 minutes for a terminal result, matching long-running workflow defaults. Pass `--detach` to return immediately after queueing or adjust `--wait-timeout`; the CLI authenticates with `SWITCHYARD_CONTROL_API_TOKEN`.

Run cancellation is Switchyard-side plus best-effort upstream cancellation. Switchyard uses historical compatibility IDs only when a cutover run still carries them, and `OPEN_PILOT_COMMAND cancel --run <id> --json` when configured.

Docker Compose requires an explicit `POSTGRES_PASSWORD`; generate a strong random value before startup. Postgres stores its initial credentials in the Docker volume. If you later change `POSTGRES_USER`, `POSTGRES_PASSWORD`, or `POSTGRES_DB` and see authentication failures, reset the local database volume:

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
- `GET /api/model-broker/status`
- `GET /api/opencode-executor/status`
- `GET /api/pipelines`
- `GET /api/agent-profiles`
- `GET /api/scheduler`
- `GET /api/workers`
- `POST /api/runs`
- `POST /api/runs/{id}/retry`
- `POST /api/runs/{id}/cancel`
- `POST /api/runs/{id}/steps/{step_id}/retry`
- `POST /api/runs/{id}/steps/{step_id}/cancel`
- `POST /api/triggers/{id}/run`
- `PATCH /api/triggers/{id}`

Runs persist the pipeline ID, version hash, and an immutable pipeline snapshot when queued, so later workflow-definition edits cannot change in-flight execution. Run retry creates a new run with the same workflow inputs and pipeline snapshot. Step retry creates a new run with `retry_from_step*` metadata and starts that same versioned pipeline at the requested step, preserving prior step inputs under `resume.<step-name>.*` for operator/audit context. Open Pilot rehydrates its branch plan, issue summary, agent completion, and push plan from that resume metadata so retries from verify/push/PR steps do not recreate earlier work. The Open Pilot state resume endpoint now derives its cursor from persisted DB state and records failure diagnostics (`failing_gate`, `operator_summary`, `recommended_next_action`) on failed or paused runs.

API docs live in [`docs/openapi.yaml`](docs/openapi.yaml). It is a hand-maintained OpenAPI 3.1 snapshot of the stable local control-plane API; a contract test requires registered routes and documented operations to stay in sync.

Client compatibility behavior for llama-line/Ollama/OpenAI callers is documented in [`docs/reference/client-compatibility.md`](docs/reference/client-compatibility.md).

The deployed operator dashboard is exposed as `https://switchyard.subcult.tv` through Almaz Caddy and Authelia, proxying to Kvant's web service on `100.65.164.66:28081`. Kvant binds this listener only with `SWITCHYARD_WEB_BIND_HOST=100.65.164.66`; nginx permits only Almaz's `100.77.128.80/32` proxy address, so direct access is denied. The API/control listener remains loopback-bound.

## Host service ownership

Switchyard is the owner of the local Ollama/llama-line compatible broker port (`11434`) in the native-runtime deployment. The historical standalone `llama-line.service` should not run alongside Switchyard because both services bind `11434`.

To replace a legacy standalone llama-line systemd unit with Switchyard's Docker Compose stack:

```bash
sudo ./deploy/install-systemd.sh
```

The installer renders `/etc/systemd/system/switchyard.service`, disables/removes `/etc/systemd/system/llama-line.service` when present, starts Switchyard with `docker compose up -d --remove-orphans`, and enables it at boot. Use `sudo systemctl reload switchyard` after pulling code when you want a rebuild via `docker compose up -d --build --remove-orphans`.

Model inventory and pulls:

```bash
# List installed Ollama models plus OpenCode provider/model metadata.
switchyard models list --api-url http://127.0.0.1:28080

# Pull Ollama models through Switchyard's configured OLLAMA_BASE_URL.
switchyard models pull --models qwen3.5:latest,nomic-embed-text:v1.5 --api-url http://127.0.0.1:28080

# Refresh OpenCode model metadata when the opencode CLI is available locally.
switchyard models refresh-opencode
```

The API surfaces the same inventory at `GET /api/models` and Ollama pull automation at `POST /api/models/pull`. OpenCode does not download model weights itself; it can refresh provider metadata with `opencode models --refresh`.

The web dashboard reads these endpoints directly, shows workflow/trigger/run state, and can dispatch enabled triggers with **Run now**. Its responsive operator shell groups work into Operate, Inspect, and Configure navigation, with persistent runtime status on desktop and a compact horizontal navigator on smaller screens. Shared panels, status semantics, focus states, and empty/error states keep queues, sessions, logs/search, prompts, config, agents, and skills consistent across the control plane. In Docker, nginx serves the built UI and proxies `/api/*` plus `/healthz` to the internal `api:8080` service.

See `docs/architecture/one-repo-control-plane.md` for the monorepo architecture, [`docs/production-user-stories.md`](docs/production-user-stories.md) for release acceptance stories, `docs/roadmap.md` for the next major development phase, and `docs/ui-feature-migration.md` for the imported UI feature map.

## Open Pilot

This repository keeps the Open Pilot issue template and PR template.
Bootstrap labels after creating or moving the repository:

```bash
open-pilot labels bootstrap PatrickFanella/switchyard
```

## License

GPL-3.0-only. See `LICENSE`.
