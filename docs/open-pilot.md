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

Strict sidecars keep sessions in memory, so a container recreation can leave a
paused run with a missing session. After inspecting that run, an operator may
POST the lifecycle resume endpoint with `fresh_session_for_run=<terminal-run-id>`
in addition to `repo` and `issue`. This explicit recovery requires a paused or failed
issue whose safe resume cursor is agent work, a recorded session, and a clean
managed worktree at its recorded full commit SHA. Before the first agent commit,
the recorded executed branch-base SHA provides the same exact-HEAD fence; dirty
or moved worktrees remain rejected. The store atomically fences the expected terminal prior
run (paused, failed, or canceled), retains its history, and records the replaced session in the new run.
The executor repeats the worktree check before creating the new session.
Dirty or moved worktrees require inspection; ordinary resume never resets a
session automatically.

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

## Readiness preflight and onboarding

`GET /api/open-pilot/readiness` on the authenticated control listener explains
why a repository or issue cannot run before an operator dispatches it. The
read token is sufficient. The route is absent from model ingress. Query
parameters are optional: `repo` (canonical slug or alias), `issue` (requires
`repo`), and `test_command` (evaluated against the argv allowlist without
executing it). The response is always `200` with `ready=false` and structured
codes for configuration or prerequisite failures; only a malformed query is
rejected with `400`.

The report composes, in order: the admission gate, activation mode, repository
registration and executor generation, the durable rollout gate and its
fingerprint, the immutable execution bundle, OpenCode and verifier health
(probed with a 3 second bound through the existing repository status
provider), the durable issue state and affinity, the command allowlist
decision, the prepared verifier's supported commands, and the effective write,
push, commit, CI, merge, and close gates.
Each entry in `checks[]` carries a stable `code`, a `status` of `pass`, `warn`,
`fail`, or `skip`, a message, and remediation text. `blockers[]` lists every
failing code. `onboarding[]` is the checklist an operator works through to
bring a repository to production readiness, with `done` computed from the same
evidence. The response never contains passwords, password file paths, host
paths, or the raw registry document; a test asserts this against fixture
secrets.

Lifecycle gate warnings (`gitea_write_disabled`, `git_push_disabled`,
`auto_commit_disabled`, `ci_checks_disabled`, `merge_disabled`,
`close_issue_disabled`) report effective values and do not block readiness:
a disabled gate is a deliberate policy and the run stops at that stage for a
human. Readiness reads policy; it never relaxes admission, affinity, executor
isolation, the command allowlist, listener boundaries, or CI/merge gates.

Failing codes and remediation:

| Code | Meaning | Remediation |
|---|---|---|
| `gate_unavailable` | The admission gate could not be constructed from the runtime configuration. | Fix the configuration named in the message (activation mode, manifest, source origin, singleton binding) and restart the API. |
| `execution_bundles_unavailable` | A registry entry's password file, endpoint, or identity prevented bundle construction. | Repair that entry in the operator registry, re-render the topology, restart the API. |
| `activation_disabled` | `SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=disabled` admits no work. | Set `durable` after topology health checks pass, then open the rollout gate. |
| `repository_unregistered` | The repository matches no canonical slug or alias. | Add its exact Gitea origin slug to the operator registry, run `scripts/prepare_open_pilot_registry.py`, re-render, restart. |
| `issue_invalid` | The issue is not a positive decimal number. | Provide the number without a leading `#`. |
| `repository_not_bound` | Legacy singleton runtime is bound to a different repository. | Dispatch against the bound repository or activate durable mode. |
| `rollout_store_unavailable` | The durable rollout status or issue state could not be read. | Check PostgreSQL connectivity and the rollout migration. |
| `rollout_not_configured` | The durable gate has never been configured. | `POST /api/open-pilot/rollout` with the current `registry_fingerprint`. |
| `rollout_fingerprint_stale` | The gate was configured for a different registry fingerprint than the loaded manifest. | Reconfigure the gate with the fingerprint this endpoint reports. |
| `rollout_closed` | The gate is closed; the message includes the close reason. | Inspect the reason, then start a bounded proof or promote to allow. |
| `rollout_proof_expired` | The bounded proof expired. | Start a new proof (24 hours maximum) or promote after both proof targets closed. |
| `rollout_proof_target_excluded` | Proof mode does not list this repository/issue/generation. | Dispatch a listed proof target or promote the gate. |
| `execution_bundle_missing` | No bundle is loaded for the repository's executor generation. | Re-render the topology for the current generation and restart the API. |
| `executor_status_unavailable` | Health could not be projected for the target. | Resolve the gate and bundle errors above; re-render and restart. |
| `opencode_unreachable` | The repository OpenCode sidecar failed its session probe. | Start or repair the repository's OpenCode service and private password file. |
| `verifier_unreachable` | The repository verifier failed `/healthz`. | Start or repair the verifier service and prepared image (`scripts/prepare_verifier.py`). |
| `command_denied` | The verify step would reject `test_command`; the message carries the policy reason. | Use a plain argv command in `OPEN_PILOT_ALLOWED_COMMANDS` with `OPEN_PILOT_COMMAND_POLICY_MODE=allow`. |
| `verifier_command_unsupported` | The repository's prepared verifier does not list `test_command` as an exact argv command; it would reject the command before running it. | Use a command the verifier reports in `verifier_preparation.commands`, or extend `backend/cmd/verifier/profiles.json` and rebuild the image with `scripts/prepare_verifier.py`. |
| `issue_affinity_stale` | The durable state is bound to a different repository or executor generation than the registry now resolves. | Finish or pause the issue on its original generation; review through `GET /api/open-pilot/repository-audit`. |
| `issue_run_active` | A queued or running run already owns the issue. | Wait for it to finish or cancel it. |

