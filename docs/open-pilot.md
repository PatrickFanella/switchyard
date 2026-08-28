# Open Pilot Readiness

Open Pilot can safely work in this repository when issues are specific, scoped, and verifiable.

## Required labels

Open Pilot only runs after both labels are present:

- `ready-for-agent`
- `agent:queued`

The issue template adds `ready-for-agent`. A human should add `agent:queued` only after reviewing the issue body.

## Bootstrap labels

Run this after creating or moving the repository:

```bash
open-pilot labels bootstrap OWNER/REPO
```

## Issue requirements

Every agent-ready issue should include:

- Goal
- Context
- Acceptance Criteria
- Test Command
- Out of Scope
- Queue Checklist

## Test command guidance

Prefer the narrowest deterministic command that proves the requested change.

Examples:

```bash
make verify
test -f docs/reference/example.md
go test ./...
pnpm --dir web run test
uv run pytest
cargo test --all
docker compose config --quiet
```

## PR review and merge

Open Pilot PR review expects the PR verification command to pass. If the deployed Open Pilot instance publishes commit statuses, merge gates should require the Open Pilot-owned status context before merging.

Gitea 1.27 atomically checks the tested head with `head_commit_id`, but the merge API has no base-commit compare-and-swap. Switchyard immediately preflights the full persisted base and head identity before every autonomous merge attempt. A Gitea request or preflight error leaves the durable state at `merge_ready` for a bounded run retry; identity mismatches still fail closed. Configure Gitea permissions and repository policy to prevent concurrent base retargeting between Switchyard's preflight and merge request. Do not disable or relax the identity checks.
