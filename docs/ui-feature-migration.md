# Switchyard UI Feature Migration

> Reconciled against `main` on 2026-09-23 (Gitea issue #65). The "Historical adapter plan" section is the original migration sequence and is kept for context; "Current state" and "Remaining adapter-backed surfaces" describe `main` as observed in `web/src/App.tsx` and `web/src/components/`.

Switchyard is absorbing the operator-facing UI ideas from the reference repos into one native HUD. The goal is not separate service consoles; it is native module surfaces backed by Switchyard APIs.

## Implemented Switchyard homes

| Historical source | Existing UI surface | Switchyard home |
| --- | --- | --- |
| Blacktower control center | tasks, runs, reports, scheduler health, session commands | Overview run center, Agents, Skills |
| llama-line web/admin | queues, inflight requests, upstream health, stats, config, request/log/search | ModelBroker, Logs/Search, Config |
| OCQ UI/admin | overview, requests, logs, sessions, settings | OpenCode Executor, Logs/Search, Config |
| Changemaker web | run control, live logs, posts, prompts, config, service status | Workflow policy, Prompts, Overview run center |

## Current state

- The web app uses the neubrutal HUD token system from `web/tailwind.config.js` and CSS variables emitted into `web/src/styles.css`.
- Navigation in `web/src/App.tsx` is grouped as Operate (Overview, Inbox, Runs, Queues), Inspect (Observability, Sessions, Logs), and Configure (Workflows, Prompts, Agents, Skills, Config).
- `web/src/App.tsx` is a shell; API, data, hash navigation, and presentation are split into `web/src/api.ts`, `web/src/hooks/`, and `web/src/components/`.
- Native surfaces (backed by Switchyard APIs, not `GET /api/adapters`):
  - Overview (`OverviewDashboard.tsx`): workflows, triggers, runs, scheduler, workers, operator summary, and Open Pilot dispatch/resume.
  - Runs (`RunCockpitPage.tsx`): run detail, step timeline, evidence filters, retry/cancel, and Open Pilot lifecycle actions.
  - Workflows (`WorkflowCatalogPage.tsx`, `WorkflowDraftEditor.tsx`): searchable catalog and linear draft editor over `/api/workflow-definitions`; see `docs/reference/workflow-catalog.md`.
  - Queues (`QueuesPage.tsx`): the durable model-broker ledger through `/api/history`, with request deep links.
  - Observability (`ObservabilityPage.tsx`): `/api/observability/model-broker`.
  - Logs/Search (`EvidenceSearchPage.tsx`): `GET /api/evidence/search`, a retention-bounded, cursor-paginated, redacted search over runs, steps, and model requests with `#/logs/search/<scope>/<query>[/<cursor>]` deep links; see `docs/reference/evidence-search.md`.
  - Sessions (`SessionsPage.tsx`): `GET /api/executor-sessions`, a bounded view built from run-step session correlations and executor status, with `#/sessions/<id>` deep links; see `docs/reference/executor-sessions.md`.
  - Agents and Skills (`FeaturePage.tsx`): native profiles from `/api/agent-profiles`, rendered as read-only policy records.
- `GET /api/adapters` still returns typed compatibility contracts for the absorbed historical surfaces, and the remaining views below render from it.

## Remaining adapter-backed surfaces

These views in `web/src/components/FeaturePage.tsx` still read the compatibility payload rather than native module state. Finishing them is tracked under roadmap #60; no issue-level breakdown existed on 2026-09-23.

- Prompts: `adapters.changemaker` run control and posts plus `adapters.prompts` (workflow-policy state).
- Config: `adapters.config` and `adapters.health` (native configuration and health state).

## Historical adapter plan

The original sequence from the migration start. Items 1 and 3 are done as described under "Current state". Item 2 is done for sessions; the Sessions view no longer renders the OCQ settings or request placeholders. Item 4 covers the remaining surfaces above.

1. Redesign Queues/Requests around the durable native model broker ledger, status filters, deep links, and correlation IDs.
2. Replace OpenCode-executor requests/logs/sessions/settings placeholders with native executor state.
3. Replace Blacktower placeholders with native profile/persona and workflow-role state.
4. Replace Changemaker placeholders with native workflow-policy, prompts, artifacts, and run state.
