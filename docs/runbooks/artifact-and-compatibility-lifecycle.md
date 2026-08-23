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
survives run/model-ledger retention.

Do not remove a compatibility route merely because a recent history page is
empty. Require the retirement endpoint to show zero attempts in the preceding
90 days, then record the observed release SHA and date in a release
qualification record before removing the selector.
