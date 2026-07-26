# UI Structure + Overview Sprint Implementation Plan

> **Historical completed plan:** The modular shell, hash navigation, and overview redesign were delivered. Retained for implementation history.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Refactor the Switchyard web UI into maintainable modules, add URL-addressable navigation, and redesign the Overview dashboard for faster operator scanning without changing backend/API behavior.

**Architecture:** Keep the existing React/Vite/Tailwind stack and preserve current API calls. Split the current single-file app into typed API/data hooks, shared HUD primitives, feature-tab components, and an Overview dashboard module. Add lightweight hash-based navigation so tabs and selected run detail can be deep-linked without introducing a router dependency.

**Tech Stack:** React 19, TypeScript strict mode, Vite 7, Tailwind 4, Vitest, existing CSS HUD tokens.

---

## Approved Scope

- Preserve existing backend/API contracts and request payloads.
- Preserve the current live operator functions: refresh, trigger run-now, enable/disable triggers, Open Pilot repo/issue inputs, run selection, retry, cancel, and feature tabs.
- Add URL-based navigation/deep links for top-level views and selected run detail.
- Redesign the Overview dashboard for operator scanability: status summary first, active work next, run detail and artifacts/events easier to read.
- Keep Phoenix tracing/backend changes separate from UI sprint commits.

## Current State Facts

- `web/src/App.tsx` contains types, fetch helpers, app state, all feature views, all Overview rendering, and shared visual primitives in one ~1600-line file.
- `web/src/main.tsx` only mounts `<App />`; there is no router.
- `web/src/App.test.tsx` is a smoke test only.
- Styling uses Tailwind classes plus custom HUD utilities in `web/src/styles.css`; keep this design language rather than replacing it.
- `vite.config.ts` proxies `/api` and `/healthz` to the local backend during dev.

## File Structure Target

Create focused files under `web/src/`:

- `types.ts` — exported domain/API response types currently at the top of `App.tsx`.
- `api.ts` — `fetchJSON`, mutation helpers, and endpoint-specific fetch functions.
- `hooks/useDashboardData.ts` — dashboard loading, refresh, selection, run detail loading, and mutation callbacks.
- `hooks/useHashNavigation.ts` — hash parsing/writing for top-level view and selected run.
- `components/hud.tsx` — shared presentational primitives such as panels, labels, status chips, key/value rows, and empty/loading/error states.
- `components/FeaturePage.tsx` — existing Queues/Sessions/Logs/Prompts/Config/Agents/Skills feature tab renderer.
- `components/OverviewDashboard.tsx` — redesigned Overview layout and subcomponents for status, triggers, runs, and run detail.
- `App.tsx` — thin application shell: global status, navigation, data hook wiring, and view selection.
- `App.test.tsx` — update smoke expectations and add navigation/deep-link coverage.

## Task 1: Extract Types and API Helpers

**Files:**
- Create: `web/src/types.ts`
- Create: `web/src/api.ts`
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [x] Move all domain type definitions from the top of `App.tsx` into `types.ts` and export them.
- [x] Move `fetchJSON` and endpoint-specific request helpers into `api.ts`.
- [x] Keep function signatures equivalent to current usage; do not rename payload fields.
- [x] Update `App.tsx` imports from `./types` and `./api`.
- [x] Run: `pnpm --dir web run format`
  - Expected: TypeScript passes with no `implicit any` or missing export errors.
- [x] Run: `pnpm --dir web run test`
  - Expected: existing smoke test passes.

## Task 2: Add Hash Navigation

**Files:**
- Create: `web/src/hooks/useHashNavigation.ts`
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [x] Implement hash format: `#/overview`, `#/queues`, `#/sessions`, `#/logs`, `#/prompts`, `#/config`, `#/agents`, `#/skills`.
- [x] Support selected run deep links as `#/overview/runs/<run-id>`.
- [x] Keep invalid hashes safe: fallback to `overview`.
- [x] On tab click, update `window.location.hash` and active view state.
- [x] When a run is selected, update hash to `#/overview/runs/<run-id>`.
- [x] When a selected run disappears from fetched data, keep the hash stable but show the existing “select a run”/empty state rather than crashing.
- [x] Add test: rendering with `window.location.hash = '#/queues'` activates the Queues view.
- [x] Add test: clicking a tab updates `window.location.hash`.
- [x] Run: `pnpm --dir web run test`
  - Expected: all tests pass.

## Task 3: Extract Shared HUD Primitives and Feature Tabs