Warning and skip codes include `activation_isolated_test`,
`activation_legacy_singleton`, `rollout_proof_bounded` (proof mode without a
target), `command_dry_run`, `command_not_provided`, `command_policy_unknown`,
`verifier_unprepared` (the verifier has no usable prepared bundle, so only
dependency-free commands can pass on its isolated network), and the lifecycle
gate codes above. The full response shape is in
`docs/openapi.yaml` under `OpenPilotReadinessReport`.

`POST /api/open-pilot/readiness/validate` (admin token) dry-runs one
configuration document: `{"registry": <operator registry v1>}` or
`{"manifest": <runtime manifest v1>}`. It uses the same strict decoders as the
runtime, rejects secret-looking material with `document_contains_secret`
without echoing the document, and compares each entry with the loaded runtime
registry: `runtime_match` is `exact`, `generation-differs` (warning: durable
states bound to the old generation will fail affinity), `unregistered`
(warning: restart required), or `slug-differs` (failure: repository IDs are
immutable identities). A manifest whose fingerprint differs from the loaded
registry warns with `runtime_fingerprint_stale`. Other codes are
`document_decode_failed`, `document_invalid`, `document_kind_ambiguous`, and
`runtime_registry_unavailable`. The dry run performs no cloning, filesystem,
network, or admission changes.

## Issue inbox

`GET /api/open-pilot/inbox` on the authenticated control listener lists intake
candidates before anything is dispatched. The read token is sufficient, and the
route is absent from model ingress. It is read-only: listing never claims an
issue, creates a run, or writes to Gitea.

The inbox checks the admission gate first. For each registered repository the
gate currently admits, it lists open issues that carry every
`OPEN_PILOT_REQUIRED_LABELS` label and, when `OPEN_PILOT_ASSIGNEE` is set,
open issues assigned to that account. It then adds durable claims stored for
those repositories. Repositories the gate rejects (disabled activation, a
closed or stale rollout, a legacy-singleton binding) are reported with an
`admission_code` and are not queried. `polled` shows whether
`OPEN_PILOT_POLL_REPOS` includes the repository; only polled repositories are
picked up automatically.

Each issue reports:

- `pickup`: `assigned`, `labels`, `both`, or `none`, with `missing_labels`.
- `spec_ready`: the body mentions acceptance criteria and test instructions,
  the same rule as `OPEN_PILOT_REQUIRE_ISSUE_SPEC`. The check fails only when
  that setting is `true`.
- `test_command`: the issue's Test Command, checked against the argv allowlist
  without executing it.
- `claim`: the durable lifecycle phase, run ID and status, whether a queued or
  running run owns the issue, next action, last step, last error, failing gate,
  branch, pull request, and repair attempts.
- `links`: the Gitea issue and pull request URLs and the Switchyard run, state,
  and readiness API paths.
- `eligible`, `action` (`dispatch`, `resume`, or `none`), and `reason_codes`
  (the codes of every failing entry in `checks[]`).

