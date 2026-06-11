# Postgres Log Search Stats Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Persist llama-line app logs, request/audit bodies, embeddings, search indexes, and operational/quality stats in Postgres.

**Architecture:** Add an optional Postgres persistence subsystem behind config flags. Runtime request/app-log events are pushed to buffered channels and written by background workers so the hot inference path does not block on DB latency. Search uses Postgres full-text search plus pgvector semantic search; stats use raw recent queries first and rollups later.

**Tech Stack:** Go 1.26, `pgx/v5` + `pgxpool`, `pgvector/pgvector-go`, Postgres extensions `vector`, `pg_trgm`, optional built-in Ollama-compatible embeddings through llama-line upstream for semantic indexing.

---

## Approved requirements

- Backend: Postgres, not SQLite.
- Scope: Persist everything: broker app logs, request history, audit/request bodies, tool errors, and stats.
- Search: Full-text + embeddings.
- Stats: Ops dashboard plus quality/tool stats.
- Planning mode: Full written plan + review gate.
- Artifact persistence: repo file (`openspec`-style plan in `docs/superpowers/plans/`).

## Current state

- No DB dependency exists in `go.mod`.
- Request history is in-memory only via `internal/ui/history.go`.
- Audit/request bodies are JSONL file-backed via `audit_log_path` and exposed by `internal/server/logs.go`.
- Live app logs are in-memory only via `internal/logger/Broadcaster`.
- Admin/UI APIs currently expose:
  - `GET /admin/logs/recent`, `GET /admin/logs/stream`
  - `GET /admin/audit/recent`, `GET /admin/audit/stream`
  - `GET /ui/api/history`
- Frontend has pages for History, Logs, Audit, Queue, Keys, Config.

## Target file map

### Backend persistence package

- Create `internal/persistence/config.go`
  - DB config structs and validation.
- Create `internal/persistence/store.go`
  - `Store` lifecycle, pgx pool, migrations, worker startup/shutdown.
- Create `internal/persistence/migrations.go`
  - Embedded SQL migrations, migration runner.
- Create `internal/persistence/models.go`
  - `AppLogEvent`, `RequestEvent`, `EmbeddingJob`, `SearchFilters`, `StatsFilters`, result structs.
- Create `internal/persistence/writer.go`
  - Buffered app-log/request-event ingestion workers and batch insert methods.
- Create `internal/persistence/search.go`
  - Full-text search and semantic search queries.
- Create `internal/persistence/stats.go`
  - Ops and quality stats query methods.
- Create `internal/persistence/embedder.go`
  - Embedding text extraction, chunking, job polling, vector insert/query integration.
- Create tests under `internal/persistence/*_test.go`
  - SQL generation/unit tests without requiring a live DB first.

### Config/runtime wiring

- Modify `internal/config/config.go`
  - Add `DatabaseConfig` and `EmbeddingConfig` fields.
- Modify `config.example.yaml` and `config.yaml`
  - Add disabled-by-default Postgres and embedding settings.
- Modify `cmd/app/main.go`
  - Build `persistence.Store` when enabled, run migrations, start workers, defer close.
- Modify `internal/server/server.go`
  - Add `SetPersistenceStore`.
- Modify `internal/server/handler.go`
  - Emit request events to persistence after response/audit summary.
- Modify `internal/logger/broadcaster.go`
  - Allow app-log events to be observed by persistence or emit app log events in `cmd/app/main.go` from the broadcaster.

### API/UI

- Create `internal/server/search.go`
  - Admin endpoints for persisted logs/search/stats.
- Modify `internal/server/admin.go`
  - Route new admin endpoints.
- Modify `web/src/lib/api/types.ts`
  - Add DB search/stats result types.
- Modify `web/src/lib/api/endpoints.ts`
  - Add search/stats client functions.
- Modify `web/src/lib/api/hooks.ts`
  - Add React Query hooks.
- Modify `web/src/pages/Logs.tsx`, `web/src/pages/Audit.tsx`, `web/src/pages/History.tsx`
  - Add persisted search modes.
- Create `web/src/pages/Stats.tsx`
  - Ops + quality dashboard.
- Modify `web/src/App.tsx` and `web/src/components/Layout.tsx`
  - Add Stats route/tab.

### Docs/ops

- Modify `README.md`
  - Document Postgres setup, retention, body privacy risks, search/stats endpoints.
- Modify `docs/api.md`
  - Document new admin endpoints.
- Optional create `docker-compose.yml`
  - Local Postgres + pgvector service if user wants local dev convenience.

---

## Database schema contract

Use schema `broker` so llama-line tables do not collide with other databases.

Migration `001_init_persistence.sql`:

