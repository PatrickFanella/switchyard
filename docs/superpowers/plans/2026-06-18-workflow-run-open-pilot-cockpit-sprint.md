# Workflow Run Detail + Open Pilot Cockpit Sprint Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Turn selected run detail and Open Pilot lifecycle state into a dedicated operator cockpit with deep links, filters, clearer retry/cancel/resume actions, artifacts, gates, and trace/correlation affordances.

**Architecture:** Keep existing backend contracts and hash navigation. Extract the current `RunDetailPanel` logic out of `OverviewDashboard.tsx`, add run-detail-specific navigation state, and create a dedicated cockpit page rendered for `#/runs/<run-id>` while Overview continues to show summary bands and links into the cockpit. Reuse existing `RunDetail`, `OpenPilotStateResponse`, `runAction`, `stepAction`, and `resumeOpenPilotState` APIs.

**Tech Stack:** React 19, TypeScript, existing HUD primitives, existing Switchyard run/Open Pilot APIs, Vitest/jsdom.

---

## Approved Scope

- Add deep links: `#/runs/<run-id>` and keep `#/overview/runs/<run-id>` as a compatibility route that selects the same run.
- Extract selected-run detail out of `OverviewDashboard.tsx` into focused components.
- Add a dedicated run cockpit page with these scan bands:
  - Run command strip
  - Open Pilot lifecycle/gates
  - Step timeline
  - Evidence/artifacts/events
  - Correlation + trace IDs
- Add filters for step status/executor and event/artifact search.
- Improve action clarity for retry run, cancel run, resume Open Pilot, retry step, and cancel step.
- Preserve backend/API behavior; no schema migrations in this sprint.

## File Structure Target

- Modify: `web/src/hooks/useHashNavigation.ts` — parse/format `#/runs/<run-id>`.
- Modify: `web/src/hooks/useDashboardData.ts` — load selected run for overview and run cockpit views.
- Modify: `web/src/types.ts` — add helper view/nav types if needed.
- Create: `web/src/components/RunCockpitPage.tsx` — dedicated run/Open Pilot cockpit.
- Create: `web/src/components/runCockpit.tsx` — smaller presentational pieces if `RunCockpitPage.tsx` gets too large.
- Modify: `web/src/components/OverviewDashboard.tsx` — remove heavy `RunDetailPanel` or replace it with compact selected-run summary and link/action buttons.
- Modify: `web/src/App.tsx` — render cockpit route.
- Modify: `web/src/App.test.tsx` — deep-link, filters, action affordance tests.

## Task 1: Extend Navigation for Run Cockpit

**Files:**
- Modify: `web/src/hooks/useHashNavigation.ts`
- Modify: `web/src/types.ts`
- Test: `web/src/App.test.tsx`

- [x] **Step 1: Add `runs` as a routable view**

Update `ViewID` in `web/src/types.ts`:

```ts
export type ViewID = 'overview' | 'runs' | 'queues' | 'observability' | 'sessions' | 'logs' | 'prompts' | 'config' | 'agents' | 'skills';
```

Do not add `runs` to the top-level `views` array; it is a detail route, not a tab.

- [x] **Step 2: Parse run cockpit hashes**

In `web/src/hooks/useHashNavigation.ts`, ensure:

```ts
#/runs/<run-id>
```

returns:

```ts
{ activeView: 'runs', selectedRunID: safeDecodeURIComponent(runID), selectedRequestID: '' }
```

Keep existing `#/overview/runs/<run-id>` behavior, but include `selectedRequestID: ''` in every returned object.

- [x] **Step 3: Format run cockpit hashes**

Update `formatHash` so:

```ts
formatHash('runs', 'run-123') === '#/runs/run-123'
```

If `activeView === 'runs'` and no run ID is present, return `#/overview`.

- [x] **Step 4: Add navigation setter**

Add `openRunCockpit(runID: string)` to `useHashNavigation()` that sets `activeView: 'runs'` and `selectedRunID: runID`.

