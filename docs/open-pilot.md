# Open Pilot Readiness

Open Pilot can safely work in this repository when issues are specific, scoped, and verifiable.

## Phase 1 repository affinity

Switchyard records additive repository and executor-generation affinity without changing the legacy `(repo, issue)` key. New admitted targets use registry spelling and positive decimal issue numbers. Existing rows retain raw identity and shadow canonical candidates. Canonical slugs and every registered alias reserve one target across old and new writers; terminal rows also keep their reservation. Workers atomically adopt safe queued or running pre-affinity runs and pause both the run and state for manual review when identity, affinity, or collision checks fail.

The runtime remains bound to one executor. Set `OPEN_PILOT_SINGLETON_REPOSITORY` to the exact source-origin slug and keep `OPEN_PILOT_EXECUTOR_GENERATION` stable. An optional `SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST` uses the strict v1 schema in `docs/schemas/`, but Phase 1 rejects every non-bound repository and production validation requires no manifest. This release does not render Compose services or admit a second repository.

Use the authenticated control route `GET /api/open-pilot/repository-audit` before changing registry data. It lists bounded collision, parse, affinity, and active/resumable evidence without paths or secrets. The route is absent from model ingress.

## Phase 2 topology preparation

`scripts/prepare_open_pilot_registry.py` validates every operator-registry entry
before creating any missing managed worktree root or marker. It checks exact
Gitea origin identity, runtime ownership and modes, non-overlapping repository
paths, private password files, and existing Git/config/data directories. It
never clones, changes ownership, or derives a path from a run input.

`scripts/render_open_pilot_topology.py` deterministically writes a runtime
manifest, Compose override, and exact Compose file-set record with one shared
registry fingerprint. Each rendered repository receives a separate OpenCode
service, verifier service, internal networks, state/cache volumes, source Git
mount, worktree mount, and password-file mount. The host wrapper
`deploy/switchyard-compose` refuses mismatched fingerprints or file paths before
calling the canonical Docker boundary. These Phase 2 artifacts prepare topology
only; multi-repository runtime admission remains disabled until repo-keyed
routing and isolated proof are complete.

## Phase 3 repository execution routing

With a generated runtime manifest, the API constructs one immutable execution
bundle for each `(repository_id, executor_generation)` pair. A bundle contains
only operator-rendered source/worktree paths, the repository's OpenCode endpoint
and private password-file value, and its verifier endpoint. Every execution and
cancellation re-resolves the canonical repository and requires the protected run
affinity fields to match before choosing a bundle; filesystem paths and raw run
slugs never select an executor.

Set `SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=disabled` to load and health-check the
topology without admitting work. `isolated-test` permits registered repositories
for bounded non-production proof, while production validation rejects that mode.
`legacy-singleton` retains the compatibility deployment when no generated
manifest is active. `GET /api/opencode-executor/status` projects each configured
repository's OpenCode and verifier health plus its verifier concurrency limit,
without exposing passwords or host paths.

## Phase 4 crash and write lifecycle

Recovery reloads durable affinity and accepts a persisted worktree only when
its canonical source equals the selected bundle source, its canonical worktree
is strictly beneath that bundle's managed root, and its Git identity still
matches the recorded repository, branch, and common directory. A sibling
repository path is never adopted.

Open Pilot requests use fail-closed stale-session handling. A missing durable
correlated session pauses for operator review, and a stale response after a
message POST never causes an automatic repost. The executor may replace only a
session created during the current call that disappeared before the first
message attempt, where no prior worktree mutation could have occurred.

`make open-pilot-topology-smoke` uses a unique Compose project to build two
production-shaped OpenCode/verifier pairs and real managed Git worktrees. It
proves each OpenCode runtime can write only its repository worktree, each
verifier mount rejects writes, one repository continues while its sibling is
stopped, both sidecars recover healthy after restart, written state persists,
and the isolated project is removed afterward.

## Phase 5 durable rollout gate

Production uses `SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=durable`. The
authenticated `GET /api/open-pilot/rollout` route reports the PostgreSQL gate,
proof targets, expiry, generation, close reason, registry-bound lifecycle
counts, and singleton rollback safety. `POST /api/open-pilot/rollout` accepts an
exact registry fingerprint and atomically closes the gate, starts a proof for
exactly two distinct repository/issue/executor-generation targets for at most
24 hours, or promotes that same fingerprint to `allow`.

The database claim query fences queued Open Pilot work while disabled and, in
proof mode, claims only the two exact targets. A proof failure or pause closes
the gate in the same database transaction as the issue-state mutation; an
expired proof is also treated as closed by admission and claims. Promotion to
`allow` requires both durable proof states to be `issue_closed`. Before a
singleton binary rollback, close the gate and require the reported active,
resumable, and cancellable registry-bound counts all to be zero.

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
