# Observability Dashboard Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Show model-broker health, recent requests, errors, and trace correlation in the Switchyard operator UI.

**Architecture:** Expose a native observability summary endpoint backed by the durable model-broker ledger and existing status providers, then add a focused Observability/Requests view in the web UI. Keep Phoenix as the trace backend but do not fetch Phoenix directly from the browser in this slice.

**Tech Stack:** Go HTTP handlers and Store methods, existing Postgres/memory store, React 19, TypeScript, Vitest.

---

## Approved Scope

- Add a native `/api/observability/model-broker` endpoint.
- Include request totals by status, recent failures, latency from ledger timestamps, active/queued counts, upstream status, and trace/correlation IDs.
- Add a top-level `#/observability` UI view.
- Do not expose prompt/response bodies in the summary.
- Do not query Phoenix directly from Switchyard UI yet; link/copy trace IDs only.

## File Structure Target

- Create: `backend/internal/controlplane/observability.go` — summary types and handler.
- Modify: `backend/internal/controlplane/http.go` — route registration.
- Modify: `backend/internal/controlplane/http_test.go` — endpoint tests.
- Modify: `web/src/types.ts` — observability response types and new view ID.
- Modify: `web/src/api.ts` — `loadModelBrokerObservability()` helper.
- Create: `web/src/components/ObservabilityPage.tsx` — operator-facing view.
- Modify: `web/src/App.tsx` — view routing.
- Modify: `web/src/App.test.tsx` — view smoke/deep-link tests.

## Task 1: Add Backend Summary Endpoint

**Files:**
- Create: `backend/internal/controlplane/observability.go`
- Modify: `backend/internal/controlplane/http.go`
- Test: `backend/internal/controlplane/http_test.go`

- [ ] **Step 1: Add response types and handler**

Create `backend/internal/controlplane/observability.go`:

```go
package controlplane

import (
    "net/http"
    "sort"
    "time"
)

type modelBrokerObservabilityResponse struct {
    Status         ModelBrokerStatus                 `json:"status"`
    Totals         map[string]int                    `json:"totals"`
    RecentRequests []modelBrokerObservabilityRequest `json:"recent_requests"`
    RecentErrors   []modelBrokerObservabilityRequest `json:"recent_errors"`
    LatencyMS      modelBrokerLatencySummary         `json:"latency_ms"`
    CheckedAt      time.Time                         `json:"checked_at"`
}

type modelBrokerObservabilityRequest struct {
    ID             string            `json:"id"`
    Status         string            `json:"status"`
    Model          string            `json:"model"`
    Path           string            `json:"path"`
    Error          string            `json:"error,omitempty"`
    DurationMS     int64             `json:"duration_ms,omitempty"`
    CorrelationIDs map[string]string `json:"correlation_ids,omitempty"`
    CreatedAt      time.Time         `json:"created_at"`
    UpdatedAt      time.Time         `json:"updated_at"`
}

type modelBrokerLatencySummary struct {
    Count int   `json:"count"`
    Avg   int64 `json:"avg"`
    Max   int64 `json:"max"`
}

func (a *API) modelBrokerObservability(w http.ResponseWriter, r *http.Request) {
    rows, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: 100})
    if err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }
    totals := map[string]int{}
    recent := make([]modelBrokerObservabilityRequest, 0, len(rows))
    errors := []modelBrokerObservabilityRequest{}
    latency := modelBrokerLatencySummary{}
    for _, row := range rows {
        totals[row.Status]++
        item := modelBrokerObservabilityItem(row)
        recent = append(recent, item)
        if row.Error != "" || row.Status == ModelBrokerRequestFailed || row.Status == ModelBrokerRequestCanceled {
            errors = append(errors, item)
        }
        if item.DurationMS > 0 {
            latency.Count++
            latency.Avg += item.DurationMS
            if item.DurationMS > latency.Max {
                latency.Max = item.DurationMS
            }
        }
    }
    if latency.Count > 0 {
        latency.Avg = latency.Avg / int64(latency.Count)
    }
    sort.Slice(recent, func(i, j int) bool { return recent[i].CreatedAt.After(recent[j].CreatedAt) })
    sort.Slice(errors, func(i, j int) bool { return errors[i].CreatedAt.After(errors[j].CreatedAt) })
    if len(recent) > 20 { recent = recent[:20] }
    if len(errors) > 10 { errors = errors[:10] }
    writeJSON(w, http.StatusOK, modelBrokerObservabilityResponse{
        Status: NewEnvModelBrokerStatusProvider().Status(r.Context()),
        Totals: totals,
        RecentRequests: recent,
        RecentErrors: errors,
        LatencyMS: latency,
        CheckedAt: time.Now().UTC(),
    })
}

func modelBrokerObservabilityItem(row ModelBrokerRequest) modelBrokerObservabilityRequest {
    item := modelBrokerObservabilityRequest{ID: row.ID, Status: row.Status, Model: row.Model, Path: row.Path, Error: row.Error, CorrelationIDs: row.CorrelationIDs, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
    if row.StartedAt != nil && row.FinishedAt != nil {
        item.DurationMS = row.FinishedAt.Sub(*row.StartedAt).Milliseconds()
    }
    return item
}
```