```sql
create schema if not exists broker;
create extension if not exists pgcrypto;
create extension if not exists vector;
create extension if not exists pg_trgm;

create table if not exists broker.schema_migrations (
  version integer primary key,
  applied_at timestamptz not null default now()
);

create table if not exists broker.app_log (
  id bigserial primary key,
  ts timestamptz not null,
  level text not null,
  msg text not null,
  fields jsonb not null default '{}'::jsonb,
  raw_json text not null,
  client text generated always as (fields->>'client') stored,
  request_id text generated always as (fields->>'id') stored,
  model text generated always as (fields->>'model') stored,
  path text generated always as (fields->>'path') stored,
  search_tsv tsvector generated always as (
    to_tsvector('simple',
      coalesce(level,'') || ' ' || coalesce(msg,'') || ' ' || coalesce(raw_json,'')
    )
  ) stored
);

create table if not exists broker.request_event (
  id uuid primary key default gen_random_uuid(),
  request_id text not null unique,
  ts timestamptz not null,
  client text not null,
  model text not null,
  path text not null,
  upstream text,
  status text not null,
  streaming boolean not null default false,
  duration_ms integer not null default 0,
  queue_ms integer not null default 0,
  upstream_ms integer not null default 0,
  request_body jsonb,
  response_body jsonb,
  request_body_text text,
  response_body_text text,
  request_body_bytes integer not null default 0,
  response_body_bytes integer not null default 0,
  response_json_valid boolean,
  request_message_count integer not null default 0,
  tool_names text[] not null default '{}',
  tool_error_count integer not null default 0,
  tool_errors text[] not null default '{}',
  raw_audit jsonb not null default '{}'::jsonb,
  search_tsv tsvector generated always as (
    to_tsvector('simple',
      coalesce(client,'') || ' ' || coalesce(model,'') || ' ' || coalesce(path,'') || ' ' ||
      coalesce(status,'') || ' ' || coalesce(request_body_text,'') || ' ' || coalesce(response_body_text,'') || ' ' ||
      array_to_string(tool_names, ' ') || ' ' || array_to_string(tool_errors, ' ')
    )
  ) stored
);

create table if not exists broker.request_embedding_job (
  request_event_id uuid primary key references broker.request_event(id) on delete cascade,
  status text not null default 'pending',
  attempts integer not null default 0,
  last_error text,
  available_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table if not exists broker.request_embedding (
  id bigserial primary key,
  request_event_id uuid not null references broker.request_event(id) on delete cascade,
  ts timestamptz not null default now(),
  chunk_ix integer not null,
  role text not null,
  content text not null,
  embedding_model text not null,
  embedding_dim integer not null,
  embedding vector(768) not null,
  meta jsonb not null default '{}'::jsonb,
  unique (request_event_id, chunk_ix, embedding_model)
);

create table if not exists broker.request_stats_1m (
  bucket_min timestamptz not null,
  client text not null,
  model text not null,
  path text not null,
  status text not null,
  cnt bigint not null,
  error_count bigint not null,
  tool_error_count bigint not null,
  malformed_count bigint not null,
  cache_hit_count bigint not null,
  p50_ms integer,
  p95_ms integer,
  p99_ms integer,
  queue_p95_ms integer,
  upstream_p95_ms integer,
  primary key (bucket_min, client, model, path, status)
);
```

Indexes:

```sql
create index if not exists app_log_ts_idx on broker.app_log (ts desc);
create index if not exists app_log_level_ts_idx on broker.app_log (level, ts desc);
create index if not exists app_log_client_ts_idx on broker.app_log (client, ts desc) where client is not null;
create index if not exists app_log_request_id_idx on broker.app_log (request_id) where request_id is not null;
create index if not exists app_log_search_idx on broker.app_log using gin (search_tsv);
create index if not exists app_log_raw_trgm_idx on broker.app_log using gin (raw_json gin_trgm_ops);

create index if not exists request_event_ts_idx on broker.request_event (ts desc);
create index if not exists request_event_client_ts_idx on broker.request_event (client, ts desc);
create index if not exists request_event_model_ts_idx on broker.request_event (model, ts desc);
create index if not exists request_event_status_ts_idx on broker.request_event (status, ts desc);
create index if not exists request_event_tool_error_ts_idx on broker.request_event (ts desc) where tool_error_count > 0;
create index if not exists request_event_malformed_ts_idx on broker.request_event (ts desc) where response_json_valid = false;
create index if not exists request_event_search_idx on broker.request_event using gin (search_tsv);
create index if not exists request_event_request_body_idx on broker.request_event using gin (request_body jsonb_path_ops) where request_body is not null;
create index if not exists request_event_response_body_idx on broker.request_event using gin (response_body jsonb_path_ops) where response_body is not null;

create index if not exists request_embedding_event_idx on broker.request_embedding (request_event_id);
create index if not exists request_embedding_ts_idx on broker.request_embedding (ts desc);
create index if not exists request_embedding_hnsw_idx on broker.request_embedding using hnsw (embedding vector_cosine_ops);

create index if not exists request_stats_1m_bucket_idx on broker.request_stats_1m (bucket_min desc);
```

**Embedding dimension:** start with `vector(768)` because the local model currently seen in traffic is `qwen3-embedding:0.6b`, commonly a smaller local embedding model. If the chosen embedding model returns a different dimension, add a migration that changes the vector table to the exact dimension before enabling embeddings. Do not silently truncate/pad vectors.

---

## API contract

All new endpoints are admin-only and support `X-Admin-Key`, `Authorization: Bearer`, and existing GET `?key=` behavior.

### `GET /admin/search/logs`

Query params:
- `q`: text query
- `level`: optional minimum or exact log level; use exact first: `DEBUG|INFO|WARN|ERROR`
- `client`, `model`, `path`, `request_id`
- `since`, `until`: RFC3339
- `limit`: default 100, max 1000
- `cursor`: base64 JSON `{ "ts":"...", "id":123 }`

Response:

```json
{
  "entries": [
    {"id": 1, "time": "2026-05-24T03:27:34Z", "level":"INFO", "msg":"request queued", "fields":{}, "raw_json":"{}"}
  ],
  "next_cursor": "..."
}
```

### `GET /admin/search/requests`

Query params:
- `q`, `client`, `model`, `path`, `status`, `has_tool_errors`, `malformed`, `since`, `until`, `limit`, `cursor`
- `include_bodies=false|true`; bodies only returned if DB contains them and config permits body reads.

Response:

```json
{
  "entries": [
    {
      "id": "uuid",
      "request_id": "217867ef47ac19f968886aab70885d40",
      "time": "2026-05-24T03:27:34Z",
      "client":"subcorp",
      "model":"qwen3:14b",
      "path":"/api/chat",
      "status":"success",
      "duration_ms":1000,
      "queue_ms":10,
      "upstream_ms":990,
      "tool_error_count":0,
      "tool_errors":[],
      "request_body": null,
      "response_body": null
    }
  ],
  "next_cursor": "..."
}
```

### `POST /admin/search/semantic`

Request:

```json
{"q":"docker permission failures during browsing", "limit":20, "client":"subcorp", "since":"2026-05-24T00:00:00Z"}
```

Response:

```json
{
  "entries": [
    {"request_id":"...", "score":0.82, "chunk":"permission denied while trying...", "role":"tool", "time":"..."}
  ]
}
```

### `GET /admin/stats/summary`

Query params: `since`, `until`, `bucket=minute|hour`, `client`, `model`.

Response includes:
- total requests
- success/error/cache counts
- tool error count
- malformed response count
- p50/p95/p99 total duration
- p95 queue and upstream duration
- top clients/models/paths/errors
- time buckets for charts

---

## Task 1: Add config and dependencies

