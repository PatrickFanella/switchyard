# UI Queues + Requests Sprint Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Redesign the Queues view into a request-centric model-broker cockpit with filters, deep links, request detail, and operational actions.

**Architecture:** Keep the current hash-router approach and extracted UI modules. Add backend history/detail helpers if needed, then replace the generic Queues feature tab with a dedicated `QueuesPage` that reads durable broker history and supports selected request deep links.

**Tech Stack:** React 19, TypeScript, existing HUD components, existing model-broker history endpoints, Vitest/jsdom.

---

## Approved Scope

- Deeply redesign only Queues/Requests.
- Add hash forms: `#/queues`, `#/queues/requests/<request-id>`.
- Add filters by status, model text, route/path, and error-only.
- Show request detail with correlation IDs, timings, status, model, path, client, error, and sanitized response metadata.
- Keep prompts/responses hidden unless already exposed by existing compatibility endpoint and explicitly safe.
- Leave Sessions/Logs/Prompts/Config/Agents/Skills tabs as extracted existing views.

## File Structure Target

- Modify: `web/src/hooks/useHashNavigation.ts` — queue request deep-link parsing.
- Modify: `web/src/types.ts` — broker request/history types.
- Modify: `web/src/api.ts` — history/detail helpers.
- Create: `web/src/components/QueuesPage.tsx` — redesigned UI.
- Modify: `web/src/components/FeaturePage.tsx` — remove or bypass old queues branch.
- Modify: `web/src/App.tsx` — render QueuesPage for `queues`.
- Modify: `web/src/App.test.tsx` — deep-link/filter tests.

## Task 1: Extend Hash Navigation for Queue Request Detail

**Files:**
- Modify: `web/src/hooks/useHashNavigation.ts`
- Modify: `web/src/types.ts`
- Test: `web/src/App.test.tsx`

- [ ] **Step 1: Extend navigation state**

Update the navigation hook return type to include:

```ts
selectedRequestID: string;
setSelectedRequestID: (requestID: string) => void;
```

Parse hashes matching:

```ts
#/queues/requests/<request-id>
```

Use the same safe decoding helper as run deep links; malformed request IDs must fall back to `#/queues` with an empty selected request.

- [ ] **Step 2: Update hash writing**

When `setSelectedRequestID('req-123')` is called, write:

```ts
window.location.hash = `#/queues/requests/${encodeURIComponent(requestID)}`;
```

When the queues tab is clicked, clear `selectedRequestID` and write `#/queues`.

- [ ] **Step 3: Add tests**

Add tests that:

```tsx
window.location.hash = '#/queues/requests/req-123';
render(<App />);
expect(window.location.hash).toBe('#/queues/requests/req-123');
```

and malformed `%E0%A4%A` does not throw.

## Task 2: Add Broker History API Helpers

**Files:**
- Modify: `web/src/types.ts`
- Modify: `web/src/api.ts`

- [ ] **Step 1: Add types**

Add:

```ts
export type ModelBrokerRequestHistoryItem = {
  id: string;
  status: string;
  model: string;
  path: string;
  client: string;
  request_body?: string;
  response_body?: string;
  error?: string;
  correlation_ids?: Record<string, string>;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
  cancel_requested_at?: string;
};

export type ModelBrokerHistoryResponse = {
  requests?: ModelBrokerRequestHistoryItem[];
  history?: ModelBrokerRequestHistoryItem[];
};
```

- [ ] **Step 2: Add helpers**

In `api.ts`, add:

```ts
export async function loadModelBrokerHistory(query = '') {
  const path = query.trim() ? `/api/history?q=${encodeURIComponent(query.trim())}` : '/api/history';
  const response = await fetchJSON<ModelBrokerHistoryResponse | ModelBrokerRequestHistoryItem[]>(path);
  if (Array.isArray(response)) return response;
  return arrayOrEmpty(response.requests ?? response.history);
}

export async function loadModelBrokerRequest(requestID: string) {
  return fetchJSON<ModelBrokerRequestHistoryItem>(`/api/history/${encodeURIComponent(requestID)}`);
}

export async function cancelModelBrokerRequest(requestID: string) {
  return fetchJSON<{ canceled?: boolean }>(`/admin/queue/${encodeURIComponent(requestID)}`, { method: 'DELETE' });
}
```

