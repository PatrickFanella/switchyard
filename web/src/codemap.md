# web/src/

## Responsibility
Contains the dashboard application shell, API client, shared domain types, styles, pages, and state hooks.

## Design
- `types.ts` mirrors backend JSON contracts and supplies empty/default states.
- `api.ts` centralizes fetch, normalization, formatting, and mutations.
- `App.tsx` selects pages while hooks own URL and asynchronous state.
- `test-utils.tsx` provides strict typed fetch fixtures and DOM helpers for the 19-case dashboard suite; `App.test.tsx` navigates operator mutations, selection/error states, and hash routes.

## Flow
`main.tsx` → `App.tsx` → navigation/data hooks → `api.ts` → backend; normalized data flows into page components and user actions return through hook callbacks.

## Integration
- Depends on React and browser Fetch APIs.
- Contract changes in `backend/internal/controlplane` usually require corresponding updates in `types.ts` and `api.ts`.