- [x] **Step 5: Add tests**

In `web/src/App.test.tsx`, add tests for:

```tsx
window.location.hash = '#/runs/run-1';
render(<App />);
expect(html).toContain('Run cockpit');
```

and malformed `#/runs/%E0%A4%A` falls back without throwing and does not show a bogus selected ID.

## Task 2: Make Dashboard Data Load Run Cockpit Detail

**Files:**
- Modify: `web/src/hooks/useDashboardData.ts`

- [x] **Step 1: Load selected run for cockpit view**

Change the auto-select effect to avoid forcing a run when `activeView === 'runs'` and there is no selected run:

```ts
if (activeView === 'overview' && !selectedRunID && dashboard.runs.length > 0) {
  setSelectedRunID(dashboard.runs[0].id);
}
```

Keep this behavior unchanged for Overview.

- [x] **Step 2: Ensure cockpit loads detail**

Keep `loadSelectedRun(selectedRunID)` independent of active view so `#/runs/<id>` loads detail on mount.

- [x] **Step 3: Expose explicit `openRunCockpit` from App, not data hook**

No API change needed in `useDashboardData`; navigation owns route state.

- [x] **Step 4: Verify typecheck**

Run: `pnpm --dir web run format`

Expected: TypeScript passes.

## Task 3: Extract Run Cockpit Components

**Files:**
- Create: `web/src/components/runCockpit.tsx`
- Create: `web/src/components/RunCockpitPage.tsx`
- Modify: `web/src/components/OverviewDashboard.tsx`

- [x] **Step 1: Create shared helpers**

Create `web/src/components/runCockpit.tsx`:

```tsx
import type { Artifact, OpenPilotStateResponse, RunDetail, RunStep } from '../types';

export const terminalRunStatuses = new Set(['succeeded', 'failed', 'canceled']);
export const activeRunStatuses = new Set(['queued', 'running']);
export const retryableStepStatuses = new Set(['failed', 'canceled', 'timed_out']);
export const cancelableStepStatuses = new Set(['queued', 'running']);

export function runDurationMS(detail: RunDetail | null) {
  if (!detail?.run.started_at || !detail.run.finished_at) return 0;
  const started = new Date(detail.run.started_at).getTime();
  const finished = new Date(detail.run.finished_at).getTime();
  return Number.isFinite(started) && Number.isFinite(finished) && finished >= started ? finished - started : 0;
}

export function stepDurationMS(step: RunStep) {
  if (!step.started_at || !step.finished_at) return 0;
  const started = new Date(step.started_at).getTime();
  const finished = new Date(step.finished_at).getTime();
  return Number.isFinite(started) && Number.isFinite(finished) && finished >= started ? finished - started : 0;
}

export function artifactLabel(artifact: Artifact) {
  return `${artifact.kind} · ${artifact.name}`;
}

export function canResumeOpenPilot(state: OpenPilotStateResponse | null) {
  return Boolean(state && ['failed', 'paused', 'agent_completed', 'merged'].includes(state.state.phase));
}
```

- [x] **Step 2: Create page shell**

Create `web/src/components/RunCockpitPage.tsx` with props:

```ts
type RunCockpitPageProps = {
  selectedRunID: string;
  selectedRunDetail: RunDetail | null;
  openPilotState: OpenPilotStateResponse | null;
  runActionID: string;
  onRetryRun: (runID: string) => void;
  onCancelRun: (runID: string) => void;
  onResumeOpenPilotState: (state: OpenPilotIssueState) => void;
  onStepAction: (runID: string, stepID: string, action: 'retry' | 'cancel') => void;
  onBackToOverview: () => void;
  onRefresh: () => void;
};
```

Use `HudSection` with:

```tsx
<HudSection kicker="Run cockpit" title="Run cockpit" note="Deep run inspection, Open Pilot lifecycle gates, evidence, and operator actions." />
```

