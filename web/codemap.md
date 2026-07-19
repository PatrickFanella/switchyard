# web/

## Responsibility
Implements the React operator dashboard for monitoring, dispatching, and controlling Switchyard.

## Design
- Vite + React 19 + TypeScript, with a single-page hash router and typed REST client.
- Components use a reusable HUD vocabulary and responsive shell; stateful API orchestration is isolated in hooks.
- `pnpm-workspace.yaml` owns dependency overrides and the explicit build-script allowlist while the frozen lockfile remains local to this package.
- Production output is served by templated nginx; it source-allows the configured trusted proxy CIDR, clears client-supplied `X-Forwarded-User`, and proxies `/api/*` to the control plane with Caddy's `Remote-User` identity.
- The Vitest suite uses strict typed dashboard fixtures and covers 19 operator and hash-navigation cases.

## Flow
1. `src/main.tsx` mounts `App` and global styles.
2. `App` derives navigation from the URL and dashboard state from `useDashboardData`.
3. The API client loads workflows, definitions, triggers, runs, adapters, scheduler, and workers concurrently.
4. User actions dispatch runs, retry/cancel work, resume Open Pilot, or inspect broker history, then refresh state.

## Integration
- Calls backend `/api/*` and selected model-broker admin compatibility routes.
- Built/tested by root Make targets and deployed as the `web` Compose service.
