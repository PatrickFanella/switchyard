# web/src/hooks/

## Responsibility
Owns dashboard data lifecycle and URL-addressable navigation state.

## Design
- `useDashboardData` centralizes loading/error/action state and refreshes related run/Open Pilot detail after mutations.
- `useHashNavigation` parses and formats stable deep links without an external router.

## Flow
1. Parse the current hash into active view and selected IDs.
2. Load the dashboard snapshot, then selected run and optional Open Pilot state.
3. Action callbacks call `api.ts`, update selection, and reload the affected snapshot/detail.
4. State changes write canonical hashes for browser navigation.

## Integration
- Used by `App.tsx` and backed by `api.ts` plus `types.ts`.
- Synchronizes with browser `hashchange` events.
