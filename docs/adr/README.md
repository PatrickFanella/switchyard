# Architecture Decision Records

Use ADRs for decisions that affect structure, dependencies, operations, security, or long-term maintenance.

ADR 0002 and ADR 0003 document the historical import phase and the native cutover that removed the imported OCQ, llama-line, and Changemaker directories.

## Records

- [ADR 0002: Import OCQ, llama-line, and Changemaker into Switchyard (historical import phase)](0002-import-services-monorepo.md)
- [ADR 0003: Rebuild Imported Apps as Native Switchyard Modules](0003-native-switchyard-modules.md)

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
