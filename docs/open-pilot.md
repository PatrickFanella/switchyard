# Open Pilot Readiness

Open Pilot can safely work in this repository when issues are specific, scoped, and verifiable.

## Queue an issue by assignment

Set `OPEN_PILOT_ASSIGNEE=open-pilot` in the runtime configuration to enable
assignment-based pickup. Then assign a scoped issue to the Gitea `open-pilot`
account in a repository allowed by `OPEN_PILOT_POLL_REPOS`. The enabled
`gitea-open-pilot-poll` trigger picks it up on its next scheduled poll; assignment
is not an immediate webhook dispatch. An empty `OPEN_PILOT_ASSIGNEE` (the default)
keeps label-only pickup.

When a label-queued issue starts, Open Pilot assigns `OPEN_PILOT_ASSIGNEE` to it
after validation and before agent execution, preserving existing assignees. This
requires `OPEN_PILOT_GITEA_WRITE_MODE=allow`; disabled/dry-run modes do not change
assignees. Assignment failure fails the validation step and is visible in the
run rather than silently proceeding. Gitea replaces the assignee list on edit,
so avoid simultaneous human assignee edits during pickup.

Assignment is an alternative to the queue labels below. Both paths re-fetch the
issue, require it to be open, and share the same atomic claim so one issue is not
queued twice. Execution rechecks eligibility; removing the bot assignment before
execution prevents an assignment-only issue from starting. An issue with all queue
labels remains eligible through the label path. To stop an already running task,
use Switchyard cancellation rather than relying on unassignment.

Issue specification, repository allowlists, command allowlists, verification,
review, CI, and merge policy still apply. Assignment does not enable Git writes
or autonomous merges: the deployed lifecycle modes and repository policy must
already permit them. Enable `OPEN_PILOT_REQUIRE_ISSUE_SPEC=true` to require
acceptance criteria and test instructions.

## Queue an issue by labels

The existing label-based path requires both labels:

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
