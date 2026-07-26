# Docs Migration Closeout Implementation Plan

> **Historical plan:** The native-module documentation migration is complete. This file is retained for decision history only.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Close the OCQ/llama-line/Changemaker migration in documentation so docs match the live Switchyard-native runtime.

**Architecture:** Keep historical ADRs intact but clearly mark them as history. Update README, roadmap, architecture, runbooks, and OpenAPI references so operators know which ports, endpoints, and compatibility semantics are live today.

**Tech Stack:** Markdown docs, OpenAPI YAML, grep-based consistency checks.

---

## Approved Scope

- Mark old imported repos as retired and removed.
- Document Switchyard-native ownership of model broker, OpenCode executor, workflow policy, and compatibility ingress.
- Add Subcorp compatibility notes: `/api/chat` returns Ollama JSON; `/v1/chat/completions` is OpenAI/SSE-compatible.
- Keep ADR history; do not rewrite decisions as if they never happened.
- No code changes except docs/tests if docs links are checked.

## File Structure Target

- Modify: `README.md` — live runtime summary and runbook links.
- Modify: `docs/architecture/one-repo-control-plane.md` — native module diagram/wording.
- Modify: `docs/roadmap.md` — move absorption work to completed; list next hardening/UI work.
- Modify: `docs/service-consolidation-plan.md` — add closeout status.
- Modify: `docs/ui-feature-migration.md` — reflect extracted UI shell and remaining tabs.
- Modify: `docs/openapi.yaml` — document alias paths still omitted if applicable.
- Create: `docs/reference/client-compatibility.md` — Switchyard as llama-line/Ollama/OpenAI-compatible client guide.

## Task 1: Add Client Compatibility Reference

**Files:**
- Create: `docs/reference/client-compatibility.md`

- [ ] **Step 1: Create client guide**

```markdown
# Client Compatibility Reference

Switchyard owns the native model-broker runtime and exposes llama-line/Ollama/OpenAI-compatible ingress for existing clients.

## Base URLs

- Local host: `http://127.0.0.1:11434`
- LAN clients: `http://10.0.0.50:11434`
- Compose-internal API: `http://api:8080`

## Ollama-compatible endpoints

| Endpoint | Response mode | Notes |
| --- | --- | --- |
| `GET /api/tags` | JSON | Proxies real upstream Ollama tags. |
| `POST /api/generate` | JSON | Non-stream response by default. |
| `POST /api/chat` | JSON | Returns plain Ollama JSON with `message.content`; clients expecting SSE must support JSON too. |
| `POST /api/embed` | JSON | Embedding passthrough. |

## OpenAI-compatible endpoints

| Endpoint | Response mode | Notes |
| --- | --- | --- |
| `POST /v1/chat/completions` | JSON or SSE when `stream:true` | Emits OpenAI-compatible chunks plus Switchyard/OCQ correlation headers. |
| `POST /v1/completions` | JSON | Completion compatibility. |
| `POST /v1/embeddings` | JSON | Embedding compatibility. |
| `GET /v1/models` | JSON | Model list compatibility. |

## Correlation headers

- `X-Request-ID`
- `X-Llama-Line-Request-ID`
- `X-OCQ-Request-ID`
- `X-OCQ-Streaming-Mode`

## Subcorp note

Subcorp should point `OLLAMA_BASE_URL` and embedding URLs at `http://10.0.0.50:11434`. Its `/api/chat` parser must accept plain JSON and not require SSE.
```

- [ ] **Step 2: Link from `docs/reference/README.md`**

Add:

```markdown
- [Client Compatibility](client-compatibility.md) — llama-line/Ollama/OpenAI compatibility behavior for downstream clients.
```

## Task 2: Update Architecture and Roadmap Docs

**Files:**
- Modify: `docs/architecture/one-repo-control-plane.md`
- Modify: `docs/roadmap.md`
- Modify: `docs/service-consolidation-plan.md`

- [ ] **Step 1: Add closeout box to architecture doc**

Add near the top:

```markdown
> **Migration status:** OCQ, llama-line, and Changemaker directories have been removed. Switchyard now owns the native model broker, OpenCode executor, compatibility ingress, durable request ledger, and first-party workflow policy.
```

- [ ] **Step 2: Update roadmap completed/current sections**

In `docs/roadmap.md`, move or add bullets under completed/current status:

```markdown
- Completed: native llama-line/Ollama/OpenAI-compatible ingress on `LLAMA_LINE_PORT`.
- Completed: durable model-broker request ledger, history/search/log/config compatibility endpoints, admin queue/inflight aliases, OCQ session/request/settings compatibility views.
- Completed: Phoenix OTLP tracing for model-broker request spans.
- Next: production runbooks, observability UI, model-broker reliability policy, and feature-tab UI redesign.
```

- [ ] **Step 3: Add service consolidation closeout**

At the top of `docs/service-consolidation-plan.md`, add:

```markdown
> **Closeout:** The imported service directories are retired. This document remains as historical migration context; live behavior is implemented in `backend/internal/controlplane` and documented in `README.md`, `docs/reference/client-compatibility.md`, and `docs/runbooks/native-model-broker-cutover.md`.
```

## Task 3: Update UI and OpenAPI Docs

**Files:**
- Modify: `docs/ui-feature-migration.md`
- Modify: `docs/openapi.yaml`

- [ ] **Step 1: Update UI migration doc**

Add a status section:

```markdown
## Current UI status

- `web/src/App.tsx` is now a shell, with API/data/navigation/presentation split into focused modules.
- Overview has been redesigned for command strip, active work, selected run, and evidence scanning.
- Feature tabs remain extracted but not deeply redesigned; Queues/Requests is the recommended next sprint.
```

- [ ] **Step 2: Fill OpenAPI alias gaps**

In `docs/openapi.yaml`, ensure these runtime paths are represented or explicitly documented as aliases:

```yaml
  /admin/logs:
  /admin/audit:
  /admin/search:
  /admin/stats/summary:
  /ui/:
  /ui/api/status:
  /ocq/requests/{id}/abort:
  /ocq/audit:
  /ocq/logs:
```

If the file already documents a canonical equivalent, add `description: Alias of ...` rather than duplicating schemas.

## Task 4: Run Consistency Checks

**Files:**
- Verify-only.

- [ ] **Step 1: Search stale live-directory claims**

Run:

```bash
rg -n "services/(llama-line|ocq)|workflows/changemaker|transitional scaffolding|compat profile" README.md docs AGENTS.md
```

Expected: only historical ADR/migration references remain, each clearly marked historical/retired.

- [ ] **Step 2: Search stale port claims**

Run:

```bash
rg -n "21434|LLAMA_LINE_PORT|11434|11435" README.md docs .env.example docker-compose.yml
```

Expected: `LLAMA_LINE_PORT` defaults to `11434`; `11435` is only the real upstream Ollama example.

- [ ] **Step 3: Verify docs and commit**

Run:

```bash
make verify
git add README.md docs/architecture/one-repo-control-plane.md docs/roadmap.md docs/service-consolidation-plan.md docs/ui-feature-migration.md docs/openapi.yaml docs/reference/README.md docs/reference/client-compatibility.md
git commit -m "docs: close native runtime migration"
```

Expected: verification passes and docs mention live runtime accurately.
