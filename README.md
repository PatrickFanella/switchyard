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
make api-soak-extended # run the release-grade 15-minute API soak
make alert-lifecycle  # manually verify Alertmanager-to-ntfy firing and resolution
make production-config-check # validate exported production transport settings
make workspace-init    # safely prepare the ignored local executor workspace
```

See [`docs/status.md`](docs/status.md) for the current capability and release-evidence state. Dated readiness reports prove only the revision they name.

## Verification gates

`make verify` is the required baseline gate for every push and pull request; it is run by the `switchyard-check` Gitea workflow. `make verify-production` is the full release-qualification gate, available manually and on the weekly `switchyard-production-verify` workflow schedule.

Both workflows target the dedicated `switchyard-production` runner label. Its reproducible job image is defined in `deploy/runner/Dockerfile` and must be published and configured by immutable digest. The baseline CI runner contract is Go `1.26.6`, Node.js major `24`, Corepack-provided pnpm `10.33.0`, Make, Docker Engine, and the Docker Compose plugin. The production runner contract adds Docker Buildx with a bootstrappable BuildKit builder that can produce an OCI archive with the pinned SBOM generator and `--provenance=mode=max` options used by `make release-artifacts`. Both workflows validate these contracts before running Make; CI supplies only the non-secret `POSTGRES_PASSWORD=switchyard-ci-only-not-a-secret` needed for clean-checkout Compose validation. No CI setup action supplies these tools.

Production releases are pinned OCI archives with BuildKit SBOM and provenance attestations. `make release-verify` writes and verifies `release/SHA256SUMS` and records the source revision in `release/manifest.json`; generated release artifacts remain outside git. Runtime logs are newline-delimited JSON with credential-shaped values redacted. `GET /metrics` exports Prometheus-format workflow, model-request, queue, latency, worker, backup-freshness, and restore-drill deadline/result metrics.

`make api-soak` runs a 30-second concurrent authenticated API soak by default. Increase `SWITCHYARD_SOAK_DURATION`, `SWITCHYARD_SOAK_CLIENTS`, and `SWITCHYARD_SOAK_MIN_REQUESTS` for extended release qualification.

Production promotion also requires a verified backup/restore drill and the real target contract smoke:

```bash
SWITCHYARD_BACKUP_DIR=/approved/encrypted/backups \
SWITCHYARD_BACKUP_POLICY=/approved/non-secret/backup-policy.json \
make postgres-backup
SWITCHYARD_RESTORE_BUNDLE=/approved/encrypted/backups/<bundle>.complete.json \
SWITCHYARD_RESTORE_ACTIVE_POLICY=/approved/non-secret/backup-policy.json \
SWITCHYARD_RESTORE_EVIDENCE_DIR=/approved/private/restore-evidence \
SWITCHYARD_RESTORE_TIMEOUT_SECONDS=900 \
make restore-drill
make production-smoke
```

Backups publish a private archive, archived policy and object contract,
metadata, then a completion manifest last. Restore evidence is
provider-neutral; PostgreSQL is the only
approved adapter. RPO and isolated-drill RTO comparisons qualify the active
policy but do not prove production recovery performance.

For backup-freshness metrics, keep
`SWITCHYARD_BACKUP_METRICS_DIR` separate from backup archives and set
`SWITCHYARD_BACKUP_METRICS_FILE` to its `backup.prom` file. Compose mounts the
directory read-only so atomic metric updates remain visible to the API.
Scheduled restore drills are opt-in. Their systemd calendar, maximum interval,
and alert grace come only from the validated operator policy. See the backup and
restore runbook for directory rules, installation, alerts, and rollback.

Follow [`docs/runbooks/release-and-rollback.md`](docs/runbooks/release-and-rollback.md); do not infer production health from a local checkout or a running container.

The opt-in `make alert-lifecycle` check posts a uniquely correlated firing alert
to Alertmanager, observes delivery through the configured ntfy topic, resolves
the same labels, and observes the recovery notification. It never publishes to
ntfy. Set `SWITCHYARD_ALERT_LIFECYCLE_TEST=1` and the required values documented
in [the incident runbook](docs/runbooks/incident-response.md). `make verify`
runs only deterministic fake-server tests; it never runs the live check.

Run `make api-run` and `make web-dev` in separate terminals for local dashboard work. The Vite dev server proxies `/api` and `/healthz` to `http://127.0.0.1:8080`; set `VITE_SWITCHYARD_API_URL` only when the API is hosted elsewhere.

