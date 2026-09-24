# Evidence search

`GET /api/evidence/search` is the native, bounded search over retained
Switchyard evidence. It replaces the Logs / Search dashboard view that
filtered a partial adapter snapshot in the browser. The full schema is in
[`docs/openapi.yaml`](../openapi.yaml).

## Listener and authorization

- Served only on the authenticated control listener. The model-ingress
  listener returns 404 for it.
- `GET` is a read-only request: admin tokens, read tokens, and trusted
  reverse-proxy identities may call it.

## Searched records and fields

| Kind | Searched fields | Returned as |
| --- | --- | --- |
| `run` | ID, workflow ID, trigger ID, status, summary | title = workflow ID, summary = run summary |
| `step` | ID, run ID, name, executor, model, status, output summary, error, correlation-ID values | title = step name, summary = output summary, error |
| `request` | ID, model, path, client, status, error, correlation-ID values | title = model, summary = path and client, error |

Never searched and never returned: run inputs, step inputs, model request
bodies, model response bodies. Substring matching over those fields would
let a read-token holder probe secrets one character at a time, so they are
excluded from the haystack as well as the projection. Every returned text
field passes `RedactForLog` and is capped at 512 characters; correlation keys
that look like credentials (`token`, `secret`, `authorization`, ...) are
replaced with `[REDACTED]`.

## Scopes

| `scope` | Kinds | Match haystack |
| --- | --- | --- |
| `all` (default) | run, step, request | fields above |
| `runs` | run | fields above |
| `steps` | step | fields above |
| `requests` | request | fields above |
| `errors` | run, step, request | fields above, restricted to failed/timed-out runs, steps with an error or failed/timed-out status, and failed or errored requests |
| `correlations` | step, request | only identifiers (step ID, step run ID, request ID) and correlation-ID values |

## Filters

| Parameter | Effect |
| --- | --- |
| `q` | Case-insensitive substring, at most 256 characters. Empty lists the most recent evidence in scope. |
| `status` | Exact, case-insensitive status match. |
| `workflow` | Restricts runs and steps to one workflow ID. Requests carry no workflow and are excluded. |
| `run_id` | Restricts to the run, its steps, and requests whose correlation-ID values equal the run ID. |
| `limit` | 1 to 100, default 25. |
| `cursor` | Opaque `next_cursor` from the previous page. |

Filter values are limited to 128 characters. Invalid scope, limit, cursor, or
over-length values return `400`.

## Retention limit

Only records created at or after `now - SWITCHYARD_RETENTION` (default
`720h`) are searchable. The response `retention` object states the exact
`window`, `floor`, and `searched_at`. A record older than the floor is not
searchable even if the retention loop has not pruned it yet, and records
retention has already deleted cannot be recovered through search. When no
retention manager is attached (tests, ad-hoc embedding) the floor uses the
same 30-day default as `NewRetentionManager`.

## Pagination

Hits are ordered by `(created_at desc, kind desc, id desc)`. The cursor
encodes the last hit's position, and each page is a strict keyset query
below that position, so pages remain stable while new evidence is written.
The memory store and the PostgreSQL store share the same matcher semantics
and ordering; migration `028_evidence_search_indexes.sql` adds
`(created_at desc, id desc)` indexes on `runs`, `run_steps`, and
`model_broker_requests` for the retention-bounded scans.

## Dashboard

The Logs / Search view (`#/logs`) hosts the search page. Deep links use path
segments so the existing hash router keeps resolving the view:
`#/logs/search/<scope>/<encoded query>[/<cursor>]`. The page has explicit
idle, loading, no-results, error (with retry), and paginated result states,
and hits link to the run cockpit or the request detail.

## Tests

- `backend/internal/controlplane/evidence_search_test.go`: scopes, filters,
  payload exclusion, redaction, retention floor, stable pagination across
  timestamp ties, authorization, and listener boundary.
- `backend/internal/controlplane/evidence_search_postgres_test.go`: the
  same contract against PostgreSQL (`make postgres-test`), including LIKE
  wildcard escaping.
- `web/src/components/EvidenceSearchPage.test.tsx`: idle, loading, results,
  no-results, error/retry, deep link, and cursor pagination states.
