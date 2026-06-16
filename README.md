# Switchyard

Switchyard is a Go backend and Vite React TypeScript monorepo for a cohesive automation runtime: workflow orchestration, model routing, OpenCode execution, built-in agent profiles, operator HUD/API, and first-party workflows such as Open Pilot, Changemaker-style content generation, and Devlog.

The product direction is not to tape together the old OCQ, llama-line, Blacktower, and Changemaker apps. Those codebases are references and transitional scaffolding. Switchyard should rebuild their useful ideas as native modules: model broker, OpenCode executor, profiles/personas, workflow policy, logs, artifacts, and operator controls.

## Monorepo layout

```txt
backend/              Switchyard API, scheduler, runs, triggers, native runtime
web/                  Switchyard HUD dashboard
services/ocq/         transitional/reference OpenCode gateway implementation
services/llama-line/  transitional/reference model broker implementation
workflows/changemaker/ transitional/reference content workflow policy
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

The local stack currently runs transitional OCQ and llama-line sidecars while their behavior is rebuilt as native Switchyard modules:

- The root Docker stack now includes local `llama-line` and OCQ gateway services. By default the API uses `LLAMA_LINE_BASE_URL=http://llama-line:11434` and `OCQ_BASE_URL=http://ocq:8088`; host ports default to `LLAMA_LINE_PORT=21434` and `OCQ_PORT=8088`.
- Workflow code now targets Switchyard-native `ModelBroker` and `OpenCodeExecutor` contracts. The current env-backed model broker routes to transitional llama-line while native queue/routing/health features are absorbed. The current OpenCode executor routes through transitional OCQ while session/correlation/cancellation behavior is rebuilt natively.
- The root Docker stack launches a local OpenCode server sidecar and OCQ forwards prompts through `OPENCODE_SERVER_URL` (default `http://opencode:4096`). The OpenCode image includes the basic Switchyard toolchain (`git`, Go, Make, Node/npm/Corepack, shell utilities), mounts `OPENCODE_WORKSPACE` (default `.`) at `/workspace`, mounts `OPENCODE_CONFIG_DIR` (default ignored `./.opencode`) at `/root/.config/opencode`, and mounts `OPENCODE_DATA_DIR` (default ignored `./opencode-data`) at `/root/.local/share/opencode` for auth data such as `auth.json`. The API mounts the same `OPENCODE_WORKSPACE` at `/workspace` so Open Pilot can create isolated worktrees that OpenCode can edit; API/OpenCode containers mark mounted workspaces as Git safe directories to avoid UID-mismatch failures. Set those paths to your host OpenCode directories to reuse `opencode auth login` credentials. It stays internal to the Compose network. Override `OPENCODE_SERVER_URL=http://host.docker.internal:4096` only when deliberately using a host-run OpenCode server. `OPENCODE_EXECUTOR_TIMEOUT` and `MODEL_BROKER_TIMEOUT` default to `10m` for long-running agent prompts and model-broker gates.
- External `BLACKTOWER_BASE_URL` and `CHANGEMAKER_BASE_URL` probes are legacy/reference-only. `CHANGEMAKER_BASE_URL` is used only by the transitional `changemaker-wrapper`; the native Changemaker template runs through Switchyard workflow definitions and the model broker.

Set `SWITCHYARD_SCHEDULER_ENABLED=true` to enable the in-process trigger scheduler. `SWITCHYARD_SCHEDULER_INTERVAL` defaults to `1m`.

Set `SWITCHYARD_WORKER_ENABLED=true` to enable async worker dispatch for long-running workflows. When enabled, `POST /api/runs`, trigger runs, retries, and Open Pilot dispatch return `202 Accepted` with queued run state; workers claim runs using durable leases. Configure with `SWITCHYARD_WORKER_COUNT`, `SWITCHYARD_WORKER_LEASE`, and `SWITCHYARD_WORKER_POLL_INTERVAL`. Use `SWITCHYARD_EXECUTOR_LIMITS` for optional per-executor pipeline concurrency caps such as `model=1,opencode=1,command=2,git=1,gitea=2,http=2`; omitted or invalid entries are ignored.

For public access, put Switchyard behind reverse-proxy authentication such as Caddy + Authelia or Cloudflare Access. Set `SWITCHYARD_PUBLIC_URL` for operator metadata and set `SWITCHYARD_REVERSE_PROXY_AUTH=true` only after the proxy protects the public URL. `GET /api/operator-safety` reports this assumption and warns when full Open Pilot autonomy is enabled without the reverse-proxy auth flag.

Workflow execution modules are opt-in while native module parity is built:

- Changemaker: the native `changemaker-daily-digest` workflow-creator template is seeded by default. `CHANGEMAKER_BASE_URL` remains a legacy fallback only for the transitional `changemaker-wrapper` pipeline.
- Open Pilot: the `open-pilot-prototype` pipeline validates `repo`/`issue`, optionally fetches the issue from Gitea when `GITEA_BASE_URL` is configured, enforces required labels from `OPEN_PILOT_REQUIRED_LABELS`, attaches issue/prompt/branch/commit/push/PR/CI/merge/issue-close artifacts, and calls OCQ through the OpenCode executor when `OCQ_BASE_URL` is configured. `OPEN_PILOT_MODEL` controls the default model; run input `model` can override it only when present in `OPEN_PILOT_ALLOWED_MODELS`. Set `OPEN_PILOT_REQUIRE_ISSUE_SPEC=true` to require issue text with acceptance/test instructions. `OPEN_PILOT_WORKTREE_MODE=disabled|dry-run|allow` controls isolated git worktree creation from `OPEN_PILOT_REPO_PATH` into `OPEN_PILOT_WORKTREE_ROOT`; `OPEN_PILOT_WORKTREE_CLEANUP=disabled|on-success` can remove a clean successful worktree after PR planning and records `open-pilot-worktree-cleanup.json`. `OPEN_PILOT_AUTO_COMMIT_MODE=disabled|dry-run|allow` controls Switchyard-owned commits after local verification and before push. `OPEN_PILOT_GIT_PUSH_MODE=disabled|dry-run|allow` controls pushing the worktree branch to `origin`. `OPEN_PILOT_GITEA_WRITE_MODE=disabled|dry-run|allow` controls PR creation; disabled/dry-run only attach PR plans. `OPEN_PILOT_CI_CHECK_MODE=disabled|poll|require-pass` controls the CI gate after AI review. `OPEN_PILOT_MERGE_MODE=disabled|dry-run|allow` controls the policy-gated merge step and `OPEN_PILOT_CLOSE_ISSUE_MODE=disabled|dry-run|allow` controls the final issue-close step. `OPEN_PILOT_REQUIRED_CHECKS` defaults to `verify,lint,test` when CI checks are enabled and may be empty only when CI checks are disabled. `OPEN_PILOT_CI_CHECK_TIMEOUT` defaults to `10m` and `OPEN_PILOT_CI_CHECK_INTERVAL` defaults to `15s`. `OPEN_PILOT_COMMAND` remains available for CLI compatibility/cancel hooks.
- Devlog: `manual-devlog` runs through the registry-backed three-step pipeline (`collect-history`, `generate-markdown`, `attach-artifact`), generates Markdown through the native `ModelBroker` when `LLAMA_LINE_BASE_URL` is configured, and attaches `devlog.md` as an artifact. `DEVLOG_MODEL` controls the default model; run input `model` can override it for a single run only when present in `DEVLOG_ALLOWED_MODELS`.

Open Pilot command execution is deny-by-default. If a run includes `test_command`, Switchyard evaluates it through `OPEN_PILOT_COMMAND_POLICY_MODE` (`deny`, `dry-run`, or `allow`), exact allowed command lists, allowed git verbs, denied commands, timeout, and optional workdir. When worktree mode is `allow`, verification runs in the created per-run worktree. If `OPEN_PILOT_AUTO_COMMIT_MODE=allow`, Switchyard then runs `git diff --check`, stages the worktree, commits with local Switchyard Open Pilot author metadata, records status/diffstat/name-status/commit SHA artifacts, and only then allows push. The push step records `git status --porcelain`, branch-ahead count, probes `origin/<branch>`, and records `remote_head_exists` when an externally published head already exists. `OPEN_PILOT_GIT_PUSH_MODE=allow` refuses to push when the worktree is dirty or when the branch has no commits ahead of the base branch. `OPEN_PILOT_GITEA_WRITE_MODE=allow` refuses to create a PR from a Switchyard-created worktree branch unless the push step published it first or detected an existing remote head. Cleanup defaults to disabled; `on-success` only removes clean worktrees after a durable handoff (branch pushed, remote head detected, or PR created) and skips dirty or unpublished worktrees. Use `dry-run` before enabling `allow`. PR creation is independently gated by `OPEN_PILOT_GITEA_WRITE_MODE`; keep it `disabled` until branch/test behavior is proven.

`OPEN_PILOT_AUTONOMY_MODE=full` is the single-user homelab preset for full Open Pilot autonomy. Unless overridden, it makes worktree creation `allow`, command policy `allow`, auto-commit `allow`, git push `allow`, Gitea PR creation `allow`, merge `allow`, issue close `allow`, CI checks `require-pass`, and worktree cleanup `on-success`; command allow/deny lists become unrestricted by default while shell metacharacter rejection remains. Use it only behind reverse-proxy authentication and with a repo/worktree path visible to the API container.

For Docker, `OPEN_PILOT_REPO_PATH` and `OPEN_PILOT_WORKTREE_ROOT` must point to paths visible inside the `api` container, typically via explicit bind mounts. Worktree creation uses `git worktree add -b` and fails if the target branch already exists rather than resetting existing work.

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

Run retry creates a new run with the same workflow inputs. Step retry creates a new run with `retry_from_step*` metadata and starts registry-backed pipelines at the requested step, preserving prior step inputs under `resume.<step-name>.*` for operator/audit context. Open Pilot rehydrates its branch plan, issue summary, agent completion, and push plan from that resume metadata so retries from verify/push/PR steps do not recreate earlier work. The Open Pilot state resume endpoint now derives its cursor from persisted DB state and records failure diagnostics (`failing_gate`, `operator_summary`, `recommended_next_action`) on failed or paused runs.

API docs live in [`docs/openapi.yaml`](docs/openapi.yaml). It is a hand-maintained OpenAPI 3.1 snapshot of the stable local control-plane API; update it when route shapes or response envelopes change.

The web dashboard reads these endpoints directly, shows workflow/trigger/run state, and can dispatch enabled triggers with **Run now**. It also includes HUD-styled module surfaces for queues, sessions, logs/search, prompts, config, agents, and skills so model brokering, OpenCode execution, content workflows, and profiles feel native to Switchyard instead of separate consoles. In Docker, nginx serves the built UI and proxies `/api/*` plus `/healthz` to the internal `api:8080` service.

See `docs/architecture/one-repo-control-plane.md` for the monorepo architecture, `docs/roadmap.md` for the next major development phase, and `docs/ui-feature-migration.md` for the imported UI feature map.

## Open Pilot

This repository keeps the Open Pilot issue template and PR template.
Bootstrap labels after creating or moving the repository:

```bash
open-pilot labels bootstrap PatrickFanella/switchyard
```

## License

GPL-3.0-only. See `LICENSE`.