Prepare the default ignored executor workspace before starting Docker. For the
empty default relative path, the command initializes a `main` Git repository and
creates its private managed worktree root and marker. Absolute production paths
must already be valid Git top-levels with a non-symlink `.git` directory. The
command refuses nonempty non-Git paths, unsafe modes, and configured UID/GID
ownership mismatches. It never changes ownership. `make docker-up` runs this
preparation automatically:

```bash
make workspace-init
```

Docker port defaults are configurable through `.env` or inline variables:

```bash
WEB_PORT=8081 API_PORT=18080 POSTGRES_PORT=5432 make docker-up
```

Published ports bind to `127.0.0.1` by default. `SWITCHYARD_BIND_HOST` controls the API and Postgres, `SWITCHYARD_WEB_BIND_HOST` controls the web UI, and `SWITCHYARD_MODEL_BIND_HOST` controls model-compatible ingress. Keep API/control and Postgres loopback-bound. Local development uses `SWITCHYARD_WEB_TRANSPORT=http`.

Production uses three network trust layers. Set `SWITCHYARD_WEB_BIND_HOST` to this host's Tailscale IPv4 and `SWITCHYARD_TAILSCALE_PROXY_IPV4` to the distinct proxy peer in `100.64.0.0/10`. The host nft gate checks that source before Docker DNAT. UFW allows only that source from `tailscale0` to pinned `SWITCHYARD_WEB_CONTAINER_IPV4:80`, plus pinned `SWITCHYARD_API_CONTAINER_IPV4/32` to `SWITCHYARD_DOCKER_BRIDGE_GATEWAY/32` TCP/11435. Compose pins both addresses on `SWITCHYARD_DOCKER_BRIDGE_NAME`. Production `OLLAMA_BASE_URL` must exactly equal `http://<bridge-gateway>:11435`; aliases, LAN or wildcard addresses, alternate ports, and trailing slashes fail validation. Direct Tailscale traffic reaches nginx with the original peer, so `SWITCHYARD_WEB_TRUSTED_PROXY_CIDR` must equal `SWITCHYARD_TAILSCALE_PROXY_IPV4/32`. The API requires the shared hop token and canonical identity. Do not broaden either UFW rule to a subnet, add a plaintext LAN fallback, or put Tailscale credentials in Compose.

Install the host gate before the main unit: `sudo ./deploy/install-web-gate.sh`, then `sudo ./deploy/install-systemd.sh`. UFW must already be active. Set `SWITCHYARD_ENV_FILE` to the same absolute regular non-symlink file for both. Every Docker-consuming service gets the `docker` supplementary group without changing the run user's account memberships; set `SWITCHYARD_DOCKER_GROUP` when the Docker socket uses another group. All production Docker calls use `deploy/docker-local`, which runs `/usr/bin/docker` against `unix:///var/run/docker.sock` with a fresh empty CLI config and clean environment. Production does not load per-user Docker config or registry credentials. All external runtime and Dockerfile base images are anonymously pullable public images; the API, web, verifier, and OpenCode service images are built locally. Docker Compose must therefore be installed system-wide, normally at `/usr/libexec/docker/cli-plugins/docker-compose` or a distro-equivalent location discovered by `/usr/bin/docker compose`. Before changing units or workloads, the installer probes both `docker info` and `docker compose version` through the canonical boundary with the rendered run user, primary group, and Docker supplementary group. The installer publishes root-owned helper and nft files, and manages distinct `switchyard-web-gate` and `switchyard-model-gate` rules. Failure deletes only rules created by that invocation, restores prior gate state, and leaves web stopped. The gate service ensures both rules before Docker; the main service verifies both before Compose start or reload.

