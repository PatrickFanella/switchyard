# Open Pilot Autonomy v1 Implementation Plan

> **Historical plan:** The lifecycle and policy path described here was subsequently implemented behind safe configuration gates. Unchecked boxes are not current status; use `docs/roadmap.md` for the remaining Open Pilot backlog.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Make Open Pilot autonomy v1 complete only when the pull request is merged and the source issue is closed, with durable workflow state so failed/stopped runs can resume safely.

**Architecture:** Add a persisted Open Pilot lifecycle state machine beside the existing generic `runs`/`run_steps` records. Keep the existing registry-backed pipeline, but extend it from “prepare PR” to “review, wait for checks, merge, close issue,” with each external transition recorded in DB state, run steps, events, and artifacts. Switchyard, not the OpenCode agent, owns push/PR/review/merge/close decisions; the agent produces committed branch changes and summaries.

**Tech Stack:** Go control-plane backend, Postgres migrations + MemoryStore parity, Gitea HTTP API, existing Switchyard Runner/Pipeline/Store/API contracts, Vite React TypeScript HUD, Docker Compose homelab deployment.

---

## Requirements Decisions

- Success means: PR merged and issue closed.
- Merge authority: policy-gated auto merge.
- Required gates: local Switchyard verification, external CI checks, and AI review approval.
- Resume state: persisted DB state machine, not only run input replay.
- Planning route: full written plan plus grill-me review before implementation.
- Persistence mode: hybrid; this repo plan is the durable artifact, and conversation summaries preserve context.
- Grill decision: close the issue immediately after merge succeeds; no post-merge/deploy gate in v1.
- Grill decision: v1 may auto-merge only PRs that Switchyard created or explicitly recorded as its own state; do not auto-takeover arbitrary external PRs.
- Grill decision: CI checks are required by configured names, not “all checks.” Add `OPEN_PILOT_REQUIRED_CHECKS` and fail clearly when required checks are absent or failing.
- Grill decision: if AI review requests changes, mark the state failed and resumable; resume starts at `agent-work` with review feedback added to the prompt.
- Grill decision: allow at most two automatic repair resumes, then pause for operator intervention.

---

## File Structure

- Create `backend/internal/controlplane/open_pilot_state.go`: Open Pilot lifecycle enums, state record, transition validation, and helper summaries.
- Create `backend/internal/controlplane/open_pilot_state_test.go`: state-machine unit tests.
- Create `backend/internal/controlplane/migrations/006_open_pilot_state.sql`: durable state table keyed by repo+issue and linked run IDs.
- Modify `backend/internal/controlplane/types.go`: API response/request types for Open Pilot state and lifecycle actions.
- Modify `backend/internal/controlplane/store.go`: MemoryStore support for Open Pilot state CRUD and transition history.
- Modify `backend/internal/controlplane/postgres_store.go`: PostgresStore support for Open Pilot state CRUD and transition history.
- Modify `backend/internal/controlplane/migrate_test.go`: migration coverage for new tables/indexes.
- Modify `backend/internal/controlplane/pipeline.go`: add review/check/merge/close steps to `open-pilot-prototype`, or rename to `open-pilot-v1` when behavior lands.
- Modify `backend/internal/controlplane/runner.go`: let pipeline steps read/write durable Open Pilot state and resume from the state machine.
- Modify `backend/internal/controlplane/executor.go`: implement new Open Pilot steps and tighten completion/failure summaries.
- Modify `backend/internal/controlplane/gitea.go`: add Gitea APIs for PR lookup, review/status checks, merge, and issue close.
- Modify `backend/internal/controlplane/http.go`: expose Open Pilot state and operator retry/resume endpoints.
- Modify `backend/internal/controlplane/*_test.go`: add integration-style tests for state persistence, review gates, merge, close, and resume.
- Modify `web/src/App.tsx`: show Open Pilot lifecycle state, gates, and next action on run detail pages.
- Modify `docs/openapi.yaml`, `README.md`, `.env.example`, `docker-compose.yml`, `docs/roadmap.md`: document new env knobs, API shapes, and v1 success semantics.

---

## Lifecycle State Machine

Use these phases exactly in v1:

```text
requested
validated
branch_prepared
agent_completed
verified
pushed
pr_created
ai_reviewed
ci_passed
merge_ready
merged
issue_closed
failed
paused
```

Terminal success is `issue_closed`. `failed` and `paused` are resumable. Every state record must keep at least:

```go
type OpenPilotIssueState struct {
    ID              string            `json:"id"`
    Repo            string            `json:"repo"`
    Issue           string            `json:"issue"`
    Phase           OpenPilotPhase    `json:"phase"`
    RunID           string            `json:"run_id,omitempty"`
    LastStepName    string            `json:"last_step_name,omitempty"`
    Branch          string            `json:"branch,omitempty"`
    Base            string            `json:"base,omitempty"`
    WorktreePath    string            `json:"worktree_path,omitempty"`
    PullRequestURL  string            `json:"pull_request_url,omitempty"`
    PullRequestID   int               `json:"pull_request_id,omitempty"`
    PullRequestNum  int               `json:"pull_request_number,omitempty"`
    MergeCommitSHA  string            `json:"merge_commit_sha,omitempty"`
    IssueClosed     bool              `json:"issue_closed"`
    GateStatus      map[string]string `json:"gate_status,omitempty"`
    ResumeInputs    map[string]string `json:"resume_inputs,omitempty"`
    RepairAttempts  int               `json:"repair_attempts"`
    MaxRepairAttempts int             `json:"max_repair_attempts"`
    LastError       string            `json:"last_error,omitempty"`
    UpdatedAt       time.Time         `json:"updated_at"`
    CreatedAt       time.Time         `json:"created_at"`
}
```

`GateStatus` keys for v1:

- `local_verify`: `pending|passed|failed|skipped`
- `ai_review`: `pending|approved|changes_requested|failed|skipped`
- `ci_checks`: `pending|passed|failed|timeout|skipped`
- `merge`: `pending|merged|failed|skipped`
- `issue_close`: `pending|closed|failed|skipped`

---

### Task 1: Persisted Open Pilot State Model

**Files:**
- Create: `backend/internal/controlplane/open_pilot_state.go`
- Create: `backend/internal/controlplane/open_pilot_state_test.go`
- Create: `backend/internal/controlplane/migrations/006_open_pilot_state.sql`
- Modify: `backend/internal/controlplane/types.go`
- Modify: `backend/internal/controlplane/store.go`
- Modify: `backend/internal/controlplane/postgres_store.go`
- Modify: `backend/internal/controlplane/migrate_test.go`

- [ ] **Step 1: Write failing state-machine tests**

Add tests for allowed transitions:

```go
func TestOpenPilotPhaseAllowsV1Transitions(t *testing.T) {
    allowed := []struct{ from, to OpenPilotPhase }{
        {OpenPilotPhaseRequested, OpenPilotPhaseValidated},
        {OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared},
        {OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted},
        {OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified},
        {OpenPilotPhaseVerified, OpenPilotPhasePushed},
        {OpenPilotPhasePushed, OpenPilotPhasePRCreated},
        {OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed},
        {OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed},
        {OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady},
        {OpenPilotPhaseMergeReady, OpenPilotPhaseMerged},
        {OpenPilotPhaseMerged, OpenPilotPhaseIssueClosed},
    }
    for _, tt := range allowed {
        if err := ValidateOpenPilotTransition(tt.from, tt.to); err != nil {
            t.Fatalf("%s -> %s rejected: %v", tt.from, tt.to, err)
        }
    }
}

func TestOpenPilotPhaseRejectsSkippingMerge(t *testing.T) {
    err := ValidateOpenPilotTransition(OpenPilotPhasePRCreated, OpenPilotPhaseIssueClosed)
    if err == nil || !strings.Contains(err.Error(), "invalid open pilot transition") {
        t.Fatalf("expected invalid transition, got %v", err)
    }
}
```

- [ ] **Step 2: Implement `open_pilot_state.go`**

Define `OpenPilotPhase`, `OpenPilotIssueState`, `OpenPilotGateStatus`, and `ValidateOpenPilotTransition`. Allow transitions from any non-terminal phase to `failed` or `paused`. Allow `failed`/`paused` to resume to the recorded `LastStepName` phase only through the runner resume path, not by direct arbitrary transition.

- [ ] **Step 3: Add migration**

Create table `open_pilot_issue_states`:

```sql
create table if not exists open_pilot_issue_states (
  id text primary key,
  repo text not null,
  issue text not null,
  phase text not null,
  run_id text,
  last_step_name text,
  branch text,
  base text,
  worktree_path text,
  pull_request_url text,
  pull_request_id integer,
  pull_request_number integer,
  merge_commit_sha text,
  issue_closed boolean not null default false,
  gate_status jsonb not null default '{}'::jsonb,
  resume_inputs jsonb not null default '{}'::jsonb,
  repair_attempts integer not null default 0,
  max_repair_attempts integer not null default 2,
  last_error text,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  unique(repo, issue)
);
create index if not exists idx_open_pilot_issue_states_phase on open_pilot_issue_states(phase);
create index if not exists idx_open_pilot_issue_states_run_id on open_pilot_issue_states(run_id);
```