**Files:**
- Modify: `go.mod`
- Modify: `internal/config/config.go`
- Modify: `config.example.yaml`
- Modify: `config.yaml`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Add dependencies**

Run:

```bash
go get github.com/jackc/pgx/v5@latest github.com/jackc/pgx/v5/pgxpool@latest github.com/pgvector/pgvector-go@latest
```

Expected: `go.mod` gains pgx and pgvector dependencies.

- [ ] **Step 2: Write failing config test**

Add this test to `internal/config/config_test.go`:

```go
func TestLoadDatabaseAndEmbeddingConfig(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(path, []byte(`
listen_addr: ":11434"
upstream: "http://127.0.0.1:11435"
database:
  enabled: true
  url: "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"
  max_conns: 8
  run_migrations: true
  persist_app_logs: true
  persist_request_bodies: true
  body_read_enabled: true
  retention_days: 30
embeddings:
  enabled: true
  model: "qwen3-embedding:0.6b"
  dimension: 768
  worker_count: 1
  max_chunk_chars: 4000
`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Database.Enabled || cfg.Database.URL == "" || cfg.Database.MaxConns != 8 {
		t.Fatalf("database config not loaded: %+v", cfg.Database)
	}
	if !cfg.Database.PersistAppLogs || !cfg.Database.PersistRequestBodies || !cfg.Database.BodyReadEnabled {
		t.Fatalf("database persistence flags not loaded: %+v", cfg.Database)
	}
	if !cfg.Embeddings.Enabled || cfg.Embeddings.Model != "qwen3-embedding:0.6b" || cfg.Embeddings.Dimension != 768 {
		t.Fatalf("embedding config not loaded: %+v", cfg.Embeddings)
	}
}
```

- [ ] **Step 3: Run test to verify RED**

Run:

```bash
go test ./internal/config -run TestLoadDatabaseAndEmbeddingConfig -count=1
```

Expected: FAIL because `Config.Database` and `Config.Embeddings` do not exist.

- [ ] **Step 4: Add config structs**

In `internal/config/config.go`, add:

```go
type DatabaseConfig struct {
	Enabled              bool   `yaml:"enabled"`
	URL                  string `yaml:"url"`
	MaxConns             int32  `yaml:"max_conns"`
	RunMigrations        bool   `yaml:"run_migrations"`
	PersistAppLogs       bool   `yaml:"persist_app_logs"`
	PersistRequestBodies bool   `yaml:"persist_request_bodies"`
	BodyReadEnabled      bool   `yaml:"body_read_enabled"`
	RetentionDays        int    `yaml:"retention_days"`
}

type EmbeddingConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Model         string `yaml:"model"`
	Dimension     int    `yaml:"dimension"`
	WorkerCount   int    `yaml:"worker_count"`
	MaxChunkChars int    `yaml:"max_chunk_chars"`
}
```

Add fields to `Config`:

```go
Database   DatabaseConfig  `yaml:"database"`
Embeddings EmbeddingConfig `yaml:"embeddings"`
```

In defaults, set:

```go
cfg.Database.MaxConns = 4
cfg.Database.RunMigrations = true
cfg.Database.RetentionDays = 30
cfg.Embeddings.Model = "qwen3-embedding:0.6b"
cfg.Embeddings.Dimension = 768
cfg.Embeddings.WorkerCount = 1
cfg.Embeddings.MaxChunkChars = 4000
```

- [ ] **Step 5: Update YAML examples**

Add to both `config.example.yaml` and `config.yaml`:

```yaml
database:
  enabled: false
  url: "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"
  max_conns: 4
  run_migrations: true
  persist_app_logs: true
  persist_request_bodies: false
  body_read_enabled: false
  retention_days: 30

embeddings:
  enabled: false
  model: qwen3-embedding:0.6b
  dimension: 768
  worker_count: 1
  max_chunk_chars: 4000
```

- [ ] **Step 6: Verify GREEN**

Run:

```bash
go test ./internal/config -run TestLoadDatabaseAndEmbeddingConfig -count=1
```

Expected: PASS.

---

## Task 2: Add migration runner and store lifecycle

**Files:**
- Create: `internal/persistence/config.go`
- Create: `internal/persistence/store.go`
- Create: `internal/persistence/migrations.go`
- Create: `internal/persistence/migrations/001_init_persistence.sql`
- Test: `internal/persistence/migrations_test.go`

- [ ] **Step 1: Write failing migration test**

Create `internal/persistence/migrations_test.go`:

```go
package persistence

import (
	"strings"
	"testing"
)

func TestEmbeddedMigrationsContainRequiredTablesAndIndexes(t *testing.T) {
	sql := strings.Join(embeddedMigrationSQL(), "\n")
	required := []string{
		"create schema if not exists broker",
		"create table if not exists broker.app_log",
		"create table if not exists broker.request_event",
		"create table if not exists broker.request_embedding",
		"create table if not exists broker.request_stats_1m",
		"using gin (search_tsv)",
		"using hnsw (embedding vector_cosine_ops)",
	}
	for _, want := range required {
		if !strings.Contains(strings.ToLower(sql), want) {
			t.Fatalf("migration missing %q\n%s", want, sql)
		}
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
go test ./internal/persistence -run TestEmbeddedMigrationsContainRequiredTablesAndIndexes -count=1
```

Expected: FAIL because package/files do not exist.

- [ ] **Step 3: Add migration SQL**

Create `internal/persistence/migrations/001_init_persistence.sql` using the SQL from the “Database schema contract” section above.

- [ ] **Step 4: Add migration embed helper**

Create `internal/persistence/migrations.go`:

```go
package persistence

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func embeddedMigrationSQL() []string {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		b, err := migrationFS.ReadFile("migrations/" + name)
		if err == nil {
			out = append(out, string(b))
		}
	}
	return out
}

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	for i, sql := range embeddedMigrationSQL() {
		version := i + 1
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		var exists bool
		_ = tx.QueryRow(ctx, `select exists(select 1 from broker.schema_migrations where version=$1)`, version).Scan(&exists)
		if exists {
			_ = tx.Rollback(ctx)
			continue
		}
		if _, err := tx.Exec(ctx, sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.Exec(ctx, `insert into broker.schema_migrations(version) values($1) on conflict do nothing`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Add store lifecycle**

Create `internal/persistence/store.go`:

```go
package persistence