- [x] **Step 3: Add empty state**

If `!selectedRunID`, render:

```tsx
<HudEmptyState>Select a run from Overview to open the run cockpit.</HudEmptyState>
```

If `selectedRunID && !selectedRunDetail`, render:

```tsx
<HudEmptyState>Loading run detail or the run was not found.</HudEmptyState>
```

- [x] **Step 4: Replace Overview heavy detail**

In `OverviewDashboard.tsx`, replace the current heavy selected-run timeline panel with a compact summary card:

- selected run ID/status/workflow
- artifact count
- step count
- buttons: `Open cockpit`, `Retry run`, `Cancel run`

Add prop:

```ts
onOpenRunCockpit: (runID: string) => void;
```

Clicking `Open cockpit` calls `onOpenRunCockpit(selectedRunDetail.run.id)`.

## Task 4: Add Open Pilot Lifecycle/Gates Band

**Files:**
- Modify: `web/src/components/RunCockpitPage.tsx`

- [x] **Step 1: Add lifecycle band**

When `selectedRunDetail.run.workflow_id === 'open-pilot'`, render an Open Pilot panel with:

- `repo#issue`
- phase chip
- next action
- branch/base
- worktree path
- pull request URL
- merge SHA
- issue closed
- repair attempts
- last error

- [x] **Step 2: Add gate grid**

Render `Object.entries(openPilotState?.state.gate_status ?? {})` as chips/cards. If empty, show `No gate status recorded yet.`

- [x] **Step 3: Add resume action**

If `canResumeOpenPilot(openPilotState)` is true, show `Resume Open Pilot`. Disable while `runActionID === `${openPilotState.state.id}:open-pilot:resume``.

- [x] **Step 4: Non-Open Pilot state**

For non-Open Pilot runs, show a small note: `This run is not managed by Open Pilot lifecycle state.`

## Task 5: Add Step Timeline Filters and Actions

**Files:**
- Modify: `web/src/components/RunCockpitPage.tsx`

- [x] **Step 1: Add local filter state**

Use:

```ts
const [stepStatus, setStepStatus] = useState('all');
const [stepExecutor, setStepExecutor] = useState('all');
```

- [x] **Step 2: Render filter controls**

Add selects for:

- status: `all`, `queued`, `running`, `succeeded`, `failed`, `canceled`, `timed_out`
- executor: derive from `new Set(selectedRunDetail.steps.map(step => step.executor))`

- [x] **Step 3: Render filtered step cards**

Each step card should show:

- position/name/executor
- status chip
- model
- attempt count
- duration
- output summary
- error
- correlation IDs
- `Retry step` when retryable
- `Cancel step` when cancelable

- [x] **Step 4: Preserve existing step action semantics**

Call:

```ts
onStepAction(selectedRunDetail.run.id, step.id, 'retry')
onStepAction(selectedRunDetail.run.id, step.id, 'cancel')
```

Disable buttons based on `runActionID` exactly like the existing Overview behavior.

## Task 6: Add Evidence, Artifacts, Events, and Correlation Band

**Files:**
- Modify: `web/src/components/RunCockpitPage.tsx`

- [x] **Step 1: Add evidence search state**

Use:

```ts
const [evidenceQuery, setEvidenceQuery] = useState('');
```

- [x] **Step 2: Render artifacts**

Show artifact cards with:

- artifact name
- kind
- URI
- created time

Filter by query against name/kind/URI.

- [x] **Step 3: Render events**

Show event cards with:

- type
- message
- created time

Filter by query against type/message.

- [x] **Step 4: Render correlation index**

Collect all step `correlation_ids` into a list:

```ts
const correlations = selectedRunDetail.steps.flatMap(step => Object.entries(step.correlation_ids ?? {}).map(([key, value]) => ({ step: step.name, key, value })));
```