Inbox reason codes, in addition to the readiness codes `issue_run_active`,
`issue_affinity_stale`, `command_denied`, `rollout_proof_target_excluded`,
`repository_unregistered`, and `gate_unavailable`:

| Code | Meaning |
|---|---|
| `labels_missing` | The issue lacks a queue label and is not assigned to the bot. |
| `issue_not_open` | Gitea reports the issue as not open. |
| `issue_spec_missing` | Specification is required and the body lacks acceptance criteria or test instructions (a warning when not required). |
| `issue_claimed` | A durable lifecycle already owns the issue; resume it instead of dispatching again. |
| `issue_lifecycle_complete` | The lifecycle closed the issue. |
| `upstream_unavailable` | Gitea listing failed; eligibility is unknown. |
| `upstream_not_listed` | A durable claim exists but Gitea no longer lists the issue with queue labels or bot assignment, or Gitea was not queried. |

`upstream` is `available` when every admitted repository listed, `partial` or
`unavailable` when some or all listings failed, and `not_queried` when no
repository was admitted or no Gitea reader is configured. An empty `issues`
array with `upstream=available` is an empty queue; with any other value, the
queue may be incomplete. Durable claims are still shown from the store when
Gitea is down.

Query parameters are `repo` (slug or alias), `eligibility` (`all`, `eligible`,
`ineligible`, `claimed`), `phase`, `pickup` (`assigned`, `labels`, `none`),
`q` (substring of repo#issue, title, labels, or reason codes), `limit` (1-100,
default 25), and `cursor` (the previous page's `next_cursor`). Issues are
ordered by repository slug and issue number. A malformed query returns `400`.

The dashboard's Open Pilot inbox view (`#/inbox`, also linked from the
overview) renders this report. Filters, the page cursor, and the selected
issue are stored in one URL-encoded segment, for example
`#/inbox/repo=owner%2Frepo&eligibility=eligible&issue=owner%2Frepo%2342`, so a
filtered view or a single issue can be shared as a link. The detail panel
links to the Gitea issue and pull request and opens the Switchyard run in the
run cockpit. Dispatch calls `POST /api/workflows/open-pilot/run` and
resume calls `POST /api/open-pilot/state/resume`. Both apply the admission
gate, the atomic `(repo, issue)` claim, and admin authorization, so the inbox
adds no new path around them.

## Queue an issue by assignment

Set `OPEN_PILOT_ASSIGNEE=open-pilot` in the runtime configuration to enable
assignment-based pickup. Then assign a scoped issue to the Gitea `open-pilot`
account in a repository allowed by `OPEN_PILOT_POLL_REPOS`. The enabled
`gitea-open-pilot-poll` trigger picks it up on its next scheduled poll; assignment
is not an immediate webhook dispatch. In bounded proof mode, polling discovers the admitted repositories and queues
only the exact proof issue targets. An empty `OPEN_PILOT_ASSIGNEE` (the default)
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

Specification validation also accepts verification instructions (`verification`
or `verify`) alongside acceptance criteria. It does not require an executable
test command for documentation or operational evidence tasks. Command policy,
dependencies, provider qualification, and natural-run evidence still apply.

Set the run deadline longer than the coding-step deadline and leave room for
verification, review, and CI. A ten-minute run budget can interrupt an otherwise
productive coding session. When execution reaches its deadline, the worker
records the timeout through a separate bounded persistence context with the
original run-attempt fence. It releases the lease and reconciles the issue state
instead of leaving the run active until lease recovery. This does not allow an
expired or reclaimed attempt to write over its replacement.

An automatic CI repair records `open-pilot.agent-work` as the child's resume
cursor. If that coding step is interrupted, operator recovery continues the
unfinished correction on the existing branch and PR. The parent's failed CI
result and the two-attempt automatic repair budget remain retained.

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


## Prepared offline verification

`scripts/prepare_verifier.py` builds an operator-owned verification image for a
committed source revision. Dependencies are downloaded only during that image
build. Verification runs on the verifier's internal network with
`GOPROXY=off` and pnpm in offline mode, and never downloads.

### Profile contract

`backend/cmd/verifier/profiles.json` is the machine-readable preparation
contract. The verifier binary embeds it and `scripts/prepare_verifier.py` reads
the same file. Each profile names:

- `toolchains`: pinned versions. The current profile,
  `go1.26.6-node24-pnpm10.33.0-v2`, pins Go 1.26.6, Node 24, and pnpm 10.33.0.
  `web/package.json` must declare `pnpm@10.33.0` or omit `packageManager`.
- `inputs`: every dependency input bound to the image, including files that
  must stay absent: `backend/go.mod`, `backend/go.sum`, `web/package.json`,
  `web/pnpm-lock.yaml`, root `package.json`, `pnpm-lock.yaml`,
  `pnpm-workspace.yaml`, `.npmrc`, `web/.npmrc`, `go.work`, and `go.work.sum`.
- `prepared_inputs`: the inputs the build actually installs from (the backend
  Go module and the `web/` pnpm lockfile). Preparation rejects a revision that
  commits any other bound input, such as a pnpm workspace or `.npmrc`.
- `commands`: the exact argv commands the prepared image supports. The current
  profile supports only `make verify`. A manifest cannot widen this list; the
  verifier rejects any manifest command outside its profile.
- `manifest_version` and `reuse`: version 1 images (`exact-revision`) run only
  at their preparation revision. Version 2 images (`dependency-inputs`) are
  reused across revisions while every bound input still matches.

Adding a toolchain, input, or command means adding a profile to
`profiles.json`, rebuilding the verifier, and preparing a new image.

### Supported environments at runtime

Each verifier answers `GET /prepared` with its bound profile, toolchains,
preparation revision (`base_sha`), input hashes, and supported commands. It
reports `prepared: false` with an error when no bundle is configured or the
bundle names a profile this binary does not support. The API's repository
status provider projects this as `verifier_preparation` on each entry of
`GET /api/opencode-executor/status` and the readiness report's `executors[]`.
A verifier built before this route existed reports an explicit error there;
health is unaffected. Readiness adds a `verifier_preparation` check when
`test_command` is supplied: it passes for a listed command, fails with
`verifier_command_unsupported` otherwise, and warns with `verifier_unprepared`
for a verifier without a usable bundle.

### Binding and rejection

For each non-Git command the verifier:

1. snapshots the worktree and records its content manifest;
2. reads the bundle manifest and checks the profile, manifest version, and
   (for version 1) the exact revision;
3. rejects commands the manifest does not list, naming the supported ones;
4. hashes every bound input in the snapshot and rejects the attempt before
   running anything if an input is `stale` (different bytes), `missing`
   (expected but absent), or `unexpected` (present but absent at
   preparation); the error names the file and says to rerun
   `scripts/prepare_verifier.py`;
5. copies the immutable pnpm store into a fresh per-attempt cache directory
   under `/cache`, with a fresh Go build cache and temp directory beside it;
6. runs the command and deletes the snapshot and per-attempt cache afterwards.

A successful response carries both the source `manifest` and the `prepared`
identity (profile, manifest version, preparation revision), so a result is tied
to the exact source bytes and the dependency preparation it used. pnpm offline
and store settings are set under both `npm_config_` and `pnpm_config_`
prefixes, because pnpm 11 and later ignore `npm_config_`.

Hash checks are the first guard against stale dependencies. The offline
toolchain is the second: if a manifest were rebound to new inputs without
re-preparing, `GOPROXY=off` and offline pnpm fail on the missing packages
instead of reusing old ones.

### Test coverage

`make verify` runs these through the real `/verify` handler with fixture
repositories. The tests simulate preparation outside the boundary with a local
`file://` Go module proxy and an in-process npm registry, then verify offline:

- a Go fixture with a module dependency passes; after `go.mod` and `go.sum`
  move to a new version, the attempt is rejected as stale before any command
  runs, and a manifest rebound without re-preparing fails offline;
- a Go fixture whose module was never prepared fails with `GOPROXY=off`;
- a Node fixture with a registry dependency installs from the prepared store
  with `pnpm install --frozen-lockfile` and passes; with an empty store it fails
  offline; after `web/pnpm-lock.yaml` changes, it is rejected as stale;
- each attempt leaves no per-attempt cache and no `node_modules` in the source
  worktree.

`scripts/test_prepare_verifier.py` (`make verifier-preparation-test`) covers
input hashing at the exact revision, rejection of unsupported configuration,
the pnpm version pin, and the shared profile file. Building and running a real
prepared image (`scripts/prepare_verifier.py`, `make linked-worktree-smoke`,
`make open-pilot-topology-smoke`) needs Docker and remains operator-run; it is
not part of `make verify`.