Before a production smoke, export the deployed `.env` values and run `make production-config-check`, or validate the file directly with `python3 scripts/validate_production_config.py --env-file .env`. Environment variables override file values. The parser does not perform shell expansion or print values. The check requires strict OpenCode mode; a non-default OpenCode password; positive numeric runtime UID/GID; an existing absolute dedicated Git top-level workspace outside, and not containing, this repository; a non-symlink `.git` directory; a private, owned, non-symlink managed worktree root and marker; canonical `/workspace` repo/worktree container paths; an HTTPS `SWITCHYARD_PUBLIC_URL`; reverse-proxy authentication with a non-default hop token of at least 32 bytes; literal safe listener binds; integer host ports; and distinct effective bind-and-port pairs. The model bind may use any specific IPv4 address required by model clients. The check validates configuration only. Gitea issues #18 and #19 still require human action and target-path evidence.

The production smoke uses `SWITCHYARD_PUBLIC_URL` for the public probe. `SWITCHYARD_PRODUCTION_PUBLIC_URL`, when set, must match it. Unless explicit `SWITCHYARD_PRODUCTION_CONTROL_URL` or `SWITCHYARD_PRODUCTION_MODEL_URL` overrides are set, the smoke derives local probe URLs from the validated API and model bind and port settings. On hosts with systemd and readable nft state, it also checks the gate service and packet counters.

Production requires `SWITCHYARD_DASHBOARD_CIDR` to identify one loopback IPv4 address with `/32`; it cannot admit a remote peer. For LAN clients, set `SWITCHYARD_MODEL_BIND_HOST` to the host's LAN address to expose only model-compatible ingress. The control-plane API and model-compatible ingress use separate in-container listeners; the model port does not serve run, trigger, workflow, history, audit, or administrative routes.

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

Strict OpenCode startup atomically rewrites persistent `auth.json` with mode
`0600`. It preserves structurally valid API/OAuth provider credentials,
including OpenAI and OpenRouter credentials, while removing well-known,
unknown, and unrelated account state without logging values. Malformed or
unsafe auth state fails startup closed. Strict mode rejects
`OPENCODE_AUTH_CONTENT`; development mode leaves auth state untouched.

