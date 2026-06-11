# Phoenix Observability Implementation Plan

> **For agentic workers:** Execute task-by-task. Recommended path: dispatch a fresh subagent per task, review each result, then continue. Steps use checkbox syntax for tracking.

**Goal:** Emit OpenTelemetry/OpenInference traces from llama-line to Arize Phoenix while extending native Postgres stats with token totals and feedback hooks.

**Architecture:** Hybrid. llama-line keeps native Postgres persistence/search/stats and optionally emits Phoenix OTLP traces. Each inference request becomes an OpenInference LLM span; queue/upstream/tool activity becomes child spans. Ollama token counts are parsed once and reused for audit, persistence, stats, and spans. Phoenix is planned for **almaz** hosting.

**Tech Stack:** Go otel-go SDK, OTLP HTTP exporter, OpenInference attributes, Arize Phoenix, Postgres migration 004, React/TypeScript UI.

---

## Confirmed requirements

- Platform: Arize Phoenix.
- Deployment preference: almaz, unless live read-only verification later shows a blocker.
- Architecture: emit to Phoenix and keep native stats.
- Features: trace visibility, LLM-as-judge/eval readiness, richer stats, human feedback/annotations.
- Phoenix OTLP HTTP endpoint: `http://<phoenix-host>:6006/v1/traces`.
- Phoenix UI: `http://<phoenix-host>:6006/`.

---

## Current codebase facts

- `internal/config/config.go` has `DatabaseConfig` and `EmbeddingConfig`; no Phoenix config.
- `cmd/app/main.go` wires optional store/embedder/metrics/audit with `Set*()` methods.
- `internal/server/server.go` has `store`, `embedder`, `logBroadcaster`; no tracer field.
- `internal/server/handler.go` has `writeAuditLog()`, `auditAttrs()`, and `persistRequestEvent()`; token counts are not extracted.
- Ollama non-streaming response JSON can include `prompt_eval_count` and `eval_count`.
- `broker.request_event` has no token count columns.
- `StatsSummary`, `HistoryEntry`, and UI stats/history pages have no token fields.
- No `docker/` directory exists yet.

---

## Task 1: Phoenix config and OTEL deps

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `config.example.yaml`
- Modify: `config.yaml`
- Modify: `go.mod`, `go.sum`

- [ ] Write failing config test for `phoenix.enabled` and `phoenix.otlp_endpoint`.
- [ ] Add `PhoenixConfig`:

```go
type PhoenixConfig struct {
    Enabled      bool   `yaml:"enabled"`
    OTLPEndpoint string `yaml:"otlp_endpoint"`
}
```

- [ ] Add `Phoenix PhoenixConfig` to `Config`.
- [ ] Validate: if `phoenix.enabled` then `phoenix.otlp_endpoint` must not be empty.
- [ ] Add disabled-by-default YAML examples:

```yaml
phoenix:
  enabled: false
  otlp_endpoint: "http://localhost:6006/v1/traces"
```

- [ ] Add deps:

```bash
go get go.opentelemetry.io/otel@latest
go get go.opentelemetry.io/otel/trace@latest
go get go.opentelemetry.io/otel/sdk/trace@latest
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@latest
go get go.opentelemetry.io/otel/attribute@latest
go mod tidy
```

- [ ] Verify: `rtk go test ./internal/config`.

---

## Task 2: Tracer lifecycle

**Files:**
- Create: `internal/tracer/tracer.go`
- Create: `internal/tracer/tracer_test.go`
- Modify: `internal/server/server.go`
- Modify: `cmd/app/main.go`

- [ ] Create `internal/tracer.New(ctx, config.PhoenixConfig) (trace.Tracer, func(), error)`.
- [ ] Disabled config returns nil tracer and safe no-op shutdown.
- [ ] Enabled config creates OTLP HTTP exporter using `OTLPEndpoint`, service name `llama-line`, and batch span processor.
- [ ] Add `tracer trace.Tracer` field and `SetTracer(trace.Tracer)` to `Server`.
- [ ] Wire in `cmd/app/main.go` before server starts; defer shutdown.
- [ ] Verify: `rtk go test ./internal/tracer ./internal/server ./cmd/app`.

