# Artifact and Compatibility Lifecycle

## Artifacts

Switchyard currently stores workflow artifacts as validated `data:` URIs in
PostgreSQL. Every newly written artifact records its storage class, MIME type,
decoded byte size, SHA-256 digest, and `retention_until` timestamp. Remote or
filesystem URIs are rejected; introduce object storage only with an explicit
authenticated lifecycle and deletion contract.

`SWITCHYARD_MAX_ARTIFACT_BYTES` bounds decoded payload size (default 1 MiB);
the encoded URI has a separate defensive expansion cap. Artifact retention
metadata inherits `SWITCHYARD_RETENTION` (default 30 days).
`retention_until` is a declaration, not an independent deletion trigger. The
retention worker deletes associated artifacts only when it prunes a terminal
run whose `created_at` is older than the configured run-retention cutoff.

Query `GET /api/artifacts/report?days=30` with a control-plane read or admin
token. `days` accepts 1 through 90 and returns one UTC bucket per day, including
zero-value days. The report contains:

- exact currently retained count and measured decoded bytes;
- durable created and pruned count and bytes since `observation_started_at`;
- retained artifacts whose declared `retention_until` has elapsed, plus legacy
  artifacts missing that metadata;
- created, pruned, and net-change daily buckets for the requested window.

Artifacts written before lifecycle metadata was introduced report through the
`*_bytes_unmeasured` counts and are excluded from byte sums. This avoids
claiming that a legacy `byte_size=0` default is a measured empty payload.

Prometheus exports the same retained gauges and durable created/pruned
counters without labels. The totals do not claim history before
`observation_started_at`.

Keep PostgreSQL as the artifact store unless an operator supplies an artifact
byte budget, an observation window, and a sustained-over-budget threshold, and
the report shows that measured workload exceeded that threshold. Do not infer
missing values or introduce object storage from a short-term spike. See
[ADR 0004](../adr/0004-inline-artifact-storage-threshold.md).

## Deprecated compatibility routes

`GET /api/compatibility/retirement` reports aggregate UTC daily usage of the
deprecated `llama-line` and `ocq` routes. It contains no request payloads and
survives run/model-ledger retention. This endpoint remains informational and
does not establish removal eligibility.

Set
`SWITCHYARD_SOURCE_REVISION` to the deployed 40-character Git SHA and
`SWITCHYARD_DEPLOYMENT_ID` to an explicit stable identity containing only ASCII
letters, digits, `.`, `_`, or `-`. If the revision is empty or `unknown`,
observation is disabled. PostgreSQL grants one replica a session-scoped fenced
observer lease; other enabled replicas fail startup rather than double-write.
Lease takeover checks the prior segment's final heartbeat to detect a restart
gap, then starts a new segment at the current instant. It never merges coverage
across segments or fills an earlier gap. A revision or identity change also
starts independent coverage. `GET /api/compatibility/observer` exposes retry
health and detected gaps.

Generate evidence with authenticated
`GET /api/compatibility/report?start=YYYY-MM-DD&end=YYYY-MM-DD`. The inclusive
window is limited to 90 elapsed UTC days. The request succeeds only after the
end day has elapsed and only when every day/backend pair has complete coverage.
The canonical report includes exact attempt counts, per-day revision,
deployment identity, one fenced segment, report source identity, durability
eligibility, and a deterministic SHA-256. Per-day revisions identify the code
that observed that day; the top-level revision identifies the release that
generated the report. Every observation deployment ID equals the report source
identity. Memory-store reports are diagnostic and never qualification eligible.
Missing days or backends return `409`; absence is never zero use. This route is
control-listener-only and is not exposed by model ingress.

Do not remove either selector in the release qualified by this report. A
follow-on removal proposal may include the optional `retirement_proposal`
release-evidence field only after all 90 days report zero attempts for both
backends and exactly two distinct, chronologically ordered, immutable prior
qualifying releases are supplied. The candidate release cannot be one of those
two. Each prior release must provide hashed deployed evidence, manifest, and
report artifact bytes. The validator binds the candidate report artifact
digest, source revision, deployment identity, and segment to candidate
evidence. Without that optional proposal, normal release evidence remains
unchanged.