### Building an image

Build the base verifier from the intended Switchyard runtime source, then run:

```bash
./deploy/docker-local build --target verifier -t switchyard-verifier:candidate backend
python3 scripts/prepare_verifier.py --source /absolute/repository \
  --revision FULL_COMMIT_SHA --verifier-image switchyard-verifier:candidate \
  --tag switchyard-verifier:prepared --record /absolute/preparation-record.json
```

The record contains the immutable image ID, source revision, input hashes, and
profile. Set the repository's optional `verifier_image` registry field to that
`sha256:` image ID and regenerate the installed topology. An image tag is not
accepted for this field. Preparation and runtime deployment are separate steps.
Set a bounded `OPEN_PILOT_COMMAND_TIMEOUT` appropriate for a cold full build
(the initial Switchyard profile is qualified with `10m`).
Rebuild preparation after any bound input changes; until then the verifier
rejects the repository's commands as stale.

### Isolation and limits

A container init process reaps orphaned test processes throughout each attempt. The prepared verifier retains a read-only root and source mounts, internal-only
network, no capabilities, and no Docker socket. It uses bounded 2 GiB temporary
and cache filesystems, 4 GiB memory, four CPUs, and the existing process and
command-time limits. Go parallelism is limited to two, with one concurrent package build; Vitest workers and the libuv thread pool are each limited to two to stay within the process budget. Package-manager writable state is copied per attempt; Go
modules remain read-only. Git-aware checks use a disposable index and snapshot commit, without copying
source repository configuration, hooks, credentials, or object storage. This
temporary commit supports archive tests; it is never published or used as source
revision evidence.
The source worktree manifest is checked again after verification.

An issue that failed its initial specification validation can be resumed after
its instructions are corrected. The lifecycle stores require exactly one
failed `open-pilot.validate-issue` step on the failed prior run and no branch,
worktree, or pull request evidence. An operator pause preserves this recovery
path. Resume fetches and validates the issue again; it does not bypass the
specification or admission gates, mutate the prior run, or admit duplicate work.

A canceled Open Pilot run still in `requested` can be resumed through the normal
lifecycle endpoint only when the prior run has no steps. Both store implementations
check this under their normal claim locks. The new run starts at issue validation,
retains prior history, and does not reuse evidence from later lifecycle phases.

Pre-branch recovery also permits a run paused by branch-preparation preflight
after successful issue validation, provided no branch/worktree identity exists
and those are its only two steps. It restarts with fresh issue validation. A
failed or completed branch step does not qualify; existing worktree recovery
continues to require the durable branch and repository identity.

## Bot identity and provider recovery

Use a token issued to the `open-pilot` Gitea account with `write:repository`,
`write:issue`, and `read:user` scopes. Set `GITEA_TOKEN` in the private runtime
configuration and `GITEA_GIT_USERNAME=open-pilot`; API writes and HTTPS Git pushes
then use the same account. Set `OPEN_PILOT_GIT_AUTHOR_NAME` and
`OPEN_PILOT_GIT_AUTHOR_EMAIL` to the bot's name and account email for commit
attribution. Do not put credentials in issue bodies, Git remotes, or tracked files.

Native OpenCode terminal provider errors stop the request immediately and retain
session/request correlation without provider response bodies. Authentication
failures are not retried automatically. The workflow pauses for operator recovery:
refresh the managed provider sign-in, verify it, and resume the affected issue.
Transient provider errors retain the bounded broker retry policy; cancellation
remains cancellation.

AI review of an executed worktree includes the actual committed pull-request patch
and its full base/head SHAs, in both the reviewer input and the retained review
artifact. It verifies that the clean worktree HEAD matches the published candidate.
Agent summaries and file statistics do not substitute for the patch. Automatic
review pauses for patches over 64 KiB, binary changes, or unavailable source
identity; these require operator review instead of approval from incomplete input.

When resuming a rejected review, the agent receives the stored task and current
review feedback. The correction starts a new durable commit intent; the prior
run retains its original intent, and commit-step recovery still reuses its
persisted intent.

The native model adapter requests OpenCode JSON-schema output for tool-call
responses and reads the validated `structured` field, including native
`tool-calls` completion. Only the StructuredOutput reporting tool is enabled in
the adapter; filesystem and execution tools remain denied. Formatting failure
pauses the caller instead of treating prose as a valid tool-call envelope.
The compatibility OCQ executor cannot provide this structured-output contract.