import (
	"context"
	"log/slog"
	"time"

	pgvectorpgx "github.com/pgvector/pgvector-go/pgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	URL                  string
	MaxConns             int32
	RunMigrations        bool
	PersistAppLogs       bool
	PersistRequestBodies bool
	BodyReadEnabled      bool
	RetentionDays        int
	Embeddings           EmbeddingConfig
}

type EmbeddingConfig struct {
	Enabled       bool
	Model         string
	Dimension     int
	WorkerCount   int
	MaxChunkChars int
}

type Store struct {
	pool *pgxpool.Pool
	cfg  Config
	log  *slog.Logger
	done chan struct{}
}

func Open(ctx context.Context, cfg Config, log *slog.Logger) (*Store, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, err
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgvectorpgx.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, err
	}
	ctxPing, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(ctxPing); err != nil {
		pool.Close()
		return nil, err
	}
	s := &Store{pool: pool, cfg: cfg, log: log, done: make(chan struct{})}
	if cfg.RunMigrations {
		if err := runMigrations(ctx, pool); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	s.pool.Close()
}
```

- [ ] **Step 6: Verify migration test GREEN**

Run:

```bash
go test ./internal/persistence -run TestEmbeddedMigrationsContainRequiredTablesAndIndexes -count=1
```

Expected: PASS.

---

## Task 3: Add async app-log ingestion

**Files:**
- Create: `internal/persistence/models.go`
- Create: `internal/persistence/writer.go`
- Modify: `internal/logger/broadcaster.go`
- Modify: `cmd/app/main.go`
- Test: `internal/persistence/writer_test.go`

- [ ] **Step 1: Write failing unit test for bounded queue behavior**

Create `internal/persistence/writer_test.go`:

```go
package persistence

import (
	"testing"
	"time"
)

func TestStoreAcceptsAppLogWithoutBlockingWhenQueueFull(t *testing.T) {
	s := &Store{appLogCh: make(chan AppLogEvent, 1)}
	s.EnqueueAppLog(AppLogEvent{Time: time.Now(), Level: "INFO", Msg: "one"})
	s.EnqueueAppLog(AppLogEvent{Time: time.Now(), Level: "INFO", Msg: "two"})

	if got := len(s.appLogCh); got != 1 {
		t.Fatalf("queue length = %d, want capped at 1", got)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
go test ./internal/persistence -run TestStoreAcceptsAppLogWithoutBlockingWhenQueueFull -count=1
```

Expected: FAIL because `AppLogEvent`, `appLogCh`, and `EnqueueAppLog` do not exist.

- [ ] **Step 3: Add models**

Create/extend `internal/persistence/models.go`:

```go
package persistence

import "time"

type AppLogEvent struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Msg     string         `json:"msg"`
	Fields  map[string]any `json:"fields"`
	RawJSON string         `json:"raw_json"`
}

type RequestEvent struct {
	RequestID          string
	Time               time.Time
	Client             string
	Model              string
	Path               string
	Upstream           string
	Status             string
	Streaming          bool
	DurationMs         int64
	QueueMs            int64
	UpstreamMs         int64
	RequestBody        []byte
	ResponseBody       []byte
	RequestBodyText    string
	ResponseBodyText   string
	RequestBodyBytes   int
	ResponseBodyBytes  int
	ResponseJSONValid  *bool
	RequestMessageCount int
	ToolNames          []string
	ToolErrorCount     int
	ToolErrors         []string
	RawAudit           []byte
}
```

- [ ] **Step 4: Add non-blocking enqueue and writer loop**

Extend `Store` in `store.go`:

```go
	appLogCh chan AppLogEvent
```

Initialize it in `Open`:

```go
	appLogCh: make(chan AppLogEvent, 10000),
```

Create `internal/persistence/writer.go`:

```go
package persistence

import (
	"context"
	"encoding/json"
	"time"
)

func (s *Store) EnqueueAppLog(e AppLogEvent) {
	if s == nil || !s.cfg.PersistAppLogs || s.appLogCh == nil {
		return
	}
	select {
	case s.appLogCh <- e:
	default:
		if s.log != nil {
			s.log.Warn("dropping app log persistence event", "msg", e.Msg)
		}
	}
}

func (s *Store) Start(ctx context.Context) {
	if s == nil {
		return
	}
	go s.appLogWriter(ctx)
}

func (s *Store) appLogWriter(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]AppLogEvent, 0, 200)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.insertAppLogs(ctx, batch)
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			flush(); return
		case <-s.done:
			flush(); return
		case e := <-s.appLogCh:
			batch = append(batch, e)
			if len(batch) >= 200 { flush() }
		case <-ticker.C:
			flush()
		}
	}
}

