# Runtime configuration (v1 boundary)

This page defines which Switchyard settings an operator can change, where,
and with what authority. The machine-readable source is
`backend/internal/runtimeconfig/inventory-v1.json`; tests in
`backend/internal/runtimeconfig/inventory_test.go` fail if a key in the
defaults contract (`defaults-v1.json`) is missing from it, if a
credential-like key is not classified secret, or if the runtime-mutable
subset grows without a test change.

## Classification

Every inventoried setting carries five attributes.

| Attribute | Values | Meaning |
| --- | --- | --- |
| Source of truth | `file-managed`, `runtime-mutable` | `file-managed` settings come from the deployed environment file. `runtime-mutable` settings live in API process memory. |
| Sensitivity | `public`, `internal`, `secret` | `secret` values never leave the process. `internal` values (addresses, paths, URLs) are shown only to authenticated control-listener callers. |
| Restart | `api`, `opencode`, `compose`, `none` | What must restart before a file edit takes effect. |
| Authorization | `host-operator`, `admin` | `host-operator` means write access to the environment file and the systemd unit on the host; no API role can change these. `admin` means the control-plane admin token or an authenticated proxy identity. |
| Observable | default true | Compose-only keys (published ports and bind hosts) and OpenCode-sidecar keys are inventoried but the API process cannot read them. |

The inventory covers settings the API process reads, plus the Compose and
sidecar keys already in the defaults contract. It does not list every
variable consumed by scripts, the verifier, backup or restore tooling, or
the web container. Those keep their own documentation in `.env.example`
and the runbooks.

## Supported v1 boundary

**File-managed (all settings except two).** Listeners, authentication,
request limits, persistence, scheduler and worker sizing, retention, model
broker, OpenCode executor, Open Pilot gates and command policy, and Gitea
credentials are file-managed. To change one:

1. Edit the key in the deployed environment file: the absolute
   `SWITCHYARD_ENV_FILE` given to `deploy/install-systemd.sh`, or `.env` for
   local Compose.
2. Restart the component named by the setting's restart attribute. On a
   systemd host, `sudo systemctl reload switchyard` re-runs
   `validate_production_config.py`, the nft and UFW gate checks, and
   `docker compose up -d`. Locally, run `docker compose up -d api` (or
   `opencode`, or the whole stack for Compose-level keys).
3. Reload the Config page and confirm the new effective value and source.

Every setting row on the Config page shows these instructions with the
exact key name. The environment file's own history (for example, the
host's configuration backup) is the audit record for file-managed changes;
Switchyard does not record them.

Listener addresses, command allowlists, OpenCode security mode, Open Pilot
activation and merge gates, and credentials are file-managed on purpose:
changing them at runtime would bypass the production configuration
validator and the deployment gates. v1 exposes no API to change them.

**Runtime-mutable (two settings).**

| Key | Effect | Resets to |
| --- | --- | --- |
| `scheduler.dispatch_paused` | The scheduler keeps ticking and records `last_tick`, but dispatches no cron or poll triggers. | `false` on API restart |
| `workers.claim_paused` | Workers stop claiming queued runs. Runs already executing continue; expired leases are still recovered. | `false` on API restart |

These pauses stop new automated work without editing configuration.
Neither can widen what Switchyard is allowed to do. Neither is persisted:
a restart returns to the file-managed `SWITCHYARD_SCHEDULER_ENABLED` and
`SWITCHYARD_WORKER_ENABLED` behavior. If the scheduler or worker pool is
disabled in the environment file, the matching control reports
`unavailable` and rejects writes.

## API

Both routes are served only by the control listener. The model-compatible
listener returns 404 for them (checked by
`TestRuntimeConfigListenerAndAuthorizationBoundary`). The webhook listener
serves only its signed dispatch route and does not mount the control API.

`GET /api/runtime-config` (read or admin credential) returns the contract
version, the current revision, every inventoried setting, and the last 50
runtime changes. For each setting it reports:

- `source`: `environment` (set in the process environment), `default`
  (unset; the defaults contract value applies), `unset` (no value and no
  contract default), `runtime`, `unavailable`, or `not-observable`.
- `effective`, plus `default`, `compose_default` and `example_default` from
  the defaults contract. These values are omitted for secret settings.
- `configured` for secret settings: whether a non-empty value is present.
- `overridden`: `true` when the effective value differs from the runtime
  default. `deployment_override` carries the documented reason when Compose
  deliberately differs from the bare-process default (for example, the
  model listener). The Config page counts both as drift.
- `instructions`: the exact edit-and-restart steps, or the API call for a
  runtime-mutable setting.

Secret redaction uses the inventory classification. A key name matching
the credential pattern (`TOKEN`, `SECRET`, `PASSWORD`, `CLIENT_KEYS`,
`GATEWAY_KEY`, `API_KEY`, `DATABASE_URL`) is also redacted, unless the key
only locates a file or directory (`_DIR`, `_FILE`, `_PATH`).
`TestRuntimeConfigRedactsSecretsByClassification` sets real-looking values
and asserts that none appear in the response.

`PUT /api/runtime-config/{key}` (admin only) takes
`{"value": <bool>, "expected_revision": <int>, "reason": "<optional>"}`.

| Response | Cause |
| --- | --- |
| 200 | Applied. Returns the updated setting, the new revision, and the change record. |
| 400 | Invalid JSON, unknown field, missing `expected_revision`, or a non-boolean value. |
| 401 / 403 | Not authenticated, or authenticated with the read token. |
| 404 | Key is not in the inventory. |
| 409 `file-managed` | Key is file-managed; the message contains its instructions. |
| 409 `unavailable` | The scheduler or worker pool is not running in this process. |
| 409 `stale-revision` | Another change was applied since the caller read the revision; the body carries the current revision. |

Each accepted change is logged (`runtime setting changed`, with key,
previous and new value, actor, reason, and revision) and added to the
in-memory change list. The list is bounded and process-local: it records
what happened during this process's lifetime and is not a durable audit
trail. `TestRuntimeConfigWriteValidationAndConflicts` covers each rejection
case and checks that rejected writes do not change state.

## Out of scope for v1

- Persisting runtime changes or making any file-managed setting
  runtime-mutable.
- Writing to the environment file or restarting services from the API.
- Changing listener, executor, command-policy, or deployment boundaries.
- Inventorying script-only and tooling-only variables.