The llama-line/Ollama/OpenAI-compatible ingress is supported. Only external cutover selectors are deprecated: `MODEL_BROKER_BACKEND=llama-line` and nested `OPENCODE_EXECUTOR_BACKEND=ocq`; native defaults and Ollama routing remain supported. Actual deprecated generation attempts expose `X-Switchyard-Backend` and `X-Switchyard-Compatibility-Warning: deprecated-external-backend` before JSON/SSE commitment, while async/workflow attempts persist namespaced decision correlations and events. `/broker/status` reports `effective_selector` plus the optional raw `configured_selector`; `/api/config` reports effective broker/executor selectors plus their optional raw configured values. Unknown or blank selectors route as `native`, and warnings appear only for llama-line or nested OCQ selection; no standard HTTP deprecation headers are emitted from status/config. Selectors will not be removed before v1.0 and require migration docs, at least 90 days, and two consecutive releases with zero observed deprecated-backend correlations/events; removal may then occur in v1.0 or a later major release. Ingress remains supported. External Changemaker adapter probes remain read-only migration visibility and do not affect the native Changemaker pipeline.
- The OpenCode image includes the basic Switchyard toolchain (`git`, Go, Make, Node/npm/Corepack, and shell utilities) for runtime integration, but strict managed agents cannot invoke shell commands. They use file tools; Switchyard sends approved verification argv to the isolated verifier. The server runs as `OPENCODE_RUNTIME_UID:OPENCODE_RUNTIME_GID`, with a read-only root filesystem, dropped capabilities, scoped Git safe directories, controlled persistent XDG paths, and no MCP services, sharing, automatic updates, floating packages, or global command allow by default. Strict bootstrap never parses persisted `opencode.json`; it purges unmanaged plugin, agent, command, mode, skill, Blacktower, and instruction surfaces before installing the image-managed defaults. The resulting config cannot inherit persisted agents, default agents, permissions, plugins, MCPs, or instructions. Strict mode rejects `OPENCODE_CONFIG`, `OPENCODE_CONFIG_CONTENT`, and `OPENCODE_PERMISSION`, requires the managed XDG config directory, `OPENCODE_DB=:memory:`, `OPENCODE_DISABLE_PROJECT_CONFIG=true`, and `OPENCODE_DISABLE_CLAUDE_CODE_PROMPT=true`. Restarting OpenCode therefore loses its session/account DB; Switchyard creates replacement sessions when work resumes. Only explicit local `development` mode may use a persisted DB or enable project or Claude prompt discovery. `HOME` and every XDG discovery root remain fixed at image-owned or dedicated mounted paths under `/home/opencode`; no host home or global config is mounted. The separate verifier image adds pinned Go 1.26, Make, Cargo, and pnpm 10.33.0 to the API image's Git, Node, and npm tools. Its internal-only service has process, memory, CPU, concurrency, output, and snapshot caps. Direct `git` verification stays fd-bound to the validated linked worktree. Every non-Git command runs in a private writable `/tmp` snapshot, rejects symlinks/special files, and cannot alter the source worktree. It supports dependency-free or already-cached checks; arbitrary dependency installation remains unsupported.
- Only the API mounts the full `OPENCODE_WORKSPACE` at `/workspace`. OpenCode mounts `${OPENCODE_WORKSPACE}/.git` read-only and the managed worktree subtree read-write. The verifier mounts both read-only. Neither service mounts the source checkout; the narrow Git-common-directory mount preserves linked-worktree discovery and read-only diff/status operations. Both set `GIT_OPTIONAL_LOCKS=0` and disable terminal Git prompts. OpenCode's strict last-match command policy denies Git by default, permits only named read operations, and explicitly denies mutation and network verbs. The persistent OpenCode server can still see sibling managed worktrees; true per-run mount namespaces require a separate executor lifecycle and remain outside this design. Local Compose defaults to ignored `./tmp/opencode-workspace`. In production set an existing absolute dedicated Git top-level outside this repository, owned by the configured runtime UID/GID, run `make workspace-init` to validate and create the managed layout without changing ownership, and keep `OPEN_PILOT_REPO_PATH=/workspace` plus `OPEN_PILOT_WORKTREE_ROOT=/workspace/.open-pilot-worktrees`. Bind mounts refuse missing host sources instead of letting Docker create root-owned directories. Production `OPENCODE_CONFIG_DIR` and `OPENCODE_DATA_DIR` must also be canonical, existing, dedicated absolute directories outside the repository and workspace, with runtime ownership and safe modes. The entrypoint fails closed on mismatched bind mounts and never recursively chowns host paths. Override `OPENCODE_SERVER_URL=http://host.docker.internal:4096` only when deliberately using a separately hardened host server.
- The verifier handles one request at a time. Each response binds regular-file bytes, paths, and Git executable modes to a SHA-256 manifest. The test command, direct `git diff --check`, and pre-commit manifest must match. The verifier kills and reaps escaped background processes after every request.
- External `BLACKTOWER_BASE_URL` probes remain optional/reference-only. Changemaker is now native workflow-template policy, not an external runtime dependency.

For live cutover operation, rollback, and smoke commands, see [`docs/runbooks/native-model-broker-cutover.md`](docs/runbooks/native-model-broker-cutover.md).

The in-process trigger scheduler is enabled by default; set `SWITCHYARD_SCHEDULER_ENABLED=false` to disable it. It supports numeric five-field cron lists, ranges, and steps across minute, hour, day-of-month, month, and day-of-week. `SWITCHYARD_SCHEDULER_TIMEZONE` defaults to `UTC`. Missed occurrences coalesce into one run, after which the next future occurrence is scheduled. `SWITCHYARD_SCHEDULER_INTERVAL` defaults to `1m`.