func (s *Store) insertAppLogs(ctx context.Context, batch []AppLogEvent) error {
	for _, e := range batch {
		fields, _ := json.Marshal(e.Fields)
		if _, err := s.pool.Exec(ctx, `
			insert into broker.app_log(ts, level, msg, fields, raw_json)
			values($1,$2,$3,$4,$5)
		`, e.Time, e.Level, e.Msg, fields, e.RawJSON); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Expose log subscriber events to persistence**

In `cmd/app/main.go`, after `srv.SetLogBroadcaster(logBroadcaster)` and after store creation in Task 5, subscribe to broadcaster:

```go
if store != nil && cfg.Database.PersistAppLogs {
	ch, unsubscribe := logBroadcaster.Subscribe()
	defer unsubscribe()
	go func() {
		for rec := range ch {
			store.EnqueueAppLog(persistence.AppLogEvent{
				Time: rec.Time,
				Level: rec.Level,
				Msg: rec.Msg,
				Fields: rec.Fields,
				RawJSON: rec.RawJSON,
			})
		}
	}()
}
```

If `logger.LogRecord.Time` is currently `string`, convert with `time.Parse(time.RFC3339Nano, rec.Time)` and fallback to `time.Now()`.

- [ ] **Step 6: Verify GREEN**

Run:

```bash
go test ./internal/persistence -run TestStoreAcceptsAppLogWithoutBlockingWhenQueueFull -count=1
```

Expected: PASS.

---

## Task 4: Add async request/audit ingestion

**Files:**
- Modify: `internal/persistence/writer.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/handler.go`
- Test: `internal/server/handler_test.go`

- [ ] **Step 1: Write failing server test using a fake store interface**

Define a narrow interface in `internal/server/server.go`:

```go
type PersistenceStore interface {
	EnqueueRequestEvent(persistence.RequestEvent)
}
```

Then add a test in `internal/server/handler_test.go` that creates a fake store and asserts successful `/api/chat` completion calls `EnqueueRequestEvent` with duration fields and body bytes. The key assertion shape:

```go
if got.event.RequestID == "" || got.event.Client != "test-client" || got.event.Status != "success" {
	t.Fatalf("bad event: %+v", got.event)
}
if got.event.DurationMs <= 0 || got.event.UpstreamMs <= 0 {
	t.Fatalf("missing timing: %+v", got.event)
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
go test ./internal/server -run TestHandleInferencePersistsRequestEvent -count=1
```

Expected: FAIL because persistence store wiring does not exist.

- [ ] **Step 3: Add store field and setter**

In `internal/server/server.go`, add:

```go
	persistence PersistenceStore
```

Add:

```go
func (s *Server) SetPersistenceStore(store PersistenceStore) {
	s.persistence = store
}
```

- [ ] **Step 4: Add request event queue to persistence**

Extend `Store`:

```go
	requestCh chan RequestEvent
```

Initialize:

```go
	requestCh: make(chan RequestEvent, 10000),
```

Add methods in `writer.go`:

```go
func (s *Store) EnqueueRequestEvent(e RequestEvent) {
	if s == nil || s.requestCh == nil {
		return
	}
	select {
	case s.requestCh <- e:
	default:
		if s.log != nil { s.log.Warn("dropping request persistence event", "request_id", e.RequestID) }
	}
}
```

Add a `requestWriter` loop analogous to `appLogWriter` and call it from `Start`.

- [ ] **Step 5: Insert request events idempotently**

Implement:

```go
func (s *Store) insertRequestEvents(ctx context.Context, batch []RequestEvent) error {
	for _, e := range batch {
		reqBodyJSON, reqText := bodyJSONAndText(e.RequestBody, s.cfg.PersistRequestBodies)
		respBodyJSON, respText := bodyJSONAndText(e.ResponseBody, s.cfg.PersistRequestBodies)
		if _, err := s.pool.Exec(ctx, `
			insert into broker.request_event(
				request_id, ts, client, model, path, upstream, status, streaming,
				duration_ms, queue_ms, upstream_ms,
				request_body, response_body, request_body_text, response_body_text,
				request_body_bytes, response_body_bytes, response_json_valid,
				request_message_count, tool_names, tool_error_count, tool_errors, raw_audit
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)
			on conflict (request_id) do update set
				ts=excluded.ts,
				status=excluded.status,
				duration_ms=excluded.duration_ms,
				queue_ms=excluded.queue_ms,
				upstream_ms=excluded.upstream_ms,
				response_body=excluded.response_body,
				response_body_text=excluded.response_body_text,
				response_body_bytes=excluded.response_body_bytes,
				response_json_valid=excluded.response_json_valid,
				tool_error_count=excluded.tool_error_count,
				tool_errors=excluded.tool_errors,
				raw_audit=excluded.raw_audit
		`, e.RequestID, e.Time, e.Client, e.Model, e.Path, e.Upstream, e.Status, e.Streaming,
			e.DurationMs, e.QueueMs, e.UpstreamMs,
			reqBodyJSON, respBodyJSON, reqText, respText,
			e.RequestBodyBytes, e.ResponseBodyBytes, e.ResponseJSONValid,
			e.RequestMessageCount, e.ToolNames, e.ToolErrorCount, e.ToolErrors, e.RawAudit); err != nil {
			return err
		}
	}
	return nil
}
```

`bodyJSONAndText` returns `(nil, "")` when body persistence is off; when on, it stores JSON bytes if valid JSON and text always as raw string with a max size cap from a constant like `maxPersistedBodyBytes = 5 << 20`.

- [ ] **Step 6: Emit request event from handler**

In `internal/server/handler.go`, immediately after `writeAuditLog(...)` and `recordHistory(...)`, build `persistence.RequestEvent` from the same already-computed fields. Use the audit summarizer outputs already present in `writeAuditLog`; if helper currently private inside write path, extract `summarizeAuditRequest` return into a struct so both audit logging and persistence can use it.

- [ ] **Step 7: Verify GREEN**

Run:

```bash
go test ./internal/server -run TestHandleInferencePersistsRequestEvent -count=1
go test ./internal/persistence ./internal/server
```

Expected: PASS.

---

## Task 5: Wire store into app startup

**Files:**
- Modify: `cmd/app/main.go`
- Test: `internal/app` or app wiring test if existing pattern supports it

- [ ] **Step 1: Add startup wiring**

In `cmd/app/main.go`, after logger creation and config load, add:

```go
var store *persistence.Store
if cfg.Database.Enabled {
	storeCfg := persistence.Config{
		URL: cfg.Database.URL,
		MaxConns: cfg.Database.MaxConns,
		RunMigrations: cfg.Database.RunMigrations,
		PersistAppLogs: cfg.Database.PersistAppLogs,
		PersistRequestBodies: cfg.Database.PersistRequestBodies,
		BodyReadEnabled: cfg.Database.BodyReadEnabled,
		RetentionDays: cfg.Database.RetentionDays,
		Embeddings: persistence.EmbeddingConfig{
			Enabled: cfg.Embeddings.Enabled,
			Model: cfg.Embeddings.Model,
			Dimension: cfg.Embeddings.Dimension,
			WorkerCount: cfg.Embeddings.WorkerCount,
			MaxChunkChars: cfg.Embeddings.MaxChunkChars,
		},
	}
	var err error
	store, err = persistence.Open(ctx, storeCfg, log)
	if err != nil {
		return fmt.Errorf("open persistence store: %w", err)
	}
	defer store.Close()
	store.Start(ctx)
}
```

After `srv := server.New(...)`, add:

```go
if store != nil {
	srv.SetPersistenceStore(store)
}
```

- [ ] **Step 2: Wire app log subscriber**

Use the code from Task 3 Step 5.

- [ ] **Step 3: Verify build**

Run:

```bash
go build ./cmd/app
```

Expected: PASS.

---

## Task 6: Add full-text search endpoints

**Files:**
- Create: `internal/persistence/search.go`
- Create: `internal/server/search.go`
- Modify: `internal/server/admin.go`
- Test: `internal/server/search_test.go`

- [ ] **Step 1: Write failing route/auth test**

Create `internal/server/search_test.go` with a fake persistence searcher. Assert `GET /admin/search/logs?q=docker` returns JSON from fake store and requires admin auth.

- [ ] **Step 2: Add search models**

In `models.go`, add:

```go
type SearchFilters struct {
	Query string
	Client string
	Model string
	Path string
	Status string
	Level string
	RequestID string
	Since *time.Time
	Until *time.Time
	Limit int
	Cursor string
	IncludeBodies bool
}

type LogSearchResult struct {
	ID int64 `json:"id"`
	Time time.Time `json:"time"`
	Level string `json:"level"`
	Msg string `json:"msg"`
	Fields map[string]any `json:"fields"`
	RawJSON string `json:"raw_json"`
}

type RequestSearchResult struct {
	ID string `json:"id"`
	RequestID string `json:"request_id"`
	Time time.Time `json:"time"`
	Client string `json:"client"`
	Model string `json:"model"`
	Path string `json:"path"`
	Status string `json:"status"`
	DurationMs int64 `json:"duration_ms"`
	QueueMs int64 `json:"queue_ms"`
	UpstreamMs int64 `json:"upstream_ms"`
	ToolErrorCount int `json:"tool_error_count"`
	ToolErrors []string `json:"tool_errors"`
	RequestBody any `json:"request_body,omitempty"`
	ResponseBody any `json:"response_body,omitempty"`
}
```

- [ ] **Step 3: Implement search SQL**

In `search.go`, implement methods:

```go
func (s *Store) SearchLogs(ctx context.Context, f SearchFilters) ([]LogSearchResult, string, error)
func (s *Store) SearchRequests(ctx context.Context, f SearchFilters) ([]RequestSearchResult, string, error)
```

Use keyset pagination: `where (ts, id) < ($cursorTS, $cursorID)` for next pages. Use `search_tsv @@ plainto_tsquery('simple', $q)` when `q` is set. Always order `ts desc, id desc` and clamp limit to 1000.

- [ ] **Step 4: Add server handlers**

Create `internal/server/search.go` with:

```go
func (s *Server) HandleAdminSearchLogs(w http.ResponseWriter, r *http.Request)
func (s *Server) HandleAdminSearchRequests(w http.ResponseWriter, r *http.Request)
```

Parse query params into `persistence.SearchFilters`, call store, return `{entries,next_cursor}`.

- [ ] **Step 5: Add routes**

In `internal/server/admin.go`, add cases:

```go
case r.Method == http.MethodGet && r.URL.Path == "/admin/search/logs":
	s.HandleAdminSearchLogs(w, r)
case r.Method == http.MethodGet && r.URL.Path == "/admin/search/requests":
	s.HandleAdminSearchRequests(w, r)
```

- [ ] **Step 6: Verify**

Run:

```bash
go test ./internal/server -run Search -count=1
go test ./internal/persistence ./internal/server
```

Expected: PASS.

---

## Task 7: Add embedding jobs and semantic search

**Files:**
- Create: `internal/persistence/embedder.go`
- Modify: `internal/persistence/writer.go`
- Modify: `internal/server/search.go`
- Test: `internal/persistence/embedder_test.go`

- [ ] **Step 1: Write failing chunk extraction test**

Create `internal/persistence/embedder_test.go`:

```go
package persistence

import "testing"

func TestEmbeddingChunksPreferMessagesAndToolErrors(t *testing.T) {
	e := RequestEvent{
		RequestID: "r1",
		Client: "subcorp",
		Model: "qwen3:14b",
		RequestBodyText: `{"messages":[{"role":"user","content":"browse the web"}]}`,
		ResponseBodyText: `{"message":{"role":"assistant","content":"done"}}`,
		ToolErrors: []string{"permission denied docker API"},
	}
	chunks := embeddingChunks(e, 4000)
	if len(chunks) < 3 {
		t.Fatalf("chunks = %#v", chunks)
	}
	if chunks[0].Role == "" || chunks[0].Content == "" {
		t.Fatalf("bad first chunk: %#v", chunks[0])
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```bash
go test ./internal/persistence -run TestEmbeddingChunksPreferMessagesAndToolErrors -count=1
```

Expected: FAIL because embedder code does not exist.

- [ ] **Step 3: Add chunk extraction**

Create `internal/persistence/embedder.go`:

```go
package persistence

type embeddingChunk struct {
	Role string
	Content string
	Meta map[string]any
}

func embeddingChunks(e RequestEvent, maxChars int) []embeddingChunk {
	if maxChars <= 0 { maxChars = 4000 }
	var chunks []embeddingChunk
	for _, err := range e.ToolErrors {
		chunks = append(chunks, embeddingChunk{Role: "tool_error", Content: truncateString(err, maxChars)})
	}
	if e.RequestBodyText != "" {
		chunks = append(chunks, embeddingChunk{Role: "request", Content: truncateString(e.RequestBodyText, maxChars)})
	}
	if e.ResponseBodyText != "" {
		chunks = append(chunks, embeddingChunk{Role: "response", Content: truncateString(e.ResponseBodyText, maxChars)})
	}
	return chunks
}

func truncateString(s string, max int) string {
	if len(s) <= max { return s }
	return s[:max]
}
```

- [ ] **Step 4: Queue embedding job after request insert**

After inserting/upserting a `request_event`, insert into `broker.request_embedding_job` when embeddings enabled and at least one chunk exists:

```sql
insert into broker.request_embedding_job(request_event_id)
select id from broker.request_event where request_id=$1
on conflict do nothing
```

- [ ] **Step 5: Implement embedding worker interface**

Add an interface so tests do not require Ollama:

```go
type Embedder interface {
	Embed(ctx context.Context, model string, input string) ([]float32, error)
}
```

Implement the worker loop:
- select pending jobs with `for update skip locked`
- load request event text
- generate chunks
- call `Embed`
- verify `len(vector) == cfg.Embeddings.Dimension`; if not, mark job failed with dimension error
- insert vectors with `pgvector.NewVector(vec)`
- mark job done

- [ ] **Step 6: Implement semantic search query**

Add:

```go
func (s *Store) SemanticSearch(ctx context.Context, queryVector []float32, f SearchFilters) ([]SemanticSearchResult, error)
```

SQL shape:

```sql
select re.request_id, e.role, e.content, 1 - (e.embedding <=> $1) as score, re.ts
from broker.request_embedding e
join broker.request_event re on re.id = e.request_event_id
where ($2::text = '' or re.client = $2)
order by e.embedding <=> $1
limit $3
```

- [ ] **Step 7: Add semantic endpoint**

In `internal/server/search.go`, add `HandleAdminSemanticSearch`. It embeds the query text, calls `SemanticSearch`, and returns entries.

- [ ] **Step 8: Verify**

Run:

```bash
go test ./internal/persistence -run Embedding -count=1
go test ./internal/server -run Semantic -count=1
```

Expected: PASS.

---

## Task 8: Add stats queries and endpoint

**Files:**
- Create: `internal/persistence/stats.go`
- Modify: `internal/server/search.go`
- Test: `internal/persistence/stats_test.go`, `internal/server/search_test.go`

- [ ] **Step 1: Write failing stat SQL shape test**

Create `internal/persistence/stats_test.go`:

```go
package persistence

import (
	"strings"
	"testing"
)

func TestStatsSummarySQLIncludesOpsAndQualityMetrics(t *testing.T) {
	sql := statsSummarySQL()
	for _, want := range []string{"count(*)", "percentile_cont(0.95)", "tool_error_count", "response_json_valid = false", "status"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("stats SQL missing %q: %s", want, sql)
		}
	}
}
```

- [ ] **Step 2: Add stats models and SQL helper**

In `stats.go`, add:

```go
type StatsSummary struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount int64 `json:"success_count"`
	ErrorCount int64 `json:"error_count"`
	CacheHitCount int64 `json:"cache_hit_count"`
	ToolErrorCount int64 `json:"tool_error_count"`
	MalformedCount int64 `json:"malformed_count"`
	P50Ms int64 `json:"p50_ms"`
	P95Ms int64 `json:"p95_ms"`
	P99Ms int64 `json:"p99_ms"`
	QueueP95Ms int64 `json:"queue_p95_ms"`
	UpstreamP95Ms int64 `json:"upstream_p95_ms"`
}

