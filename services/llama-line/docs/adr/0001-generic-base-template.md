# ADR 0001: Keep the Base Template Stack-Neutral

## Status

Accepted

## Context

The template needs to support Go, Node/TypeScript, Python, Rust, React, Vite, Tailwind, Postgres, Docker, and Makefile-driven projects. A single repository cannot scaffold every stack cleanly without becoming noisy and opinionated.

## Decision

Keep this repository as a minimal base template with shared project conventions, docs structure, Open Pilot readiness, verification entry points, and optional infrastructure profiles. Build stack-specific templates from this base.

## Consequences

- Derived templates start from consistent process and documentation.
- Stack-specific choices stay isolated in child templates.
- The base remains easy to audit and update.
