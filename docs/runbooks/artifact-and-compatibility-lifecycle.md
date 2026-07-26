# Artifact and Compatibility Lifecycle

## Artifacts

Switchyard currently stores workflow artifacts as validated `data:` URIs in
PostgreSQL. Every newly written artifact records its storage class, MIME type,
decoded byte size, SHA-256 digest, and `retention_until` timestamp. Remote or
filesystem URIs are rejected; introduce object storage only with an explicit
authenticated lifecycle and deletion contract.

`SWITCHYARD_MAX_ARTIFACT_BYTES` bounds decoded payload size (default 1 MiB);
the encoded URI has a separate defensive expansion cap. Artifact retention
inherits `SWITCHYARD_RETENTION` (default 30 days), and run retention deletes
associated inline artifacts through the database foreign key. Inspect the
`retention_until` field in run details when investigating an artifact
approaching deletion.

## Deprecated compatibility routes

`GET /api/compatibility/retirement` reports aggregate UTC daily usage of the
deprecated `llama-line` and `ocq` routes. It contains no request payloads and
survives run/model-ledger retention.

Do not remove a compatibility route merely because a recent history page is
empty. Require the retirement endpoint to show zero attempts in the preceding
90 days, then record the observed release SHA and date in a release
qualification record before removing the selector.