func statsSummarySQL() string {
	return `
		select
			count(*) as total_requests,
			count(*) filter (where status='success') as success_count,
			count(*) filter (where status!='success') as error_count,
			count(*) filter (where status='cache_hit') as cache_hit_count,
			coalesce(sum(tool_error_count),0) as tool_error_count,
			count(*) filter (where response_json_valid = false) as malformed_count,
			coalesce(percentile_cont(0.50) within group (order by duration_ms),0)::bigint as p50_ms,
			coalesce(percentile_cont(0.95) within group (order by duration_ms),0)::bigint as p95_ms,
			coalesce(percentile_cont(0.99) within group (order by duration_ms),0)::bigint as p99_ms,
			coalesce(percentile_cont(0.95) within group (order by queue_ms),0)::bigint as queue_p95_ms,
			coalesce(percentile_cont(0.95) within group (order by upstream_ms),0)::bigint as upstream_p95_ms
		from broker.request_event
		where ts >= $1 and ts <= $2`
}
```

- [ ] **Step 3: Implement `StatsSummary` query**

Add:

```go
func (s *Store) StatsSummary(ctx context.Context, since, until time.Time) (StatsSummary, error)
```

Scan the row from `statsSummarySQL()`.

- [ ] **Step 4: Add endpoint**

Route `GET /admin/stats/summary` to `HandleAdminStatsSummary`, parse `since/until`, default since to `now - 1h`, until to `now`, and return JSON.

- [ ] **Step 5: Verify**

Run:

```bash
go test ./internal/persistence -run Stats -count=1
go test ./internal/server -run Stats -count=1
```

Expected: PASS.

---

## Task 9: Frontend API types/hooks and Stats page

**Files:**
- Modify: `web/src/lib/api/types.ts`
- Modify: `web/src/lib/api/endpoints.ts`
- Modify: `web/src/lib/api/hooks.ts`
- Create: `web/src/pages/Stats.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/components/Layout.tsx`

- [ ] **Step 1: Add TS types**

Add:

```ts
export interface LogSearchEntry {
  id: number
  time: string
  level: LogLevel
  msg: string
  fields: Record<string, unknown>
  raw_json: string
}