The managed OpenCode image includes pinned ripgrep, so file discovery and search
work on the restricted network without a first-use binary download.

CI-repair execution receives the original issue task, bounded check/review
feedback, and the current committed PR patch with full base/head SHAs. Stored
repair prompts are rebuilt to keep their executor contract current. The agent
edits and reads back files; Switchyard owns verification, commit, and push.
The patch context describes committed changes only, so a resumed agent must
also inspect working files for partial edits. Final review still requires a
clean worktree matching the published head.

An authenticated operator can add a correction when resuming a paused or failed
issue at its safe agent-work cursor. POST the lifecycle resume endpoint with
`{"expected_run_id":"<terminal-run-id>","feedback":"<correction>"}`.
Feedback is limited to 8192 bytes, retained in the new run, and fenced against
the prior run inside the store transaction. It cannot choose a later cursor,
replace a session, or bypass verification, review, CI, or merge policy. Combine
it with the explicit session-recovery query only when that separate recovery
is needed. Empty request bodies preserve ordinary resume behavior.


## Automatic repository discovery and preparation

Set `OPEN_PILOT_POLL_REPOS=*` to discover repositories visible to the existing
Gitea runtime account on every poll. Discovery includes repositories created
later. Archived repositories and repositories without issues are excluded from
pickup. Discovery does not bypass the execution registry or durable admission.
An operator-owned host reconciler prepares repositories when labeled or assigned
work appears; the API, OpenCode executor, and verifier never receive a Docker
socket or clone repositories during task execution.

`scripts/reconcile_open_pilot_repositories.py` maintains a private catalog of all
visible repositories independently of the active executor pool. Its default
capacity is four repositories, including pinned entries. Pending work stays in
the catalog when the pool is full. A repository with unfinished durable state
keeps its executor slot. Retired slots retain their source, credentials, worktrees,
containers, volumes, and history; only their running executors are stopped after
a successful topology transition.

The reconciler uses stable numeric Gitea repository IDs for managed paths. It
creates dedicated clones and private executor configuration, preserves the
remote default branch, and pins executor images by immutable local ID. The host
retains deterministic tags for those IDs, and generated services never pull
pinned local images from a registry. Newly
onboarded repositories use `required_checks: ["*"]`: concrete checks must exist
on the exact pull request head, and every reported check must pass. An empty or
aggregate-only success cannot satisfy that policy. Existing repositories retain
their explicit CI contexts. Normal specification, command, verification, review,
write, merge, and close gates remain in force. Source/executor preparation does
not prove that every project's dependencies or test commands are supported by
the selected verifier image. Configure a repository-specific prepared verifier
profile when its tests need dependencies beyond the image's ambient toolchains.

Run discovery without preparation first:

```bash
python3 scripts/reconcile_open_pilot_repositories.py \
  --env-file /absolute/path/to/switchyard.env \
  --registry /absolute/path/to/open-pilot/registry.json \
  --workspace-root /absolute/path/to/automatic-workspaces \
  --catalog /absolute/path/to/private-state/catalog.json \
  --deployment-root /absolute/path/to/switchyard \
  --control-url http://127.0.0.1:28080 \
  --pinned switchyard,actions-smoke-test
```

Add `--apply`, `--opencode-image sha256:<immutable-id>`, and
`--verifier-image sha256:<immutable-id>` to prepare selected repositories and
write a candidate registry. Add `--activate` to publish the validated topology
through the installed Compose wrapper. Activation defers when an operator pause
or unfinished runtime runs exist. It saves the previous topology, preserves the
completed proof identities, and uses the authenticated proof-to-allow transition
for the new fingerprint. It never rewrites durable affinity or proof history.
Install the operator timer only after validating its exact deployment paths,
image IDs, account visibility, catalog, preparation, and recovery path.

Render `deploy/switchyard-repository-discovery.service.in` with the absolute
deployment paths, control URL, pool capacity, and immutable executor image IDs.
Install it together with `deploy/switchyard-repository-discovery.timer` under
the operator's user systemd directory, then run `systemctl --user daemon-reload`
and `systemctl --user enable --now switchyard-repository-discovery.timer`.
The timer runs five minutes after each completed reconciliation. Busy runtime
or operator pauses retain the catalog with a deferral reason and retry on the
next timer run. Other failures remain visible in the service journal.
