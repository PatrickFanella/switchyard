# Executor sessions

`GET /api/executor-sessions` is the bounded, read-only view of OpenCode
executor sessions. It replaces the Sessions dashboard view that rendered the
`GET /api/adapters` compatibility snapshot (`adapters.sessions` and
`adapters.ocq`). The full schema is in [`docs/openapi.yaml`](../openapi.yaml).

## Listener and authorization

- Served only on the authenticated control listener. The model-ingress
  listener returns 404 for it.
- `GET` is read-only: admin tokens, read tokens, and trusted reverse-proxy
  identities may call it. `POST` and `DELETE` return 405.

## Sources

The view uses two native contracts. It does not read the adapter snapshot.

1. **Run-step correlation records.** Retained run steps whose
   `correlation_ids` contain `opencode_session_id` or `ocq_session_id`,
   newest first, capped at 500 records. Each record contributes its run, step,
   step status, timestamps, and request IDs (`opencode_request_id`,
   `ocq_request_id`, `model_broker_request_id`, `llama_line_request_id`,
   `request_id`). From the owning run only three inputs are read: the Open
   Pilot repository ID, executor generation, and repository slug.
2. **Executor status.** The default executor status (the same provider as
   `GET /api/opencode-executor/status`) and the per-repository executor status
   of each loaded Open Pilot execution bundle. For active native sessions on an
   available executor, the view also checks presence with
   `GET /session/{id}/message?limit=1` on the owning executor. At most 16
   sessions are checked per request. Only the latest message's role, finish
   marker, error name, and timestamps are decoded; message content is not.

Records are grouped by owning executor and session ID. A run with repository
affinity maps to the executor key `<repository_id>/<executor_generation>` when
repository bundles are configured; otherwise, including legacy-singleton mode,
it maps to `default`.

## Session fields

| Field | Meaning |
| --- | --- |
| `origin` | `native` for `opencode_session_id` recorded by the native executor; `compatibility` for `ocq_session_id` or OCQ-routed records. |
| `repository` | Repository ID, executor generation, and slug recorded on the owning run. |
| `runs`, `requests` | Related run steps and request IDs, 20 of each at most, with `*_truncated` flags. |
| `active` | At least one related step is `running` or `queued`. |
| `presence` | `present`, `missing` (executor returned 404), `unknown` (executor unavailable, not loaded, or failed), or `not_checked` (inactive, compatibility, or over the check budget). |
| `last_observed_at` | Latest step timestamp, or the executor's latest message time when that is newer (`last_observed_source`). |
| `status` | See below. |

| `status` | Condition |
| --- | --- |
| `active` | A related step is active and none of the stale conditions apply. |
| `stale` | A related step is active, and the executor no longer has the session, its run already finished, the run's worker lease expired, or the session has no confirmed presence and no observation within `OPENCODE_MESSAGE_TIMEOUT`. |
| `ended` | Native session with no active related step. |
| `historical_compatibility` | Compatibility record with no active related step. |

## Unavailable executors and empty lists

Each executor appears in `executors` with a coarse `status` (`healthy`,
`unreachable`, `failed`, `not_configured`, `unknown`) and no URL or upstream
error text. `executor_availability` is `available`, `partial`, `unavailable`,
or `not_configured`. Sessions on an unavailable executor are not probed; they
report `presence: unknown` and the executor status. A session whose recorded
repository generation has no loaded executor reports `executor_status:
not_loaded`.

An empty session list is not evidence of health. When the list is empty and
executors are not all available, `warnings` says so, and the dashboard states
that the empty list does not mean the executor is healthy or idle.

## Aborting a session

There is no session mutation route. `abort` in the response states the only
path: cancel the owning run with `POST /api/runs/{id}/cancel`, which requires
admin authorization. Run cancellation aborts the run's active executor session
through the run's recorded executor route. Related runs carry `cancelable`
(the run is not terminal); the dashboard links to the run cockpit and offers
no abort control of its own.

## Bounds and filters

| Parameter | Effect |
| --- | --- |
| `limit` | Sessions returned, 1 to 100, default 50. |
| `session_id` | One session, matched against `opencode_session_id` or `ocq_session_id`. Letters, digits, `.`, `_`, `:`, `-`, at most 256 characters. |
| `repository` | Repository ID, slug, executor key, or `default`. At most 128 characters. |

Only records created within the retention window (`SWITCHYARD_RETENTION`,
default `720h`) are read; `retention` states the floor. `truncated` is true
when the step scan or the session limit cut results. No migration is needed:
the scan uses the `run_steps(created_at desc, id desc)` index from migration
`028_evidence_search_indexes.sql`.

## Secret handling

Responses never contain run or step inputs, prompts, message content,
executor endpoint URLs, upstream error text, or credentials. Identifiers and
step names pass `RedactForLog` and are capped at 512 characters; repository
slugs that do not match `owner/name` are dropped.

## Dashboard

The Sessions view (`#/sessions`) renders `SessionsPage`. Session deep links use
`#/sessions/<encoded session id>` and filter server-side. Correlation links for
`opencode_session_id` and `ocq_session_id` in the run cockpit now resolve to
this deep link instead of an evidence search. Session cards link to the run
cockpit, the step evidence anchor (`#/runs/<run>/steps/<step>`), and model
requests (`#/queues/requests/<id>`). The page refreshes every 15 seconds
through `useLiveRefresh`.

## Tests

- `backend/internal/controlplane/executor_sessions_test.go`: multiple
  repositories, partial upstream failure, stale sessions, empty lists with
  unavailable executors, read-only access and admin-only cancellation,
  secret absence, model-listener non-exposure, filters, retention, the check
  budget, and metadata-only executor probes.
- `backend/internal/controlplane/executor_sessions_postgres_test.go`: the
  store scan against PostgreSQL (`make postgres-test`).
- `web/src/components/SessionsPage.test.tsx`: loading, session states,
  evidence links, no abort control, empty lists with and without available
  executors, error and retry, deep links, and polling.