**Files:**
- Create: `web/src/components/hud.tsx`
- Create: `web/src/components/FeaturePage.tsx`
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [x] Move shared small components from `App.tsx` into `components/hud.tsx`: status chip, section/panel shells, metadata rows, empty state, error state, and loading state.
- [x] Move existing feature-tab renderer into `components/FeaturePage.tsx` with typed props.
- [x] Keep feature tab copy stable enough that the current smoke test can still find the major sections.
- [x] Ensure shared components accept `className?: string` where current layout needs local spacing.
- [x] Run: `pnpm --dir web run lint`
  - Expected: no unused imports after the extraction.
- [x] Run: `pnpm --dir web run format`
  - Expected: TypeScript passes.

## Task 4: Extract Data Hook

**Files:**
- Create: `web/src/hooks/useDashboardData.ts`
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [x] Move dashboard fetching, health fetching, refresh timer behavior, selected run detail loading, and mutation callbacks into `useDashboardData`.
- [x] Return a plain object with: `dashboard`, `dashboardError`, `health`, `loading`, `refreshing`, `selectedRunID`, `selectedRunDetail`, `setSelectedRunID`, `refresh`, `runTrigger`, `toggleTrigger`, `retryRun`, `cancelRun`, `openPilotRepo`, `openPilotIssue`, `setOpenPilotRepo`, `setOpenPilotIssue`, `openPilotReady`, `openPilotState`.
- [x] Keep current refresh semantics; do not introduce polling frequency changes in this sprint.
- [x] Keep errors visible in the UI but never throw during render.
- [x] Run: `pnpm --dir web run format`
  - Expected: no type errors from moved state.
- [x] Run: `pnpm --dir web run test`
  - Expected: smoke and navigation tests pass.

## Task 5: Redesign Overview Dashboard

**Files:**
- Create: `web/src/components/OverviewDashboard.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/styles.css` only if a reusable HUD utility is needed
- Test: `web/src/App.test.tsx`

- [x] Create `OverviewDashboard` with props from `useDashboardData`; keep it presentational except invoking passed callbacks.
- [x] Reframe the Overview as four scan bands:
  1. **Command strip:** API/live health, scheduler, worker pool, queue/run counts, refresh action.
  2. **Active work:** recent runs and trigger controls, with status chips and clear primary actions.
  3. **Selected run:** timeline, lifecycle state, retry/cancel actions, and summary.
  4. **Evidence:** events and artifacts in compact scrollable panels.
- [x] Improve visual hierarchy using existing HUD tokens: fewer nested borders, stronger headings, consistent card spacing, and more obvious active/failed/succeeded status colors.
- [x] Preserve all current actions and disabled/loading behavior.
- [x] Keep responsive behavior usable at laptop widths: overview should stack below `lg`, and run detail should not require horizontal scrolling.
- [x] Add/adjust smoke assertions for the new Overview labels: “Command strip”, “Active work”, “Selected run”, and “Evidence”.
- [x] Run: `pnpm --dir web run test`
  - Expected: tests pass with new labels.
- [x] Run: `pnpm --dir web run build`
  - Expected: production build succeeds.

## Task 6: Final Verification and Review

**Files:**
- Modify only files touched by earlier tasks.

- [x] Run: `pnpm --dir web run lint`
  - Expected: no lint errors.
- [x] Run: `pnpm --dir web run format`
  - Expected: TypeScript passes.
- [x] Run: `pnpm --dir web run test`
  - Expected: all Vitest tests pass.
- [x] Run: `pnpm --dir web run build`
  - Expected: Vite production build succeeds.
- [x] If backend files are still modified from Phoenix tracing, do not include them in any UI-only commit.
- [x] Produce a summary with changed UI files, behavior preserved, and any follow-up UI debt.

## Acceptance Criteria

- `App.tsx` becomes a shell rather than a 1600-line owner of all UI concerns.
- Top-level tabs are addressable by URL hash, and selected run deep links are supported.
- Overview is visibly easier to scan and retains all existing actions.
- Existing feature tabs still render.
- Web tests/build pass.
- No prompts, responses, tokens, or secrets are exposed in new UI code.

## Out of Scope

- Backend/API changes.
- New data endpoints.
- Replacing Tailwind or introducing a router dependency.
- Full redesign of non-Overview feature tabs.
- Committing Phoenix tracing changes as part of a UI-only commit.

## Self-Review

- Spec coverage: component split is covered by Tasks 1, 3, and 4; URL navigation by Task 2; Overview redesign by Task 5; verification by Task 6.
- Placeholder scan: no task relies on “TBD” implementation details; all file paths and commands are explicit.
- Type consistency: hook return names match the planned `OverviewDashboard` props and existing app responsibilities.
