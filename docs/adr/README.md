# Architecture Decision Records

Use ADRs for decisions that affect structure, dependencies, operations, security, or long-term maintenance.

ADR 0002 and ADR 0003 document the historical import phase and native cutover. ADR 0004 records the measured threshold contract for inline artifact storage. ADRs 0005 to 0007 record deferred capability decisions: each keeps the current boundary, names an owner and a revisit trigger, and does not authorize building the deferred feature.

## Records

- [ADR 0002: Import OCQ, llama-line, and Changemaker into Switchyard (historical import phase)](0002-import-services-monorepo.md)
- [ADR 0003: Rebuild Imported Apps as Native Switchyard Modules](0003-native-switchyard-modules.md)
- [ADR 0004: Keep artifacts inline until measured limits are exceeded](0004-inline-artifact-storage-threshold.md)
- [ADR 0005: Keep plugin and skill installation outside Switchyard](0005-plugin-and-skill-installation-boundary.md)
- [ADR 0006: Retain linear workflows; define the limit for bounded branching](0006-linear-workflows-before-branching.md)
- [ADR 0007: Defer dynamic multi-agent orchestration beyond static roles](0007-bounded-multi-agent-orchestration.md)

Suggested format:

```markdown
# ADR NNNN: Title

## Status

Proposed | Accepted | Superseded

## Context

What forces or constraints led to this decision?

## Decision

What did we choose?

## Consequences

What trade-offs follow from this choice?
```
