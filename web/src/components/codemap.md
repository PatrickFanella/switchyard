# web/src/components/

## Responsibility
Renders operator-facing pages and reusable HUD controls for runtime overview, run details, queues, observability, and adapter-backed feature inventories.

## Design
- `hud.tsx` provides shared visual/status primitives.
- `OverviewDashboard` composes runtime summaries and dispatch controls.
- `RunCockpitPage`/`runCockpit.tsx` present steps, events, artifacts, gates, and run actions.
- `LiveRefreshIndicator` shows live-refresh state from `useLiveRefresh`: cadence or pause reason, last successful update, reconnect/backoff attempt, stale and error markers.
- `ArtifactPreview.tsx` renders bounded inline text/JSON/Markdown artifacts with type, size, digest, and retention metadata plus copy/download; `artifactPreview.ts` decodes data URIs without fetching and `artifactMarkdown.ts` is the minimal HTML-free Markdown parser.
- `DevlogReviewPanel.tsx` renders the Devlog report review in the run cockpit for Devlog runs: source window, expanded Markdown preview, digest-checked exact-bytes export, approve/reject bound to the report SHA-256, delivery and retry, and receipts. `devlogReview.ts` holds its API helpers and types.
- `CorrelationLinks.tsx` renders the cockpit failure summary, validated correlation links with copyable fallback IDs and reasons, evidence anchors and focus for run evidence deep links, and the unknown or retained-out run notice. `CorrelationLinks.test.tsx` drives a failed CI run through deep links, browser back/forward, retained-out records, and unconfigured destinations.
- `OpenPilotInboxPage` renders the read-only `GET /api/open-pilot/inbox` intake queue: filters, cursor pagination, upstream availability versus empty queue, per-issue reason codes, existing claims, and links to the Gitea issue/PR and run cockpit. Filters and selection round-trip through `#/inbox/<params>`; dispatch and resume call the existing Open Pilot run and lifecycle routes.
- `WorkflowCatalogPage` renders the `#/workflows` catalog (search, status filter, ordered step view via `WorkflowDefinitionSteps`) and routes `view`/`edit`/`new`/`duplicate` sub-hashes; `WorkflowDraftEditor` edits a linear draft (metadata, input fields, executor settings, executor-compatible profiles, the step-output binding picker, and `output_shape` v1 rows), shows backend validation messages verbatim, notes which version stays active, handles read-only access, and registers an unsaved-changes navigation guard. It saves drafts only. `WorkflowVersionsPanel` (on the detail screen) lists immutable versions as active/draft/earlier, compares two versions with a metadata and per-step field diff, and offers admin-only activation of the latest version and rollback of earlier versions through a confirmation dialog that states the target, consequences, and activation checks; conflicts (409), rejected activations (400), and 403 are reported without changing state.
- `QueuesPage` and `ObservabilityPage` independently query model-broker history/health.
- `FeaturePage` handles prompts, agents, and skills; it delegates the sessions view to `SessionsPage`, the logs view to `EvidenceSearchPage`, and the configuration view to `ConfigPage`.
- `SessionsPage`/`sessionLinks.ts` render the read-only `GET /api/executor-sessions` view: executor availability, per-session repository, status, presence, and related run, step, and request links, with `#/sessions/<id>` deep links and no abort control (cancellation goes through the owning run).
- `EvidenceSearchPage`/`evidenceSearch.ts` query the bounded `/api/evidence/search` route with idle, loading, no-results, error, deep-link (`#/logs/search/<scope>/<query>[/<cursor>]`), and cursor-pagination states.
- `ConfigPage` loads `/api/runtime-config`, shows effective values, sources, drift, and file-managed edit/restart instructions per setting, and offers real controls only for the runtime-mutable pauses (admin, revision-checked). Secret values are never received.

## Flow
`App` selects a page by hash route, passes normalized state and action callbacks, and components render status or invoke callbacks/API helpers in response to operator input.

## Integration
- Consumes `../types`, `../api`, and state from `../hooks`.
- Styling contracts are defined in `../styles.css`.