- [ ] **Step 2: Register route**

In `backend/internal/controlplane/http.go`, add inside `func (a *API) Handler() http.Handler` near the other operator endpoints:

```go
mux.HandleFunc("GET /api/observability/model-broker", a.modelBrokerObservability)
```

Place it near `/api/model-broker/status` or other operator endpoints.

- [ ] **Step 3: Add backend test**

Add a test in `backend/internal/controlplane/http_test.go` that:

```go
func TestAPIModelBrokerObservabilitySummarizesLedger(t *testing.T) {
    store := NewMemoryStore()
    api := NewAPI(store, NewRunner(store))
    req, _ := store.CreateModelBrokerRequest(CreateModelBrokerRequestInput{ID: "req-1", Model: "qwen3.5:latest", Path: "/api/generate", Client: "test", RequestBody: `{}`})
    _, _ = store.StartModelBrokerRequest(req.ID)
    _, _ = store.FinishModelBrokerRequest(req.ID, FinishModelBrokerRequestInput{Status: ModelBrokerRequestCompleted, ResponseBody: `{}`, CorrelationIDs: map[string]string{"trace_id":"abc"}})

    rr := httptest.NewRecorder()
    api.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/model-broker", nil))

    require.Equal(t, http.StatusOK, rr.Code)
    require.Contains(t, rr.Body.String(), "req-1")
    require.Contains(t, rr.Body.String(), "completed")
    require.NotContains(t, rr.Body.String(), "request_body")
}
```

- [ ] **Step 4: Run backend tests**

Run: `go test ./internal/controlplane`

Expected: tests pass.

## Task 2: Add UI Types and API Helper

**Files:**
- Modify: `web/src/types.ts`
- Modify: `web/src/api.ts`

- [ ] **Step 1: Add types**

Append to `web/src/types.ts` near adapter types:

```ts
export type ModelBrokerObservabilityRequest = {
  id: string;
  status: string;
  model: string;
  path: string;
  error?: string;
  duration_ms?: number;
  correlation_ids?: Record<string, string>;
  created_at: string;
  updated_at: string;
};

export type ModelBrokerObservability = {
  status: {
    status: string;
    configured: boolean;
    backend: string;
    base_url?: string;
    queue_depth: number;
    active_request: boolean;
    upstreams: { name: string; url: string; status: string; queue_depth?: number }[];
    error?: string;
  };
  totals: Record<string, number>;
  recent_requests: ModelBrokerObservabilityRequest[];
  recent_errors: ModelBrokerObservabilityRequest[];
  latency_ms: { count: number; avg: number; max: number };
  checked_at: string;
};
```

Update `ViewID` and `views` to include:

```ts
| 'observability'
```

and:

```ts
{ id: 'observability', label: 'Observability', source: 'Phoenix / ModelBroker' }
```

- [ ] **Step 2: Add helper**

In `web/src/api.ts`, import `ModelBrokerObservability` and add:

```ts
export async function loadModelBrokerObservability() {
  return fetchJSON<ModelBrokerObservability>('/api/observability/model-broker');
}
```

- [ ] **Step 3: Run type check through build**

Run: `pnpm --dir web run build`

Expected: TypeScript passes or fails only because the component is not yet wired; fix imports before continuing.

## Task 3: Add Observability Page

**Files:**
- Create: `web/src/components/ObservabilityPage.tsx`
- Modify: `web/src/App.tsx`
- Test: `web/src/App.test.tsx`

