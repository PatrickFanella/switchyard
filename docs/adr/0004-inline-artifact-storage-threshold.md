# ADR 0004: Keep artifacts inline until measured limits are exceeded

## Status

Accepted

## Context

Switchyard stores bounded workflow artifacts as inline `data:` URIs in
PostgreSQL. Adding object storage would add credentials, network dependencies,
and a second deletion lifecycle. Current workloads have not supplied enough
evidence to justify that cost.

Artifact `retention_until` is declaration metadata calculated when an artifact
is created. The retention worker does not delete an artifact at that timestamp.
It deletes artifacts with their parent run only when the terminal run's
`created_at` is older than the configured run-retention cutoff.

## Decision

Keep inline artifacts in PostgreSQL. Switchyard measures exact retained count
and measured decoded bytes, durable created and pruned count and bytes, and bounded UTC
daily buckets. The observation start timestamp states how much history the
totals cover. Legacy records without decoded size metadata are counted as
unmeasured rather than treated as zero bytes. No report extrapolates unobserved
workload.

Consider a storage change only when an operator supplies all three inputs:

- an artifact byte budget;
- an observation window;
- a sustained-over-budget threshold for that window.

The measured workload must exceed the supplied sustained threshold before a
storage change is proposed. The operator owns these values; Switchyard does not
ship guessed defaults. A later proposal must define authenticated access,
integrity, migration, deletion, backup, and restore contracts before adding
object storage.

## Consequences

PostgreSQL remains the only artifact store. Operators can set limits from their
own capacity and measured workload. This decision does not claim a byte budget,
observation duration, sustained threshold, or current growth rate.