Render them in a separate `Correlation + traces` panel. Show trace-ish keys first: `trace_id`, `span_id`, `llama_line_request_id`, `ocq_request_id`, `model_broker_request_id`.

## Task 7: Wire App and Overview

**Files:**
- Modify: `web/src/App.tsx`
- Modify: `web/src/components/OverviewDashboard.tsx`

- [x] **Step 1: Import the cockpit**

In `App.tsx`:

```ts
import { RunCockpitPage } from './components/RunCockpitPage';
```

- [x] **Step 2: Use `openRunCockpit`**

Destructure from `useHashNavigation()`:

```ts
openRunCockpit
```

Pass it to Overview as `onOpenRunCockpit`.

- [x] **Step 3: Render cockpit route**

Before feature tab fallback:

```tsx
activeView === 'runs' ? (
  <RunCockpitPage
    selectedRunID={selectedRunID}
    selectedRunDetail={data.selectedRunDetail}
    openPilotState={data.openPilotState}
    runActionID={data.runActionID}
    onRetryRun={data.retryRun}
    onCancelRun={data.cancelRun}
    onResumeOpenPilotState={data.resumeOpenPilotState}
    onStepAction={data.stepAction}
    onBackToOverview={() => data.setSelectedRunID(selectedRunID)}
    onRefresh={data.refresh}
  />
) : ...
```

Make `onBackToOverview` navigate to `#/overview/runs/<selectedRunID>`; add a helper in navigation if needed.

## Task 8: Tests

**Files:**
- Modify: `web/src/App.test.tsx`

- [x] **Step 1: Extend mock data**

Add mock responses for:

- `/api/runs` returns one Open Pilot run `run-1`.
- `/api/runs/run-1` returns `run`, two `steps`, two `events`, and two `artifacts`.
- `/api/open-pilot/state?repo=OWNER%2FREPO&issue=123` returns lifecycle state with gate status and next action.

- [x] **Step 2: Add cockpit deep-link test**

Assert `#/runs/run-1` renders:

- `Run cockpit`
- `Open Pilot lifecycle`
- `Step timeline`
- `Evidence`
- `Correlation + traces`
- `Resume Open Pilot`

- [x] **Step 3: Add step filter test**

Select status `failed`; assert failed step remains and succeeded step is hidden.

- [x] **Step 4: Add evidence search test**

Type an artifact name; assert matching artifact remains and non-matching event/artifact is hidden.

- [x] **Step 5: Add malformed hash test**

Set `window.location.hash = '#/runs/%E0%A4%A'`, render, and assert no crash plus empty/loading state.

## Task 9: Verification and Commit

**Files:**
- All above.

- [x] **Step 1: Web checks**

Run:

```bash
pnpm --dir web run format
pnpm --dir web run lint
pnpm --dir web run test
pnpm --dir web run build
```

Expected: all pass.

- [x] **Step 2: Full repo verification**

Run:

```bash
make verify
```

Expected: passes. Clean generated `backend/app` and `backend/switchyard` before commit if `make verify` produces them.

- [x] **Step 3: Commit**

```bash
git add web/src/hooks/useHashNavigation.ts web/src/hooks/useDashboardData.ts web/src/types.ts web/src/components/runCockpit.tsx web/src/components/RunCockpitPage.tsx web/src/components/OverviewDashboard.tsx web/src/App.tsx web/src/App.test.tsx docs/superpowers/plans/2026-06-18-workflow-run-open-pilot-cockpit-sprint.md
git commit -m "feat: add open pilot run cockpit"
```

## Self-Review Checklist

- Spec coverage: deep links, run detail, Open Pilot lifecycle, gates, step filters, evidence search, correlation IDs, and action clarity are each mapped to tasks.
- Placeholder scan: no `TBD`, vague “add tests”, or “similar to” implementation steps remain.
- Type consistency: route state uses `selectedRunID`; request cockpit continues to use `selectedRequestID`; backend API contracts are unchanged.