- [ ] **Step 1: Create component**

Create `web/src/components/ObservabilityPage.tsx`:

```tsx
import { useEffect, useState } from 'react';
import { loadModelBrokerObservability, messageFrom } from '../api';
import type { ModelBrokerObservability } from '../types';
import { formatTime, statusClasses, HudEmptyState, HudField, HudPanel, HudPanelStrong, HudSection } from './hud';

export function ObservabilityPage() {
  const [data, setData] = useState<ModelBrokerObservability | null>(null);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let alive = true;
    loadModelBrokerObservability()
      .then((next) => { if (alive) { setData(next); setError(''); } })
      .catch((err) => { if (alive) setError(messageFrom(err)); })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, []);

  return (
    <HudSection kicker="Observability" title="Model broker signal" note="Durable request history, upstream status, and trace correlation for the llama-line-compatible ingress.">
      {loading ? <HudEmptyState>Loading broker observability…</HudEmptyState> : null}
      {error ? <HudPanelStrong className="border-[hsl(var(--danger))] p-4 text-sm text-[hsl(var(--danger))]">{error}</HudPanelStrong> : null}
      {data ? (
        <>
          <div className="grid gap-3 md:grid-cols-4">
            <HudField label="Broker" value={data.status.status} />
            <HudField label="Backend" value={data.status.backend} />
            <HudField label="Avg latency" value={`${data.latency_ms.avg}ms`} />
            <HudField label="Failures" value={data.totals.failed ?? 0} />
          </div>
          <div className="mt-4 grid gap-4 lg:grid-cols-2">
            <HudPanelStrong className="p-4">
              <h3 className="hud-label text-[hsl(var(--signal))]">Recent requests</h3>
              <div className="mt-4 grid gap-3">
                {data.recent_requests.map((request) => (
                  <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                    <div className="flex items-center justify-between gap-3">
                      <span className="text-sm font-semibold text-[hsl(var(--ink))]">{request.model}</span>
                      <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                    </div>
                    <p className="mt-2 break-all text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.id} · {request.path} · {request.duration_ms ?? 0}ms · {formatTime(request.created_at)}</p>
                  </HudPanel>
                ))}
              </div>
            </HudPanelStrong>
            <HudPanelStrong className="p-4">
              <h3 className="hud-label text-[hsl(var(--signal))]">Recent errors</h3>
              {data.recent_errors.length === 0 ? <HudEmptyState>No recent broker errors.</HudEmptyState> : null}
              <div className="mt-4 grid gap-3">
                {data.recent_errors.map((request) => (
                  <HudPanel key={request.id} className="bg-[hsl(var(--panel-soft))] p-3">
                    <span className={`hud-chip px-2.5 py-1 text-[0.62rem] ${statusClasses(request.status)}`}>{request.status}</span>
                    <p className="mt-3 text-sm text-[hsl(var(--muted))]">{request.error || 'No error text recorded'}</p>
                    <p className="mt-2 break-all text-xs uppercase tracking-[0.18em] text-[hsl(var(--faint))]">{request.id}</p>
                  </HudPanel>
                ))}
              </div>
            </HudPanelStrong>
          </div>
        </>
      ) : null}
    </HudSection>
  );
}
```

- [ ] **Step 2: Wire the view in App**

In `web/src/App.tsx`, import `ObservabilityPage` and render it when `activeView === 'observability'`.

- [ ] **Step 3: Add tests**

In `web/src/App.test.tsx`, add one test that sets `window.location.hash = '#/observability'`, mocks `/api/observability/model-broker`, renders `<App />`, and expects text `Model broker signal`.

- [ ] **Step 4: Verify web**

Run:

```bash
pnpm --dir web run format
pnpm --dir web run lint
pnpm --dir web run test
pnpm --dir web run build
```

Expected: all pass.

## Task 4: Final Verification

**Files:**
- Verify-only.

- [ ] **Step 1: Run full repo verification**

Run: `make verify`

Expected: passes.

- [ ] **Step 2: Commit**

```bash
git add backend/internal/controlplane/observability.go backend/internal/controlplane/http.go backend/internal/controlplane/http_test.go web/src/types.ts web/src/api.ts web/src/components/ObservabilityPage.tsx web/src/App.tsx web/src/App.test.tsx
git commit -m "feat: surface model broker observability"
```