Async worker dispatch is enabled by default, so `POST /api/runs`, trigger runs, retries, and Open Pilot dispatch return `202 Accepted` with queued state. Set `SWITCHYARD_WORKER_ENABLED=false` only for local synchronous debugging. Workers claim runs using durable leases and retry failed or timed-out runs up to `SWITCHYARD_RUN_MAX_ATTEMPTS` (default `3`) with exponential backoff bounded by `SWITCHYARD_RUN_RETRY_MAX_BACKOFF`; `SWITCHYARD_RUN_TIMEOUT` defaults to `10m`. Configure concurrency with `SWITCHYARD_WORKER_COUNT`, `SWITCHYARD_WORKER_LEASE`, and `SWITCHYARD_WORKER_POLL_INTERVAL`. On termination, new claims stop and active work drains for `SWITCHYARD_SHUTDOWN_GRACE` before forced cancellation. Use `SWITCHYARD_EXECUTOR_LIMITS` for optional per-executor pipeline concurrency caps such as `model=1,opencode=1,command=2,git=1,gitea=2,http=2`; omitted or invalid entries are ignored.

Asynchronous model-compatible requests use a durable priority queue. `MODEL_BROKER_QUEUE_MAX_DEPTH` (default `100`, maximum `200`) bounds queued, running, and cancel-requested work; full queues return `429` with `Retry-After`. `MODEL_BROKER_QUEUE_WORKERS` controls concurrent claims. Claims order higher client priority first and FIFO within equal priority, using atomic row locking under Postgres.

List runs with bounded cursor pagination using `GET /api/runs?limit=50&cursor=...`; the response includes `next_cursor`. `GET /readyz` checks persistence rather than only process liveness. Terminal runs and model requests are pruned after `SWITCHYARD_RETENTION` (default `720h`) on `SWITCHYARD_RETENTION_INTERVAL`. Request, prompt, model-response, and artifact sizes are bounded by the corresponding `SWITCHYARD_MAX_*_BYTES` settings, model history redacts credential-shaped values before persistence, and per-client request rate is capped by `SWITCHYARD_RATE_LIMIT_PER_SECOND`.

Workflow-definition PUTs create immutable draft versions. Validate and publish the latest draft with `POST /api/workflow-definitions/{id}/activate`, inspect history at `GET .../{id}/versions`, and restore an earlier snapshot with `POST .../{id}/rollback` and `{"version": N}`; rollback creates a new active version rather than rewriting history. Generic HTTP steps are supported only when the definition opts in with `allow_http=true`, the target host matches `SWITCHYARD_HTTP_ALLOWED_HOSTS`, and HTTPS is used (`SWITCHYARD_HTTP_ALLOW_INSECURE=true` is intended only for controlled local testing). Redirects, methods, timeout, and response size are constrained. Compatibility feedback now requires `request_id`, a rating from `-1` to `1`, and an optional redacted comment, and is durably queryable from `/admin/feedback`.

Validated webhook triggers use a third, webhook-only HMAC listener that is disabled and unpublished by default. It never exposes control, model, health, metrics, history, or admin routes. See [Webhook ingress](docs/webhook-ingress.md) for private-file key configuration, canonical signatures, scalar projections, rotation, bounds, and replay semantics. Its isolated contract is [openapi-webhook-ingress.yaml](docs/openapi-webhook-ingress.yaml).

Every control-plane request except health checks requires authentication. Set `SWITCHYARD_CONTROL_API_TOKEN` to a long random bearer token; the CLI reads the same variable automatically. `SWITCHYARD_CONTROL_READ_TOKEN` optionally grants GET/HEAD-only access: on the control listener, every supported GET route also supports HEAD as its bodyless alias with the same authentication. Browser deployments may instead set `SWITCHYARD_REVERSE_PROXY_AUTH=true` and a random `SWITCHYARD_REVERSE_PROXY_TOKEN` of at least 32 bytes after restricting the listener's network source to the trusted proxy. Proxy admin authentication requires the `X-Switchyard-Proxy-Token` hop header plus `Remote-User` or `X-Forwarded-User`; conflicting identity headers are rejected. Bearer authentication is unchanged. Never enable proxy trust on an independently reachable listener. Cross-origin access is disabled by default and may be allowlisted with comma-separated `SWITCHYARD_CORS_ALLOWED_ORIGINS`. Set `SWITCHYARD_PUBLIC_URL` for operator metadata. `GET /api/operator-safety` reports the proxy assumption and warns when full Open Pilot autonomy is enabled without it.