- [ ] **Step 4: Add Store methods**

Add methods:

```go
OpenPilotIssueState(repo string, issue string) (OpenPilotIssueState, error)
UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error)
ListOpenPilotIssueStates() []OpenPilotIssueState
```

MemoryStore must deep-copy maps. PostgresStore must marshal/unmarshal `gate_status` and `resume_inputs`.

- [ ] **Step 5: Verify and commit**

Run:

```bash
cd backend && go test ./internal/controlplane
make verify
make switchyard-smoke
git diff --check
```

Commit:

```bash
git add backend/internal/controlplane
git commit -m "feat: persist open pilot lifecycle state"
```

---

### Task 2: Attach State Machine to Existing Open Pilot Pipeline

**Files:**
- Modify: `backend/internal/controlplane/runner.go`
- Modify: `backend/internal/controlplane/executor.go`
- Test: `backend/internal/controlplane/executor_test.go`
- Test: `backend/internal/controlplane/runner_test.go`

- [ ] **Step 1: Add tests for phase updates through current steps**

Test a normal OCQ success with PR dry-run/disabled transitions through at least `agent_completed` and records branch/prompt/response resume inputs. Test a push/PR allowed path reaches `pr_created`.

- [ ] **Step 2: Update Open Pilot step handlers**

At the end of existing steps, update state:

- `validate-issue` -> `validated`
- `branch-plan` -> `branch_prepared`
- `agent-work` success -> `agent_completed`
- `verify-command` success -> `verified`
- `push-branch` executed or remote head exists -> `pushed`
- `pr-plan` created -> `pr_created`

On step error, transition to `failed` with `LastError`, `LastStepName`, and current resume inputs.

- [ ] **Step 3: Make resume prefer DB state**

When a run starts with `repo`/`issue`, the Open Pilot hydrator should first load `OpenPilotIssueState(repo, issue)`. If phase is `failed` or `paused`, derive resume state from DB fields before falling back to `resume.*` run inputs.

- [ ] **Step 4: Verify and commit**

Run backend tests, `make verify`, smoke, and commit:

```bash
git commit -m "feat: bind open pilot pipeline to lifecycle state"
```

---

### Task 3: Gitea PR Lookup, CI Status, Merge, and Issue Close APIs

**Files:**
- Modify: `backend/internal/controlplane/gitea.go`
- Test: `backend/internal/controlplane/gitea_test.go`

- [ ] **Step 1: Add Gitea contract tests with `httptest`**

Cover these API calls:

- `GET /api/v1/repos/{owner}/{repo}/pulls?state=open&head={owner}:{branch}` for PR lookup.
- `GET /api/v1/repos/{owner}/{repo}/commits/{sha}/status` or equivalent configured status endpoint for CI status.
- `POST /api/v1/repos/{owner}/{repo}/pulls/{index}/merge` for merge.
- `PATCH /api/v1/repos/{owner}/{repo}/issues/{index}` with `{"state":"closed"}` for issue close.

- [ ] **Step 2: Extend `GiteaWriter` safely**

Split interfaces if needed:

```go
type GiteaPullRequestReader interface {
    FindPullRequestByHead(ctx context.Context, repo string, head string) (GiteaPullRequest, bool, error)
    PullRequestChecks(ctx context.Context, repo string, pr GiteaPullRequest) (GiteaCheckSummary, error)
}

type GiteaPullRequestMerger interface {
    MergePullRequest(ctx context.Context, repo string, number int) (GiteaMergeResult, error)
    CloseIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error)
}
```

Keep `CreatePullRequest` behavior backward-compatible.

- [ ] **Step 3: Add explicit timeout/env knobs**

Use existing HTTP client timeout for v1 unless a test proves it is insufficient. Document any Gitea status API assumptions in README.

- [ ] **Step 4: Verify and commit**

Run backend tests and commit:

```bash
git commit -m "feat: add gitea review and merge client APIs"
```

---

### Task 4: AI Review Gate