---

## Task 3: Token count extraction and persistence

**Files:**
- Modify: `internal/server/handler.go`
- Modify: `internal/persistence/models.go`
- Modify: `internal/persistence/store.go`
- Modify: `internal/server/handler_test.go`

- [ ] Add failing handler test: Ollama response with `prompt_eval_count:42` and `eval_count:17` produces audit attrs `prompt_tokens=42`, `completion_tokens=17`.
- [ ] In `auditAttrs()`, parse response JSON:

```go
var tokenSummary struct {
    PromptEvalCount int `json:"prompt_eval_count"`
    EvalCount       int `json:"eval_count"`
}
_ = json.Unmarshal(respBody, &tokenSummary)
```

- [ ] Add attrs `prompt_tokens`, `completion_tokens`, `total_tokens`.
- [ ] Add `PromptTokens`, `CompletionTokens` to `persistence.RequestEvent`.
- [ ] Populate token fields in `persistRequestEvent()` with `auditInt()`.
- [ ] Extend request-event upsert SQL for token columns after migration 004 exists.
- [ ] Verify: `rtk go test ./internal/server ./internal/persistence`.

---

## Task 4: OpenInference LLM spans

**Files:**
- Create: `internal/server/spans.go`
- Create: `internal/server/testspans_test.go`
- Modify: `internal/server/handler.go`
- Modify: `internal/server/handler_test.go`

- [ ] Add in-memory span-recorder test helper.
- [ ] Add failing test: non-streaming chat emits span attrs:
  - `openinference.span.kind=LLM`
  - `llm.model_name`
  - `llm.token_count.prompt`
  - `llm.token_count.completion`
  - `llm.token_count.total`
- [ ] Implement `emitLLMSpan(...)` no-op when tracer nil.
- [ ] Root span name: `llm.inference`.
- [ ] Add OpenInference message attrs from request `messages[]` and response `message` when parseable.
- [ ] Add child spans `llm.queue_wait` and `llm.upstream_call` with timestamps based on total/queue/upstream durations.
- [ ] Call from `writeAuditLog()` after attrs are built.
- [ ] Verify: `rtk go test ./internal/server`.

---

## Task 5: Tool call spans

**Files:**
- Modify: `internal/server/spans.go`
- Modify: `internal/server/handler_test.go`

- [ ] Add failing test: request with tool name and tool error emits a child span with `openinference.span.kind=TOOL`.
- [ ] In `emitLLMSpan`, emit one `tool.call` child span per `request_tool_names` entry.
- [ ] Tool span attrs:
  - `openinference.span.kind=TOOL`
  - `llm.tool.name`
  - `output.value` from matching tool error when present
- [ ] Verify: `rtk go test ./internal/server`.

---

## Task 6: Native token stats migration

**Files:**
- Create: `internal/persistence/migrations/004_token_counts.sql`
- Modify: `internal/persistence/stats.go`
- Modify: `internal/persistence/stats_test.go`
- Modify: `internal/persistence/store.go`

- [ ] Add migration:

```sql
alter table broker.request_event
    add column if not exists prompt_tokens integer not null default 0,
    add column if not exists completion_tokens integer not null default 0;
```

- [ ] Add `TotalPromptTokens` and `TotalCompletionTokens` to `StatsSummary`.
- [ ] Update `statsSummarySQL()` with `coalesce(sum(prompt_tokens),0)` and `coalesce(sum(completion_tokens),0)`.
- [ ] Add regression test that stats SQL includes both sums.
- [ ] Verify: `rtk go test ./internal/persistence`.

---

## Task 7: Almaz Phoenix compose

**Files:**
- Create: `docker/phoenix.yml`
- Modify: `README.md`

