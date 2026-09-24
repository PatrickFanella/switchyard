# web/src/components/

## Responsibility
Renders operator-facing pages and reusable HUD controls for runtime overview, run details, queues, observability, and adapter-backed feature inventories.

## Design
- `hud.tsx` provides shared visual/status primitives.
- `OverviewDashboard` composes runtime summaries and dispatch controls.
- `RunCockpitPage`/`runCockpit.tsx` present steps, events, artifacts, gates, and run actions.
- `LiveRefreshIndicator` shows live-refresh state from `useLiveRefresh`: cadence or pause reason, last successful update, reconnect/backoff attempt, stale and error markers.
- `ArtifactPreview.tsx` renders bounded inline text/JSON/Markdown artifacts with type, size, digest, and retention metadata plus copy/download; `artifactPreview.ts` decodes data URIs without fetching and `artifactMarkdown.ts` is the minimal HTML-free Markdown parser.
- `QueuesPage` and `ObservabilityPage` independently query model-broker history/health.
- `FeaturePage` handles sessions, logs, prompts, agents, skills, and configuration views.

## Flow
`App` selects a page by hash route, passes normalized state and action callbacks, and components render status or invoke callbacks/API helpers in response to operator input.

## Integration
- Consumes `../types`, `../api`, and state from `../hooks`.
- Styling contracts are defined in `../styles.css`.
