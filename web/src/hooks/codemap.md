# web/src/hooks/

## Responsibility
Owns dashboard data lifecycle and URL-addressable navigation state.

## Design
- `useDashboardData` centralizes loading/error/action state and refreshes related run/Open Pilot detail after mutations.
- `useHashNavigation` parses and formats stable deep links without an external router.
- `useLiveRefresh` is a bounded poller for the selected run and activity feed, with an explicit `refreshNow` that shares the same request guard: one self-rescheduling timer (no overlapping requests), pauses when the run is terminal, the tab is hidden, or nothing is selected, exponential backoff capped at 60s on failure, and sequence tickets so stale out-of-order responses are discarded. There is no run event stream on the backend; the only SSE route is the model-compatible ingress.

## Flow
1. Parse the current hash into active view and selected IDs.
2. Load the dashboard snapshot, then selected run and optional Open Pilot state.
3. Action callbacks call `api.ts`, update selection, and reload the affected snapshot/detail.
4. State changes write canonical hashes for browser navigation.

## Integration
- Used by `App.tsx` and backed by `api.ts` plus `types.ts`.
- Synchronizes with browser `hashchange` events.