- [ ] Create Docker Compose file for almaz-oriented deployment:

```yaml
services:
  phoenix:
    image: arizephoenix/phoenix:latest
    restart: unless-stopped
    ports:
      - "6006:6006"
      - "4317:4317"
    environment:
      PHOENIX_WORKING_DIR: /mnt/data
      # PHOENIX_SQL_DATABASE_URL: "postgresql://postgres:postgres@postgres:5432/phoenix"
    volumes:
      - phoenix_data:/mnt/data

volumes:
  phoenix_data:
    driver: local
```

- [ ] Document target host: almaz, likely under `/opt/server/monitoring` or `/opt/server/dev` after read-only live verification.
- [ ] Verify compose syntax: `docker compose -f docker/phoenix.yml config --quiet`.

---

## Task 8: Phoenix feedback endpoint

**Files:**
- Create: `internal/server/feedback.go`
- Create: `internal/server/feedback_test.go`
- Modify: `internal/server/admin.go`

- [ ] Add admin-only `POST /admin/feedback`.
- [ ] Request body:

```json
{
  "span_id": "abc123",
  "name": "quality",
  "label": "positive",
  "score": 1.0,
  "explanation": "Good answer"
}
```

- [ ] Validate `span_id` and `name`.
- [ ] Return 503 when Phoenix not enabled.
- [ ] Derive Phoenix base URL by trimming `/v1/traces` from `phoenix.otlp_endpoint`.
- [ ] Proxy to `POST /v1/span_annotations?sync=false` with `annotator_kind=HUMAN`.
- [ ] Verify: `rtk go test ./internal/server -run Feedback`.

---

## Task 9: Frontend Phoenix and tokens

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/api/endpoints.ts`
- Modify: `web/src/pages/History.tsx`
- Modify: `web/src/pages/Stats.tsx`
- Modify: `web/src/components/Layout.tsx`
- Optional create: `web/src/components/FeedbackModal.tsx`

- [ ] Add `prompt_tokens` and `completion_tokens` to `HistoryEntry`.
- [ ] Add `total_prompt_tokens` and `total_completion_tokens` to `StatsSummary`.
- [ ] Add token columns to History page.
- [ ] Add token stat cards to Stats page.
- [ ] Add Phoenix UI link in Layout. Prefer runtime localStorage setting over build-time env unless user chooses otherwise.
- [ ] Add `submitFeedback()` endpoint wrapper for `/admin/feedback`.
- [ ] Verify: `cd web && pnpm build`.

---

## Task 10: Docs and final verify

**Files:**
- Modify: `README.md`
- Modify: `docs/api.md`
- Modify: `docs/superpowers/plans/2026-05-24-phoenix-observability.md` if actual implementation diverges

- [ ] README: add Phoenix Observability section with almaz placement note, compose command, config block, traced fields, and feedback curl.
- [ ] docs/api.md: document `/admin/feedback`, token fields in stats/history/search responses, and Phoenix config.
- [ ] Run final verification:

```bash
rtk make verify
cd web && pnpm build
```

- [ ] Report remaining manual deploy steps for almaz:
  - read-only verify Docker/Postgres/Caddy layout on almaz
  - decide `/opt/server/monitoring` vs `/opt/server/dev`
  - deploy Phoenix compose
  - configure Caddy/Authelia only after explicit approval
  - set `phoenix.enabled: true` and correct endpoint in llama-line config

---

## Acceptance criteria

- With `phoenix.enabled: false`, llama-line behavior unchanged.
- With Phoenix enabled, llama-line starts and emits OpenInference LLM spans to Phoenix OTLP HTTP.
- Non-streaming Ollama token counts are parsed and stored in audit attrs, Postgres request events, stats, and spans.
- `/admin/stats/summary` includes prompt/completion token totals.
- `/admin/feedback` proxies human annotations to Phoenix and remains admin-auth protected.
- UI shows token fields and offers a Phoenix link.
- `rtk make verify` and `pnpm build` pass.