Phoenix / OTLP tracing is optional. Set `OTEL_EXPORTER_OTLP_ENDPOINT` (for example `http://10.0.0.200:6006`) plus optional `OTEL_EXPORTER_OTLP_HEADERS=x-project-name=switchyard` to export traces over OTLP/HTTP; leave the endpoint unset for no-op tracing.

Workflow execution modules are opt-in while native module parity is built:

- Changemaker: the native `changemaker-daily-digest` workflow-creator template is seeded by default and runs through workflow definitions plus the native model broker.
- Open Pilot: the `open-pilot-prototype` pipeline validates `repo`/`issue`, optionally fetches the issue from Gitea when `GITEA_BASE_URL` is configured, enforces required labels from `OPEN_PILOT_REQUIRED_LABELS`, attaches issue/prompt/branch/commit/push/PR/CI/merge/issue-close artifacts, and calls the native OpenCode executor. The disabled-by-default `gitea-open-pilot-poll` trigger discovers open issues only in the `OPEN_PILOT_POLL_REPOS` allowlist, re-fetches each issue, requires every configured label, and atomically claims each repo/issue once before queueing. `OPEN_PILOT_MODEL` controls the default model; run input `model` can override it only when present in `OPEN_PILOT_ALLOWED_MODELS`. Set `OPEN_PILOT_REQUIRE_ISSUE_SPEC=true` to require issue text with acceptance/test instructions. `OPEN_PILOT_WORKTREE_MODE=disabled|dry-run|allow` controls isolated git worktree creation from `OPEN_PILOT_REPO_PATH` into `OPEN_PILOT_WORKTREE_ROOT`; `OPEN_PILOT_WORKTREE_CLEANUP=disabled|on-success` can remove a clean successful worktree after PR planning and records `open-pilot-worktree-cleanup.json`. `OPEN_PILOT_AUTO_COMMIT_MODE=disabled|dry-run|allow` controls Switchyard-owned commits after local verification and before push. `OPEN_PILOT_GIT_PUSH_MODE=disabled|dry-run|allow` controls pushing the worktree branch to `origin`. `OPEN_PILOT_GITEA_WRITE_MODE=disabled|dry-run|allow` controls PR creation; disabled/dry-run only attach PR plans. `OPEN_PILOT_CI_CHECK_MODE=disabled|poll|require-pass` controls the CI gate after AI review. `OPEN_PILOT_MERGE_MODE=disabled|dry-run|allow` controls the policy-gated merge step and `OPEN_PILOT_CLOSE_ISSUE_MODE=disabled|dry-run|allow` controls the final issue-close step. `OPEN_PILOT_REQUIRED_CHECKS` defaults to `verify,lint,test` when CI checks are enabled and may be empty only when CI checks are disabled. `OPEN_PILOT_CI_CHECK_TIMEOUT` defaults to `10m` and `OPEN_PILOT_CI_CHECK_INTERVAL` defaults to `15s`. `OPEN_PILOT_COMMAND` remains available for CLI compatibility/cancel hooks.
- Devlog: `manual-devlog` runs through the registry-backed three-step pipeline (`collect-history`, `generate-markdown`, `attach-artifact`), generates Markdown through the native `ModelBroker`, and attaches `devlog.md` as an artifact. `DEVLOG_MODEL` controls the default model; run input `model` can override it for a single run only when present in `DEVLOG_ALLOWED_MODELS`.