- [ ] **Step 3: Run type check**

Run: `pnpm --dir web run build`

Expected: no type errors.

## Task 3: Build `QueuesPage`

**Files:**
- Create: `web/src/components/QueuesPage.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/components/FeaturePage.tsx`

- [ ] **Step 1: Create page component**

Create `web/src/components/QueuesPage.tsx` with state for:

```ts
const [requests, setRequests] = useState<ModelBrokerRequestHistoryItem[]>([]);
const [query, setQuery] = useState('');
const [status, setStatus] = useState('all');
const [errorOnly, setErrorOnly] = useState(false);
const [selected, setSelected] = useState<ModelBrokerRequestHistoryItem | null>(null);
```

Use `loadModelBrokerHistory(query)` on mount and when refresh is clicked. Filter client-side by `status`, `model`, `path`, and `errorOnly`.

- [ ] **Step 2: Layout the cockpit**

Use existing HUD primitives and create these sections:

- Command strip: refresh, search input, status select, error-only toggle.
- Request lanes: counts for queued/running/completed/failed/canceled.
- Request list: id/model/path/status/duration.
- Request detail: selected request fields and correlation IDs.

Use copy:

```tsx
<HudSection kicker="ModelBroker" title="Requests cockpit" note="Durable llama-line/Ollama/OpenAI compatibility requests, filters, and correlation IDs." />
```

- [ ] **Step 3: Wire selection and cancel**

When a request row is clicked:

```ts
setSelectedRequestID(request.id);
setSelected(request);
```

Show a `Cancel` button only for `queued`, `running`, or `cancel_requested`. On click, call `cancelModelBrokerRequest(request.id)`, refresh history, and keep the selected detail open.

- [ ] **Step 4: Wire App**

In `App.tsx`, render `QueuesPage` when `activeView === 'queues'` and pass `selectedRequestID` and `setSelectedRequestID` from the navigation hook.

Keep `FeaturePage` for other views.

## Task 4: Add UI Tests

**Files:**
- Modify: `web/src/App.test.tsx`

- [ ] **Step 1: Mock history endpoint**

Add mock response for `/api/history`:

```json
{
  "requests": [
    {"id":"req-ok","status":"completed","model":"qwen3.5:latest","path":"/api/generate","client":"test","created_at":"2026-06-17T00:00:00Z","updated_at":"2026-06-17T00:00:01Z","started_at":"2026-06-17T00:00:00Z","finished_at":"2026-06-17T00:00:01Z","correlation_ids":{"trace_id":"abc"}},
    {"id":"req-fail","status":"failed","model":"qwen3.5:latest","path":"/api/chat","client":"test","error":"upstream unavailable","created_at":"2026-06-17T00:00:00Z","updated_at":"2026-06-17T00:00:01Z"}
  ]
}
```

- [ ] **Step 2: Test queues render**

Assert:

```tsx
expect(await screen.findByText('Requests cockpit')).toBeInTheDocument();
expect(screen.getByText('req-ok')).toBeInTheDocument();
expect(screen.getByText('req-fail')).toBeInTheDocument();
```

- [ ] **Step 3: Test filtering**

Click error-only and assert `req-fail` remains while `req-ok` is hidden.

- [ ] **Step 4: Test deep link**

Set `window.location.hash = '#/queues/requests/req-ok'`, render, and assert the detail panel shows `trace_id`.

## Task 5: Verify and Commit

**Files:**
- All above.

- [ ] **Step 1: Run web checks**

```bash
pnpm --dir web run format
pnpm --dir web run lint
pnpm --dir web run test
pnpm --dir web run build
```

- [ ] **Step 2: Run full verification**

Run: `make verify`

- [ ] **Step 3: Commit**

```bash
git add web/src/hooks/useHashNavigation.ts web/src/types.ts web/src/api.ts web/src/components/QueuesPage.tsx web/src/components/FeaturePage.tsx web/src/App.tsx web/src/App.test.tsx
git commit -m "feat: redesign broker requests cockpit"
```
