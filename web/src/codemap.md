# web/src/

## Responsibility
Contains the dashboard application shell, API client, shared domain types, styles, pages, and state hooks.

## Design
- `types.ts` mirrors backend JSON contracts and supplies empty/default states.
- `openPilotInbox.ts` holds the inbox contract types, request/dispatch helpers, and `#/inbox/<params>` deep-link parsing.
- `workflowDrafts.ts` holds the workflow draft form model and its conversion to and from `WorkflowDefinition`, executor settings and published outputs, the `{{steps.<id>.outputs.<name>}}` binding options and reference checks, catalog filtering, `#/workflows/...` routes, and the definition, profile, control-access, and guarded draft-save API helpers (saves send `expected_version`). `workflowVersions.ts` holds version history loading, active/draft/earlier roles, the version comparison, client-side activation checks, action consequences, and the activate/rollback API helpers.
- `profileVersions.ts` is the agent-profile and run-model-policy client: profile draft form model and local checks, version/rollback helpers, draft/activate/rollback mutations mapped to validation/forbidden/conflict/error, the model-policy preview, override dispatch, and model-source labels.
- `promptAuthoring.ts` is the prompt authoring client: inventory and preview types, `GET /api/prompts` and `POST /api/prompts/preview` helpers, client-side variable listing for sample fields, template replacement in a definition, draft saves through `saveWorkflowDraft`, and run prompt-binding labels.
- `navigationGuard.ts` lets a page with unsaved edits veto shell navigation; `useHashNavigation` consults it for sidebar and browser view changes.
- `api.ts` centralizes fetch, normalization, formatting, and mutations.
- `App.tsx` selects pages while hooks own URL and asynchronous state.
- `correlation.ts` defines typed correlation destinations (run, step, request, session, executor request, Gitea issue/PR/commit/CI, trace, span), the external-link allowlist derived from `/api/operator-safety` `correlation_targets`, run evidence deep links (`#/runs/<run>/<steps|artifacts|events>/<id>`), and the run failure summary. `correlation.test.ts` covers allowlist rejection and destination mapping.
- `test-utils.tsx` provides strict typed fetch fixtures and DOM helpers for the dashboard suite; `App.test.tsx` navigates operator mutations, selection/error states, and hash routes; `components/ArtifactPreview.test.tsx` covers inline artifact decoding, sanitized rendering, and fallbacks.

## Flow
`main.tsx` → `App.tsx` → navigation/data hooks → `api.ts` → backend; normalized data flows into page components and user actions return through hook callbacks.

## Integration
- Depends on React and browser Fetch APIs.
- Contract changes in `backend/internal/controlplane` usually require corresponding updates in `types.ts` and `api.ts`.