Open Pilot command execution is deny-by-default. If a run includes `test_command`, Switchyard evaluates it through `OPEN_PILOT_COMMAND_POLICY_MODE` (`deny`, `dry-run`, or `allow`), exact allowed command lists, allowed git verbs, denied commands, timeout, and optional workdir. Both `dry-run` and `allow` reject shell syntax and execute an argument vector directly; `allow` does not bypass the configured allowlists. When worktree mode is `allow`, verification runs in the created per-run worktree. If `OPEN_PILOT_AUTO_COMMIT_MODE=allow`, Switchyard then runs `git diff --check`, stages the worktree, commits with local Switchyard Open Pilot author metadata, records status/diffstat/name-status/commit SHA artifacts, and only then allows push. The push step records `git status --porcelain`, branch-ahead count, probes `origin/<branch>`, and records `remote_head_exists` when an externally published head already exists. Normal push and remote probes require a sanitized HTTPS `origin` whose host/path match `GITEA_BASE_URL` and the requested repository, regardless of whether `GITEA_TOKEN` is set. `OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE=true` permits absolute local bare remotes only for tests and is rejected by production validation. Production push allow also requires `GITEA_TOKEN`. Git subprocesses disable hooks, signing, command helpers, terminal prompts, protocol extensions, redirecting config, and TLS weakening. `OPEN_PILOT_GIT_PUSH_MODE=allow` refuses to push when the worktree is dirty or when the branch has no commits ahead of the base branch. `OPEN_PILOT_GITEA_WRITE_MODE=allow` refuses to create a PR from a Switchyard-created worktree branch unless the push step published it first or detected an existing remote head. Cleanup defaults to disabled; `on-success` only removes clean worktrees after a durable handoff (branch pushed, remote head detected, or PR created) and skips dirty or unpublished worktrees. Use `dry-run` before enabling `allow`. PR creation is independently gated by `OPEN_PILOT_GITEA_WRITE_MODE`; keep it `disabled` until branch/test behavior is proven.

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
- `GET /api/model-broker/queue`
- `GET /api/model-broker/requests`
- `GET /api/model-broker/requests/{id}`
- `POST /api/model-broker/requests/{id}/cancel`
- `POST /api/model-broker/requests/{id}/drop`
- `DELETE /api/model-broker/requests`
- `GET /api/opencode-executor/status`
- `GET /api/operator-summary`
- `GET /api/operator-safety`
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

API docs live in [`docs/openapi.yaml`](docs/openapi.yaml). It is a hand-maintained OpenAPI 3.1 snapshot of the stable local control-plane API; a contract test requires registered routes and documented operations to stay in sync. Public model-listener operations whose security differs from the control listener are specified in [`docs/openapi-model-ingress.yaml`](docs/openapi-model-ingress.yaml).

Client compatibility behavior for llama-line/Ollama/OpenAI callers is documented in [`docs/reference/client-compatibility.md`](docs/reference/client-compatibility.md).
Model lifecycle routes are authenticated control-plane routes only. Queue health is reported separately from upstream-provider health. Request states are `queued`, `running`, `cancel_requested`, `completed`, `failed`, `canceled`, and `dropped`; terminal states are immutable.

The public operator dashboard remains an HTTPS route protected by Caddy and Authelia. Caddy must strip inbound `Remote-User`, `X-Forwarded-User`, and `X-Switchyard-Proxy-Token`; only after successful Authelia authentication may it set canonical `Remote-User` and the configured hop token. The approved private web hop is host-managed Tailscale, with nginx restricted to the proxy peer's exact Tailscale `/32`. The API/control listener remains loopback-bound. Repository validation does not prove that the tailnet ACL, deployed route, or Caddy header policy works.

## Host service ownership

Switchyard is the owner of the local Ollama/llama-line compatible broker port (`11434`) in the native-runtime deployment. The historical standalone `llama-line.service` should not run alongside Switchyard because both services bind `11434`.

To replace a legacy standalone llama-line systemd unit with Switchyard's Docker Compose stack:

```bash
sudo ./deploy/install-systemd.sh
```

The installer renders `/etc/systemd/system/switchyard.service`, disables/removes `/etc/systemd/system/llama-line.service` when present, validates the repository `.env`, starts Switchyard with that file, and enables it at boot. Use `sudo systemctl reload switchyard` after pulling code; reload validates `.env` again before rebuilding the Compose stack.

Model inventory and pulls:

```bash
# List installed Ollama models plus OpenCode provider/model metadata.
switchyard models list --api-url http://127.0.0.1:18080

# Pull Ollama models through Switchyard's configured OLLAMA_BASE_URL.
switchyard models pull --models qwen3.5:latest,nomic-embed-text:v1.5 --api-url http://127.0.0.1:18080

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