**Files:**
- Modify: `backend/internal/controlplane/pipeline.go`
- Modify: `backend/internal/controlplane/executor.go`
- Modify: `backend/internal/controlplane/profiles.go`
- Test: `backend/internal/controlplane/executor_test.go`

- [ ] **Step 1: Add pipeline step**

Insert after `pr-plan`:

```go
{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}
```

- [ ] **Step 2: Define review prompt contract**

The prompt must include: repo/issue, PR URL, branch, verification summary, changed files/diff artifact when available, and agent summary. Output must be one of:

```text
APPROVED: <one paragraph>
CHANGES_REQUESTED: <one paragraph>
```

- [ ] **Step 3: Implement parser and gate**

`APPROVED` transitions to `ai_reviewed` and `GateStatus["ai_review"]="approved"`. `CHANGES_REQUESTED` transitions to `failed` with `GateStatus["ai_review"]="changes_requested"`, stores the review body in `ResumeInputs["ai_review.feedback"]`, increments `repair_attempts`, and sets the next resume cursor to `agent-work`. If `repair_attempts >= max_repair_attempts`, transition to `paused` instead of auto-resuming.

- [ ] **Step 4: Test approval and rejection**

Use `httptest` llama-line/OCQ-compatible model response. Assert rejection stops before CI/merge.

- [ ] **Step 5: Verify and commit**

Run backend tests, full verify, smoke, and commit:

```bash
git commit -m "feat: add open pilot ai review gate"
```

---

### Task 5: CI Check Gate

**Files:**
- Modify: `backend/internal/controlplane/pipeline.go`
- Modify: `backend/internal/controlplane/executor.go`
- Modify: `.env.example`, `docker-compose.yml`
- Test: `backend/internal/controlplane/executor_test.go`

- [ ] **Step 1: Add pipeline step**

Insert after `ai-review`:

```go
{ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}
```

- [ ] **Step 2: Add env knobs**

Document and implement:

```text
OPEN_PILOT_CI_CHECK_MODE=disabled|poll|require-pass
OPEN_PILOT_REQUIRED_CHECKS=verify,lint,test
OPEN_PILOT_CI_CHECK_TIMEOUT=10m
OPEN_PILOT_CI_CHECK_INTERVAL=15s
```

Autonomy full should default to `require-pass` only if Gitea checks are configured; otherwise fail closed with a clear warning unless explicitly disabled.

- [ ] **Step 3: Implement polling**

Poll until all configured required checks pass, any configured required check fails, a configured required check is absent after timeout, or timeout. Do not require every reported check. Only names in `OPEN_PILOT_REQUIRED_CHECKS` block merge. Transition:

- pass -> `ci_passed`
- fail -> `failed`, `GateStatus["ci_checks"]="failed"`
- timeout -> `failed`, `GateStatus["ci_checks"]="timeout"`
- disabled -> keep explicit `skipped` and do not allow merge in full autonomy unless user configured disabled deliberately.

- [ ] **Step 4: Verify and commit**

Run backend tests and commit:

```bash
git commit -m "feat: require open pilot ci checks"
```

---

### Task 6: Policy-Gated Merge and Issue Close

**Files:**
- Modify: `backend/internal/controlplane/pipeline.go`
- Modify: `backend/internal/controlplane/executor.go`
- Test: `backend/internal/controlplane/executor_test.go`

- [ ] **Step 1: Add final pipeline steps**

Append:

```go
{ID: "merge-pr", Position: 10, Name: "open-pilot.merge-pr", Executor: "gitea"},
{ID: "close-issue", Position: 11, Name: "open-pilot.close-issue", Executor: "gitea"},
```

- [ ] **Step 2: Add merge mode env**

Document and implement:

```text
OPEN_PILOT_MERGE_MODE=disabled|dry-run|allow
OPEN_PILOT_CLOSE_ISSUE_MODE=disabled|dry-run|allow
```

`OPEN_PILOT_AUTONOMY_MODE=full` should default both to `allow`, but only behind existing reverse-proxy auth warning and after gates pass.

- [ ] **Step 3: Enforce merge-ready invariant**

Before merge, require:

```go
state.Phase == OpenPilotPhaseCIPassed
state.GateStatus["local_verify"] == "passed" || explicit configured skip
state.GateStatus["ai_review"] == "approved"
state.GateStatus["ci_checks"] == "passed"
state.PullRequestNum > 0
```

Then transition to `merge_ready`, call merge API, persist `merge_commit_sha`, transition to `merged`.

- [ ] **Step 4: Close issue only after merge**