export interface RequestSearchEntry {
  id: string
  request_id: string
  time: string
  client: string
  model: string
  path: string
  status: string
  duration_ms: number
  queue_ms: number
  upstream_ms: number
  tool_error_count: number
  tool_errors: string[]
  request_body?: unknown
  response_body?: unknown
}

export interface StatsSummary {
  total_requests: number
  success_count: number
  error_count: number
  cache_hit_count: number
  tool_error_count: number
  malformed_count: number
  p50_ms: number
  p95_ms: number
  p99_ms: number
  queue_p95_ms: number
  upstream_p95_ms: number
}
```

- [ ] **Step 2: Add endpoints**

In `endpoints.ts`, add:

```ts
export function searchLogs(params: URLSearchParams) {
  return request<{entries: LogSearchEntry[]; next_cursor?: string}>(`/admin/search/logs?${params}`)
}

export function searchRequests(params: URLSearchParams) {
  return request<{entries: RequestSearchEntry[]; next_cursor?: string}>(`/admin/search/requests?${params}`)
}

export function getStatsSummary(params: URLSearchParams) {
  return request<StatsSummary>(`/admin/stats/summary?${params}`)
}
```

- [ ] **Step 3: Add hook**

In `hooks.ts`, add:

```ts
export function useStatsSummary(range = '1h') {
  return useQuery({
    queryKey: ['stats', 'summary', range],
    queryFn: () => {
      const until = new Date()
      const since = new Date(until.getTime() - (range === '24h' ? 86400000 : 3600000))
      const p = new URLSearchParams({ since: since.toISOString(), until: until.toISOString() })
      return getStatsSummary(p)
    },
    refetchInterval: 5000,
  })
}
```

- [ ] **Step 4: Add Stats page**

Create `web/src/pages/Stats.tsx`:

```tsx
import { useState } from 'react'
import { Card, CardHeader, CardTitle, CardContent } from '@/components/ui/Card'
import { Select } from '@/components/ui/Input'
import { Badge } from '@/components/ui/Badge'
import { useStatsSummary } from '@/lib/api/hooks'
import { formatDuration } from '@/lib/utils'

