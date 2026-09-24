# web/

## Responsibility
Implements the React operator dashboard for monitoring, dispatching, and controlling Switchyard.

## Design
- Vite + React 19 + TypeScript, with a single-page hash router and typed REST client.
- Components use a reusable HUD vocabulary and responsive shell; stateful API orchestration is isolated in hooks.
- `pnpm-workspace.yaml` owns dependency overrides and the explicit build-script allowlist while the frozen lockfile remains local to this package.
- Production output is served by templated nginx; it allows only the observed original Tailscale proxy `/32`, overwrites forwarding headers, clears client-supplied `X-Forwarded-User`, and proxies `/api/*` with Caddy's `Remote-User` identity plus the configured hop token. Host nft and exact UFW forward rules enforce the same peer before and after DNAT.
- The Vitest suite uses strict typed dashboard fixtures and covers operator, hash-navigation, fake-timer live-refresh, artifact preview cases (decoding, sanitized Markdown, copy/download fallbacks, keyboard access), Devlog review states (preview, digest-checked export, approval, delivery, stale/failed/disabled delivery), runtime configuration states (secret redaction, drift filter, revision-checked pause controls, stale/denied/unauthenticated errors), the workflow catalog/draft editor (keyboard use, create/edit/save/reload journey, validation errors, stale-version conflicts, read-only access, unsaved-changes guard), workflow versions (comparison, activation confirmation, failed activation, rollback, stale conflict, read-only), profile management (inspect, stale-edit conflict, validation, read-only, activate and rollback), and per-run model policy (override dispatch, rejection, resolved run policy) across App, hash navigation, `useLiveRefresh`, `ArtifactPreview`, `DevlogReviewPanel`, `ConfigPage`, `WorkflowCatalogPage`, `WorkflowVersionsPanel`, `workflowDrafts`, `workflowVersions`, `ProfilesPage`, `RunModelPolicy`, and `profileVersions`.

## Flow
1. `src/main.tsx` mounts `App` and global styles.
2. `App` derives navigation from the URL and dashboard state from `useDashboardData`.
3. The API client loads workflows, definitions, triggers, runs, adapters, scheduler, and workers concurrently.
4. User actions dispatch runs, retry/cancel work, resume Open Pilot, or inspect broker history, then refresh state.

## Integration
- Calls backend `/api/*` and selected model-broker admin compatibility routes.
- Built/tested by root Make targets and deployed as the `web` Compose service.