Require `state.Phase == OpenPilotPhaseMerged`; then call issue close API immediately and transition to `issue_closed`. Run summary must be exactly: `Open Pilot completed OWNER/REPO#123: PR merged and issue closed`.

- [ ] **Step 5: Verify and commit**

Run backend tests, full verify, smoke, and commit:

```bash
git commit -m "feat: auto merge and close open pilot issues"
```

---

### Task 7: API and HUD State Visibility

**Files:**
- Modify: `backend/internal/controlplane/http.go`
- Modify: `docs/openapi.yaml`
- Modify: `web/src/App.tsx`
- Test: `backend/internal/controlplane/http_test.go`, `web/src/App.test.tsx`

- [ ] **Step 1: Add API endpoints**

Add:

```text
GET /api/open-pilot/states
GET /api/open-pilot/states/{repo}/{issue}
POST /api/open-pilot/states/{repo}/{issue}/resume
POST /api/open-pilot/states/{repo}/{issue}/pause
```

Use URL escaping rules already used by Gitea repo parsing; if path encoding is awkward for `OWNER/REPO`, prefer query params:

```text
GET /api/open-pilot/state?repo=OWNER/REPO&issue=123
```

- [ ] **Step 2: Show state in HUD**

On run detail, show phase, gate status, PR URL, merge SHA, and next action. Keep visual changes minimal; do not redesign the whole UI.

- [ ] **Step 3: Update OpenAPI**

Add schemas for `OpenPilotIssueState`, gate status, and resume/pause responses.

- [ ] **Step 4: Verify and commit**

Run:

```bash
cd backend && go test ./internal/controlplane
pnpm --dir web run test
make verify
make switchyard-smoke
git diff --check
git commit -m "feat: expose open pilot lifecycle state"
```

---

### Task 8: Resume Semantics and Failure Diagnostics

**Files:**
- Modify: `backend/internal/controlplane/runner.go`
- Modify: `backend/internal/controlplane/executor.go`
- Test: `backend/internal/controlplane/executor_test.go`, `backend/internal/controlplane/http_test.go`
- Modify: `README.md`, `docs/roadmap.md`

- [ ] **Step 1: Define resume cursor mapping**

Map phases to next pipeline step:

```text
validated -> branch-plan
branch_prepared -> prepare-prompt
agent_completed -> verify-command or push-branch when no test_command
verified -> push-branch
pushed -> pr-plan
pr_created -> ai-review
ai_reviewed -> ci-checks
ci_passed -> merge-pr
merged -> close-issue
```

- [ ] **Step 2: Implement resume endpoint behavior**

`POST /api/open-pilot/state?repo=OWNER/REPO&issue=123/resume` should enqueue a new Open Pilot run with inputs from DB state, not just old `resume.*` keys.

- [ ] **Step 3: Improve diagnostics**

On every failure, persist:

- phase
- step name
- failing gate
- short operator summary
- recommended next action: `fix_agent_commit`, `rerun_verify`, `wait_ci`, `manual_review`, `check_gitea_token`, or `resume`

AI review changes-requested failures are special: the next resume cursor is `agent-work`, and the review feedback must be inserted into the resumed implementation prompt. Automatic repair resumes stop after `max_repair_attempts` and leave the state `paused`.

- [ ] **Step 4: Test stopped/failed pickup**

Simulate a failed run at `ci-checks`, then call resume and assert only `ci-checks`, `merge-pr`, and `close-issue` run.

- [ ] **Step 5: Verify, commit, sync, deploy**

Run full checks and commit:

```bash
git commit -m "feat: resume open pilot lifecycle runs"
```

Then follow project process: rebase/push `origin main`, deploy with `rtk docker compose up -d --build`, and verify `/healthz`, `/api/operator-safety`, `/api/pipelines`, new Open Pilot state endpoint, and web root.

---

## Self-Review

- Spec coverage: plan covers PR merge, issue close, policy gates, DB state machine, resume after failed/stopped runs, API/HUD visibility, and diagnostics.
- Placeholder scan: no `TBD` or unspecified “add appropriate handling” steps remain; ambiguous Gitea CI status API is isolated to Task 3/5 with explicit tests required because Gitea deployments differ.
- Type consistency: `OpenPilotIssueState`, `OpenPilotPhase`, `GateStatus`, and phase names are used consistently across tasks.
- YAGNI boundary: no marketplace/profile editor, no generalized DAG engine, no arbitrary agent merge authority. This is a linear Open Pilot v1 lifecycle only.