export function StatsPage() {
  const [range, setRange] = useState('1h')
  const stats = useStatsSummary(range)
  const s = stats.data
  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>Stats</CardTitle>
          <Select value={range} onChange={e => setRange(e.target.value)}>
            <option value="1h">last hour</option>
            <option value="24h">last 24h</option>
          </Select>
        </CardHeader>
        <CardContent>
          {!s ? <div className="text-[var(--color-muted)]">loading…</div> : (
            <div className="grid gap-3 md:grid-cols-3 xl:grid-cols-6">
              <Stat label="requests" value={s.total_requests} />
              <Stat label="success" value={s.success_count} badge="success" />
              <Stat label="errors" value={s.error_count} badge={s.error_count ? 'error' : 'neutral'} />
              <Stat label="tool errors" value={s.tool_error_count} badge={s.tool_error_count ? 'error' : 'neutral'} />
              <Stat label="malformed" value={s.malformed_count} badge={s.malformed_count ? 'warn' : 'neutral'} />
              <Stat label="cache hits" value={s.cache_hit_count} />
              <Stat label="p95 total" value={formatDuration(s.p95_ms)} />
              <Stat label="p99 total" value={formatDuration(s.p99_ms)} />
              <Stat label="p95 queue" value={formatDuration(s.queue_p95_ms)} />
              <Stat label="p95 llm" value={formatDuration(s.upstream_p95_ms)} />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

function Stat({ label, value, badge = 'info' }: { label: string; value: string | number; badge?: 'neutral' | 'info' | 'warn' | 'error' | 'success' }) {
  return <div className="rounded-[var(--radius)] border border-[var(--color-border)] bg-[var(--color-bg)] p-3 space-y-2">
    <div className="text-xs text-[var(--color-muted)]">{label}</div>
    <Badge variant={badge}>{value}</Badge>
  </div>
}
```

- [ ] **Step 5: Add route/tab**

In `App.tsx`, import `StatsPage` and add route `/stats`.

In `Layout.tsx`, add nav item `{ to: '/stats', label: 'Stats' }`.

- [ ] **Step 6: Verify web build**

Run:

```bash
cd web && pnpm build
```

Expected: PASS.

---

## Task 10: Persisted search UI for logs/requests

**Files:**
- Modify: `web/src/pages/Logs.tsx`
- Modify: `web/src/pages/Audit.tsx`
- Modify: `web/src/pages/History.tsx`

- [ ] **Step 1: Add persisted search mode to Logs**

Add a toggle `live | database` at the top of `LogsPage`. In database mode, show query input, level select, Search button, and render `searchLogs` results in the same heavy-formatted row style as `LogStream`.

- [ ] **Step 2: Add persisted request search to Audit**

Add a database search panel above the live audit list:
- query input
- client/model/status filters
- include bodies checkbox
- search button
- results list using `BodyView` for request/response bodies.

- [ ] **Step 3: Add persisted history mode**

Allow `HistoryPage` to switch between `/ui/api/history` and `/admin/search/requests` so old entries beyond ring buffer are searchable.

- [ ] **Step 4: Verify web build**

Run:

```bash
cd web && pnpm build
```

Expected: PASS.

---

## Task 11: Docs and verification

**Files:**
- Modify: `README.md`
- Modify: `docs/api.md`
- Optional create: `docker-compose.yml`

- [ ] **Step 1: Update README**

Document:
- `database.enabled`
- `database.url`
- `database.persist_app_logs`
- `database.persist_request_bodies`
- `database.body_read_enabled`
- `embeddings.enabled`
- `embeddings.model`
- privacy warning: full bodies may contain secrets/prompts/tool outputs
- retention expectations

- [ ] **Step 2: Update API docs**

Add sections for:
- `GET /admin/search/logs`
- `GET /admin/search/requests`
- `POST /admin/search/semantic`
- `GET /admin/stats/summary`

- [ ] **Step 3: Optional local compose**

If user wants a local DB, create `docker-compose.yml`:

```yaml
services:
  postgres:
    image: pgvector/pgvector:pg17
    environment:
      POSTGRES_DB: llama_line
      POSTGRES_USER: llama
      POSTGRES_PASSWORD: line
    ports:
      - "5432:5432"
    volumes:
      - postgres-data:/var/lib/postgresql/data
volumes:
  postgres-data:
```

- [ ] **Step 4: Final verification**

Run:

```bash
make verify
cd web && pnpm build
```

Expected: both PASS.

---

## Rollout notes

1. Ship DB disabled by default.
2. Enable `database.enabled=true` with `persist_request_bodies=false` first.
3. Confirm app logs and request metadata persist.
4. Enable `persist_request_bodies=true` only if sensitive prompt/response storage is acceptable.
5. Enable embeddings after confirming chosen embedding model dimension equals `embeddings.dimension`.
6. Run migration and monitor DB size/index growth.

## Self-review

- Spec coverage: Postgres persistence, indexing, full-text search, embeddings, and ops/quality stats are covered.
- Placeholder scan: no `TBD`/`TODO` placeholders remain; optional compose is explicitly optional.
- Type consistency: `RequestEvent`, `AppLogEvent`, search result, and stats result names are consistent across backend and frontend tasks.
- Risk notes: hot-path DB latency, vector dimension mismatch, privacy/body retention, index bloat, and rollout order are called out.
