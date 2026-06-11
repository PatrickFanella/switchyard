# Llama-Line Ops Completion Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Turn the current Phoenix/Postgres/ntfy prototype into reproducible, backed-up, documented llama-line observability infrastructure.

**Architecture:** Keep kvant `llama-line` as the broker and tracing source. Make local Postgres/pgvector and the broker runtime reproducible from repo files, route traces to Phoenix projects intentionally, and send broker alerts to the existing NUC ntfy alert path. Keep Phoenix on almaz with documented backup/auth decisions.

**Tech Stack:** Go 1.26, systemd, Docker Compose, PostgreSQL/pgvector, OpenTelemetry OTLP HTTP, Arize Phoenix v16, ntfy, Makefile verification.

---

## Current state to preserve

- Working directory: `/home/onnwee/Projects/tools/llama-line`.
- Live broker currently started manually from `bin/llama-line --config config.yaml`.
- Current DB container: `llama-line-postgres`, image `pgvector/pgvector:pg17`, host bind `127.0.0.1:5432`, volume `llama-line-postgres-data`, db/user `llama_line`/`llama`.
- `config.yaml` currently has DB persistence, embeddings, Phoenix tracing, and ntfy alerting enabled.
- Phoenix on almaz: `http://10.0.0.200:6006`, Caddy/Authelia UI at `phoenix.subcult.tv`.
- Current Phoenix routing: broker sends all traces to project `llama-line`; `default` is archived/legacy and still contains old traces.
- Current ntfy alert URL: `https://ntfy.subcult.tv/nuc-alerts?title=llama-line%20alert&priority=4&tags=llama-line,warning`.
- Existing modified/untracked files before this plan:
  - `README.md`
  - `docker/phoenix.yml`
  - `config.yaml`
  - `internal/config/config.go`
  - `internal/tracer/tracer.go`
  - `internal/tracer/tracer_test.go`
  - `internal/persistence/migrations/001_init_persistence.sql`
  - `internal/persistence/migrations/003_request_search_body_json.sql`
  - `internal/persistence/migrations_test.go`
  - `scripts/phoenix_bootstrap.py`

---

## File ownership map

### Runtime and deployment
- Modify: `docker-compose.yml` — add reproducible local `postgres` profile using `pgvector/pgvector:pg17` and volume `llama-line-postgres-data`.
- Modify: `deploy/llama-line.service` — make service point at repo-local binary/config for kvant or document install paths cleanly.
- Create: `deploy/llama-line.env.example` — non-secret runtime env example.
- Create: `deploy/install-systemd.sh` — idempotent install/update script for binary, config, systemd unit.
- Modify: `.gitignore` — ensure generated secrets/data/logs stay out of git.

### Backups and docs
- Create: `docs/operations/runtime.md` — exact runtime/start/restart/status flow.
- Create: `docs/operations/backups.md` — DB dump/restore, Phoenix volume gap, restic integration notes.
- Modify: `README.md` — link runtime, DB, Phoenix bootstrap, alert docs.
- Modify: `docs/api.md` — fix alert webhook JSON docs to match actual `AlertPayload`.

### Phoenix project routing
- Modify: `internal/config/config.go` — add `phoenix.project_mode`, keep `phoenix.project_name`.
- Modify: `internal/config/load.go` — validate project mode and defaults.
- Modify: `internal/config/load_test.go` — tests for static/client project mode.
- Create: `internal/tracer/manager.go` — manages tracer providers keyed by Phoenix project.
- Create: `internal/tracer/manager_test.go` — verifies `x-project-name` header per project.
- Modify: `internal/tracer/tracer.go` — keep single-project constructor as helper.
- Modify: `internal/server/server.go` — replace single `trace.Tracer` field with tracer resolver interface.
- Modify: `internal/server/handler.go` — start request spans using project derived from `clientName`.
- Modify: `cmd/app/main.go` — wire tracer manager and shutdown all providers.

### Alerts
- Modify: `internal/config/config.go` — add optional ntfy formatting fields if needed.
- Modify: `internal/metrics/alert.go` — either keep generic webhook or add ntfy-aware formatting.
- Modify: `internal/metrics/alert_test.go` — verify payload/headers.
- Create: `scripts/send_test_alert.py` — safe route test for configured webhook.

### Phoenix datasets/evals
- Modify: `scripts/phoenix_bootstrap.py` — keep idempotent setup; add `--verify` mode.
- Create: `scripts/phoenix_eval_smoke.py` — run dataset examples through broker and create experiment runs/evaluations.
- Create: `docs/operations/evals.md` — how to run and interpret evals.

---

## Task 1: Freeze and verify current local state

**Files:** none changed unless unexpected drift is found.

- [ ] **Step 1: Capture status**

Run:

```bash
/usr/bin/git status --short
/usr/bin/git diff --stat
pgrep -af '[l]lama-line.*config.yaml' || true
docker ps --filter name=llama-line-postgres --format '{{.Names}} {{.Image}} {{.Status}} {{.Ports}}'
```

Expected:
- Shows current modified files listed above.
- Shows one broker process.
- Shows `llama-line-postgres pgvector/pgvector:pg17 Up ... 127.0.0.1:5432->5432/tcp`.

- [ ] **Step 2: Run full verification**

Run:

```bash
make verify
```

Expected: `go vet ./...`, `go test ./...`, and `skip compose config` all succeed.

- [ ] **Step 3: Record current runtime evidence**

Run:

```bash
curl -fsS http://127.0.0.1:11434/broker/status | python3 -m json.tool | sed -n '1,80p'
PGPASSWORD=line psql 'postgres://llama@127.0.0.1:5432/llama_line?sslmode=disable' -c \
  "select version from broker.schema_migrations order by version; select count(*) request_events from broker.request_event; select count(*) embeddings from broker.request_embedding;"
```

Expected:
- `/broker/status` returns queue/upstream JSON.
- migration versions `1,2,3,4` exist.

---

## Task 2: Make local Postgres reproducible

**Files:**
- Create: `docker-compose.yml`
- Modify: `docs/stacks.md`
- Modify: `.gitignore` only if local DB artifacts are not already ignored.

- [ ] **Step 1: Create compose file**

Create `docker-compose.yml`:

```yaml
services:
  postgres:
    image: pgvector/pgvector:pg17
    container_name: llama-line-postgres
    restart: unless-stopped
    profiles:
      - db
    environment:
      POSTGRES_DB: llama_line
      POSTGRES_USER: llama
      POSTGRES_PASSWORD: line
    ports:
      - "127.0.0.1:5432:5432"
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U llama -d llama_line"]
      interval: 10s
      timeout: 5s
      retries: 5
    volumes:
      - llama-line-postgres-data:/var/lib/postgresql/data

volumes:
  llama-line-postgres-data:
    name: llama-line-postgres-data
```

- [ ] **Step 2: Validate compose config**

Run:

```bash
docker compose --profile db config --quiet
```

Expected: exits 0.

- [ ] **Step 3: Confirm compose adopts the existing container**

Run:

```bash
docker compose --profile db up -d postgres
docker compose --profile db ps postgres
```

Expected:
- Existing `llama-line-postgres` remains running or is recreated without data loss because it uses named volume `llama-line-postgres-data`.
- Status is healthy/running.

- [ ] **Step 4: Update `docs/stacks.md` Postgres section**

Replace the generic Postgres snippet with:

```markdown
## Postgres

Local llama-line persistence uses pgvector:

```bash
docker compose --profile db up -d postgres
PGPASSWORD=line psql 'postgres://llama@127.0.0.1:5432/llama_line?sslmode=disable' -c 'select current_database(), current_user;'
```

The schema is created by llama-line when `database.run_migrations: true`.
The named Docker volume is `llama-line-postgres-data`.
```

- [ ] **Step 5: Run tests**

Run:

```bash
make verify
```

Expected: pass.

---

## Task 3: Stabilize broker runtime with systemd

**Files:**
- Modify: `deploy/llama-line.service`
- Create: `deploy/llama-line.env.example`
- Create: `deploy/install-systemd.sh`
- Create: `docs/operations/runtime.md`

- [ ] **Step 1: Update systemd unit**

Replace `deploy/llama-line.service` with:

```ini
[Unit]
Description=llama-line Ollama broker
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
User=onnwee
Group=onnwee
WorkingDirectory=/home/onnwee/Projects/tools/llama-line
EnvironmentFile=-/etc/llama-line/env
ExecStart=/home/onnwee/Projects/tools/llama-line/bin/llama-line --config /home/onnwee/Projects/tools/llama-line/config.yaml
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal
SyslogIdentifier=llama-line

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 2: Add env example**

Create `deploy/llama-line.env.example`:

```bash
# Optional runtime overrides for llama-line.service.
# Keep secrets out of this committed example.
LLAMA_LINE_LOG_LEVEL=info
```

- [ ] **Step 3: Add install script**

Create executable `deploy/install-systemd.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

install -d -m 0755 /etc/llama-line
if [[ ! -f /etc/llama-line/env ]]; then
  install -m 0644 "$repo_root/deploy/llama-line.env.example" /etc/llama-line/env
fi

go build -o "$repo_root/bin/llama-line" "$repo_root/cmd/app"
install -m 0644 "$repo_root/deploy/llama-line.service" /etc/systemd/system/llama-line.service
systemctl daemon-reload
systemctl enable llama-line.service
systemctl restart llama-line.service
systemctl --no-pager --full status llama-line.service
```

Run:

```bash
chmod +x deploy/install-systemd.sh
```

- [ ] **Step 4: Document runtime**

Create `docs/operations/runtime.md`:

```markdown
# llama-line Runtime

## Start dependencies

```bash
docker compose --profile db up -d postgres
```

## Install/update systemd service

```bash
sudo ./deploy/install-systemd.sh
```

## Status

```bash
systemctl status llama-line --no-pager
journalctl -u llama-line -n 100 --no-pager
curl -fsS http://127.0.0.1:11434/broker/status | python3 -m json.tool
```

## Manual fallback

```bash
go build -o bin/llama-line ./cmd/app
setsid "$PWD/bin/llama-line" --config config.yaml > tmp/llama-line.out 2>&1 < /dev/null &
```
```

- [ ] **Step 5: Verify service in dry-run-safe way**

Run on kvant only when ready to replace the manual process:

```bash
sudo ./deploy/install-systemd.sh
curl -fsS http://127.0.0.1:11434/broker/status | python3 -m json.tool | sed -n '1,60p'
```

Expected: service active, status endpoint works.

---

## Task 4: Add DB backup and restore path

**Files:**
- Create: `scripts/backup_postgres.sh`
- Create: `scripts/restore_postgres.sh`
- Create: `docs/operations/backups.md`
- Modify: `.gitignore`

- [ ] **Step 1: Add backup script**

Create executable `scripts/backup_postgres.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

backup_dir="${BACKUP_DIR:-/home/onnwee/Projects/tools/llama-line/backups/postgres}"
ts="$(date -u +%Y%m%dT%H%M%SZ)"
out="$backup_dir/llama_line-$ts.sql.gz"

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

docker exec llama-line-postgres pg_dump -U llama -d llama_line --no-owner --no-privileges \
  | gzip -9 > "$out"
chmod 600 "$out"
printf '%s\n' "$out"
```

Run:

```bash
chmod +x scripts/backup_postgres.sh
```

- [ ] **Step 2: Add restore script**

Create executable `scripts/restore_postgres.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

dump="${1:?usage: scripts/restore_postgres.sh backups/postgres/llama_line-YYYYMMDDTHHMMSSZ.sql.gz}"

test -f "$dump"
gzip -t "$dump"

docker exec llama-line-postgres psql -U llama -d llama_line -v ON_ERROR_STOP=1 -c 'drop schema if exists broker cascade;'
gzip -dc "$dump" | docker exec -i llama-line-postgres psql -U llama -d llama_line -v ON_ERROR_STOP=1
docker exec llama-line-postgres psql -U llama -d llama_line -c 'select version from broker.schema_migrations order by version;'
```

Run:

```bash
chmod +x scripts/restore_postgres.sh
```

- [ ] **Step 3: Ignore local backups**

Add to `.gitignore` if absent:

```gitignore
/backups/
```

- [ ] **Step 4: Test backup**

Run:

```bash
scripts/backup_postgres.sh
ls -lh backups/postgres/llama_line-*.sql.gz | tail -1
```

Expected: compressed dump exists, mode is not world-readable.

- [ ] **Step 5: Document backups**

Create `docs/operations/backups.md`:

```markdown
# llama-line Backups

## Local Postgres

Create a dump:

```bash
scripts/backup_postgres.sh
```

Restore a dump:

```bash
scripts/restore_postgres.sh backups/postgres/llama_line-YYYYMMDDTHHMMSSZ.sql.gz
```

The current local dump directory is `backups/postgres/` and is gitignored.

## Homelab backup follow-up

For durable backups, add `/home/onnwee/Projects/tools/llama-line/backups/postgres` or an equivalent scripted dump path to the relevant host backup runbook. Do not back up raw prompt/response bodies without accepting privacy risk.

## Phoenix data

Phoenix still stores SQLite data in almaz Docker volume `monitoring_phoenix_data`. That is separate from llama-line Postgres and needs either a volume backup or a Phoenix migration to Postgres.
```

---

## Task 5: Clean repo state and document config boundaries

**Files:**
- Modify: `README.md`
- Modify: `config.example.yaml`
- Create: `docs/operations/configuration.md`
- Modify: `.gitignore`

- [ ] **Step 1: Add config docs**

Create `docs/operations/configuration.md`:

```markdown
# llama-line Configuration

## Sensitive files

- `config.yaml` contains local admin/client keys and must stay uncommitted unless secrets are redacted.
- `config.example.yaml` is the committed template.
- `.env` contains local project metadata and stays local.

## Production-adjacent local defaults

Current kvant runtime uses:

```yaml
database:
  enabled: true
  url: "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"

embeddings:
  enabled: true
  model: qwen3-embedding:0.6b
  dimension: 768

phoenix:
  enabled: true
  otlp_endpoint: "http://10.0.0.200:6006/v1/traces"
  project_name: "llama-line"

alert_webhook_url: "https://ntfy.subcult.tv/nuc-alerts?title=llama-line%20alert&priority=4&tags=llama-line,warning"
alert_queue_full_threshold: 180
```
```

- [ ] **Step 2: Update `config.example.yaml`**

Ensure `config.example.yaml` includes non-secret examples for:

```yaml
alert_webhook_url: ""
alert_queue_full_threshold: 0

phoenix:
  enabled: false
  otlp_endpoint: "http://phoenix:6006/v1/traces"
  project_name: "llama-line"
```

- [ ] **Step 3: Confirm secrets are ignored**

Run:

```bash
/usr/bin/git status --short --ignored | sed -n '1,120p'
```

Expected:
- `config.yaml` should either be tracked intentionally with redacted values or moved to local-only flow. If tracked and contains real keys, stop and choose one:
  1. keep it uncommitted and document local-only, or
  2. replace secrets with placeholders and create `config.local.yaml` ignored by git.

- [ ] **Step 4: Run verify**

Run:

```bash
make verify
```

Expected: pass.

---

## Task 6: Harden Phoenix backup/auth decisions

**Files:**
- Modify: `docker/phoenix.yml`
- Create: `docs/operations/phoenix.md` or update `/home/onnwee/docs/almaz/services/phoenix.md` on almaz after live verification.

- [ ] **Step 1: Keep current hardening in repo**

Verify `docker/phoenix.yml` has:

```yaml
image: arizephoenix/phoenix:version-16.0.0
ports:
  - "10.0.0.200:6006:6006"
environment:
  PHOENIX_WORKING_DIR: /mnt/data
  PHOENIX_DEFAULT_RETENTION_POLICY_DAYS: "60"
  PHOENIX_TELEMETRY_ENABLED: "false"
  PHOENIX_ALLOW_EXTERNAL_RESOURCES: "false"
```

- [ ] **Step 2: Add Phoenix backup runbook command**

Document this safe SQLite-volume backup command in `docs/operations/phoenix.md`:

```bash
ssh almaz 'docker stop phoenix && docker run --rm -v monitoring_phoenix_data:/data -v /opt/server/monitoring/backups:/backup alpine:3.20 sh -c "tar -czf /backup/phoenix-data-$(date -u +%Y%m%dT%H%M%SZ).tar.gz -C /data ." && docker start phoenix'
```

Also document a lower-downtime alternative:

```bash
ssh almaz 'docker exec phoenix sqlite3 /mnt/data/phoenix.db ".backup /mnt/data/phoenix-backup.db"'
```

- [ ] **Step 3: Decide auth path before changing live Phoenix**

Do not enable Phoenix auth until the API key bootstrap path is confirmed. Current LAN direct Phoenix is unauthenticated; Caddy UI is protected by Authelia. Document the current stance:

```markdown
Phoenix native auth remains disabled. Public UI is protected by Authelia. LAN direct `10.0.0.200:6006` is trusted LAN only. If this changes, enable Phoenix auth and create a system API key before turning on automation.
```

- [ ] **Step 4: Verify almaz docs audit after doc edits**

Run on almaz if almaz docs are edited:

```bash
ssh almaz 'python3 /home/onnwee/.config/opencode/oh-my-opencode-slim/managed-skills/almaz/scripts/almaz_doc_audit.py /home/onnwee/docs/almaz'
```

Expected: `errors=0 warnings=0`.

---

## Task 7: Improve ntfy alerts without breaking generic webhooks

**Files:**
- Modify: `internal/metrics/alert.go`
- Create/modify: `internal/metrics/alert_test.go`
- Create: `scripts/send_test_alert.py`
- Modify: `docs/api.md`

- [ ] **Step 1: Add alert tests**

Create `internal/metrics/alert_test.go` with:

```go
package metrics

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAlerterSendsPayload(t *testing.T) {
	done := make(chan AlertPayload, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q", got)
		}
		var payload AlertPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		done <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	a := NewAlerter(server.URL, slog.Default())
	a.Send("route_test", "test message", map[string]int{"depth": 180})

	select {
	case payload := <-done:
		if payload.Event != "route_test" || payload.Message != "test message" {
			t.Fatalf("payload = %#v", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for alert")
	}
}

func TestQueueFullThreshold(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	a := NewAlerter(server.URL, slog.Default())

	a.AlertQueueFull(179, 200, 180)
	select {
	case <-requests:
		t.Fatal("unexpected alert below threshold")
	case <-time.After(100 * time.Millisecond):
	}

	a.AlertQueueFull(180, 200, 180)
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("expected alert at threshold")
	}
}
```

- [ ] **Step 2: Add test alert script**

Create `scripts/send_test_alert.py`:

```python
#!/usr/bin/env python3
import json
import sys
import urllib.request
import yaml

cfg = yaml.safe_load(open("config.yaml", "r", encoding="utf-8"))
url = cfg.get("alert_webhook_url")
if not url:
    raise SystemExit("alert_webhook_url is empty")

payload = {
    "event": "route_test",
    "message": "llama-line alert webhook configured",
    "detail": {"safe_test": True},
}
req = urllib.request.Request(
    url,
    data=json.dumps(payload).encode(),
    headers={"Content-Type": "application/json"},
    method="POST",
)
with urllib.request.urlopen(req, timeout=10) as resp:
    print(resp.status)
    sys.exit(0 if 200 <= resp.status < 300 else 1)
```

Run:

```bash
chmod +x scripts/send_test_alert.py
python3 scripts/send_test_alert.py
```

Expected: prints `200`.

- [ ] **Step 3: Fix docs/api.md alert payload**

Replace the current alert example with:

```json
{"event":"queue_full","message":"queue depth reached alert threshold","timestamp":"2026-05-25T04:12:03Z","detail":{"depth":180,"max_depth":200,"threshold":180}}
{"event":"ollama_unavailable","message":"ollama is not reachable","timestamp":"2026-05-25T04:12:03Z"}
```

- [ ] **Step 4: Verify delivery in ntfy cache**

Run:

```bash
python3 scripts/send_test_alert.py
ssh nuc "python3 - <<'PY'
import datetime, sqlite3
con=sqlite3.connect('file:/srv/server/management/data/ntfy/cache/cache.db?mode=ro', uri=True)
con.row_factory=sqlite3.Row
for row in con.execute('select time, topic, title, message, priority, tags from messages where topic=? order by time desc limit 3', ('nuc-alerts',)):
    print(datetime.datetime.fromtimestamp(row['time'], datetime.UTC).isoformat(), row['topic'], row['priority'], row['title'], row['tags'], (row['message'] or '')[:160].replace('\\n',' '))
PY"
```

Expected: latest message title `llama-line alert`, tags `llama-line,warning`.

---

## Task 8: Route Phoenix traces per client project

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Create: `internal/tracer/manager.go`
- Create: `internal/tracer/manager_test.go`
- Modify: `internal/server/server.go`
- Modify: `internal/server/handler.go`
- Modify: `cmd/app/main.go`
- Modify: `config.example.yaml`
- Modify: `README.md`

- [ ] **Step 1: Extend config types**

In `internal/config/config.go`, change `PhoenixConfig` to:

```go
type PhoenixConfig struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	ProjectName  string `yaml:"project_name"`
	ProjectMode  string `yaml:"project_mode"` // static | client
}
```

- [ ] **Step 2: Add defaults and validation**

In `internal/config/load.go`, after `applyDefaults(&cfg)`, keep endpoint validation and add:

```go
	if cfg.Phoenix.Enabled && cfg.Phoenix.ProjectMode != "" && cfg.Phoenix.ProjectMode != "static" && cfg.Phoenix.ProjectMode != "client" {
		return nil, fmt.Errorf("config: phoenix.project_mode must be static or client")
	}
```

In `applyDefaults`, add:

```go
	if cfg.Phoenix.ProjectMode == "" {
		cfg.Phoenix.ProjectMode = "static"
	}
	if cfg.Phoenix.ProjectName == "" {
		cfg.Phoenix.ProjectName = "llama-line"
	}
```

- [ ] **Step 3: Test config**

In `internal/config/load_test.go`, add tests:

```go
func TestPhoenixProjectModeDefault(t *testing.T) {
	path := writeTempConfig(t, `listen_addr: ":0"
ollama_url: "http://127.0.0.1:11434"
phoenix:
  enabled: true
  otlp_endpoint: "http://phoenix:6006/v1/traces"
keys: []
`)
	cfg, err := Load(path)
	if err != nil { t.Fatal(err) }
	if cfg.Phoenix.ProjectMode != "static" { t.Fatalf("mode=%q", cfg.Phoenix.ProjectMode) }
	if cfg.Phoenix.ProjectName != "llama-line" { t.Fatalf("project=%q", cfg.Phoenix.ProjectName) }
}

func TestPhoenixProjectModeValidation(t *testing.T) {
	path := writeTempConfig(t, `listen_addr: ":0"
ollama_url: "http://127.0.0.1:11434"
phoenix:
  enabled: true
  otlp_endpoint: "http://phoenix:6006/v1/traces"
  project_mode: "bad"
keys: []
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "phoenix.project_mode") { t.Fatalf("err=%v", err) }
}
```

Use the existing helper name if `writeTempConfig` differs in this repo; inspect `internal/config/load_test.go` before editing.

- [ ] **Step 4: Add tracer manager**

Create `internal/tracer/manager.go`:

```go
package tracer

import (
	"context"
	"sync"

	"go.opentelemetry.io/otel/trace"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

type Resolver interface {
	TracerForClient(client string) trace.Tracer
	Shutdown()
}

type Manager struct {
	cfg       config.PhoenixConfig
	mu        sync.Mutex
	tracers   map[string]trace.Tracer
	shutdowns []func()
}

func NewManager(ctx context.Context, cfg config.PhoenixConfig) (*Manager, error) {
	m := &Manager{cfg: cfg, tracers: map[string]trace.Tracer{}}
	if !cfg.Enabled {
		return m, nil
	}
	if cfg.ProjectMode == "" || cfg.ProjectMode == "static" {
		tr, shutdown, err := New(ctx, cfg)
		if err != nil { return nil, err }
		m.tracers[cfg.ProjectName] = tr
		m.shutdowns = append(m.shutdowns, shutdown)
	}
	return m, nil
}

func (m *Manager) TracerForClient(client string) trace.Tracer {
	if !m.cfg.Enabled {
		return nil
	}
	project := m.cfg.ProjectName
	if m.cfg.ProjectMode == "client" && client != "" {
		project = client
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if tr, ok := m.tracers[project]; ok {
		return tr
	}
	cfg := m.cfg
	cfg.ProjectName = project
	tr, shutdown, err := New(context.Background(), cfg)
	if err != nil {
		return nil
	}
	m.tracers[project] = tr
	m.shutdowns = append(m.shutdowns, shutdown)
	return tr
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, shutdown := range m.shutdowns {
		shutdown()
	}
}
```

- [ ] **Step 5: Test manager headers**

Create `internal/tracer/manager_test.go` with an HTTP test server collecting `x-project-name` values for two clients. Use spans and `Shutdown()` to flush. Expected collected projects include `subcorp` and `jury-rigged`.

- [ ] **Step 6: Wire server resolver**

In `internal/server/server.go`, replace:

```go
tracer trace.Tracer
```

with:

```go
tracerResolver interface { TracerForClient(string) trace.Tracer }
```

Add:

```go
func (s *Server) SetTracerResolver(r interface{ TracerForClient(string) trace.Tracer }) {
	s.tracerResolver = r
}
```

Keep `SetTracer` for tests/backward compatibility by wrapping the fixed tracer:

```go
type fixedTracerResolver struct{ tracer trace.Tracer }
func (f fixedTracerResolver) TracerForClient(string) trace.Tracer { return f.tracer }

func (s *Server) SetTracer(tracer trace.Tracer) {
	s.tracerResolver = fixedTracerResolver{tracer: tracer}
}
```

- [ ] **Step 7: Start spans per client**

In `internal/server/handler.go`, wherever `s.tracer` is used to start spans, change to:

```go
var tr trace.Tracer
if s.tracerResolver != nil {
	tr = s.tracerResolver.TracerForClient(clientName)
}
if tr != nil {
	ctx, span = tr.Start(ctx, "llama-line.request")
	defer span.End()
}
```

Preserve existing span attributes.

- [ ] **Step 8: Wire in main**

In `cmd/app/main.go`, replace single tracer wiring with:

```go
tracerManager, err := tracer.NewManager(ctx, cfg.Phoenix)
if err != nil {
	fmt.Fprintf(os.Stderr, "error opening phoenix tracer: %v\n", err)
	os.Exit(1)
}
defer tracerManager.Shutdown()
srv.SetTracerResolver(tracerManager)
```

- [ ] **Step 9: Set config to client mode**

In `config.yaml` after confirming all Phoenix projects exist:

```yaml
phoenix:
  enabled: true
  otlp_endpoint: "http://10.0.0.200:6006/v1/traces"
  project_name: "llama-line"
  project_mode: "client"
```

- [ ] **Step 10: Verify live routing**

Run:

```bash
go build -o bin/llama-line ./cmd/app
sudo systemctl restart llama-line || { pkill -f '[l]lama-line.*config.yaml' || true; setsid "$PWD/bin/llama-line" --config config.yaml > tmp/llama-line.out 2>&1 < /dev/null & }
```

Send one smoke request using `subcorp` key and one using `jury-rigged` key, then verify Phoenix:

```bash
python3 - <<'PY'
import json, urllib.request, urllib.parse
base='http://10.0.0.200:6006'
for name in ['subcorp','jury-rigged','llama-line']:
    obj=json.load(urllib.request.urlopen(base+f'/v1/projects/{urllib.parse.quote(name)}/traces?limit=3'))
    print(name, len(obj.get('data', [])))
PY
```

Expected: new traces land in client project names, not just `llama-line`.

---

## Task 9: Automate Phoenix eval smoke runs

**Files:**
- Modify: `scripts/phoenix_bootstrap.py`
- Create: `scripts/phoenix_eval_smoke.py`
- Create: `docs/operations/evals.md`

- [ ] **Step 1: Add `--verify` to bootstrap**

Extend `scripts/phoenix_bootstrap.py` with a `--verify` flag that lists projects, prompts, datasets, annotation configs, and experiment counts, then exits nonzero if any managed object is missing.

Implement the check using existing `PROJECTS`, `PROMPTS`, `DATASETS`, and `ANNOTATION_CONFIGS` constants:

```python
def verify(client: PhoenixClient) -> bool:
    ok = True
    projects = {p['name'] for p in client.get_all('/v1/projects')}
    prompts = {p['name'] for p in client.get_all('/v1/prompts')}
    datasets = {d['name'] for d in client.get_all('/v1/datasets')}
    configs = {c['name'] for c in client.get_all('/v1/annotation_configs')}
    for name, _ in PROJECTS:
        if name not in projects:
            print('missing project', name); ok = False
    for prompt in PROMPTS:
        if prompt['name'] not in prompts:
            print('missing prompt', prompt['name']); ok = False
    for dataset in DATASETS:
        if dataset['name'] not in datasets:
            print('missing dataset', dataset['name']); ok = False
    for cfg in ANNOTATION_CONFIGS:
        if cfg['name'] not in configs:
            print('missing annotation config', cfg['name']); ok = False
    return ok
```

- [ ] **Step 2: Create eval smoke script**

Create `scripts/phoenix_eval_smoke.py` that:
1. reads `config.yaml`,
2. selects the `subcorp` key,
3. calls `/v1/embeddings` through broker for the `smoke-tests` dataset first example,
4. creates an experiment run under `smoke-tests-baseline-evaluators`,
5. writes a code evaluation `quality=1.0` if broker returned 200.

Use Phoenix endpoints from live OpenAPI:
- `GET /v1/datasets`
- `GET /v1/datasets/{id}/examples`
- `GET /v1/datasets/{dataset_id}/experiments`
- `POST /v1/experiments/{experiment_id}/runs`
- `POST /v1/experiment_evaluations`

- [ ] **Step 3: Document evals**

Create `docs/operations/evals.md`:

```markdown
# Phoenix Evaluations

## Bootstrap managed objects

```bash
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006 --verify
```

## Run smoke eval

```bash
python3 scripts/phoenix_eval_smoke.py --base-url http://10.0.0.200:6006 --broker-url http://127.0.0.1:11434
```

The smoke eval writes a code evaluation to the Phoenix experiment for `smoke-tests`.
```

- [ ] **Step 4: Verify eval path**

Run:

```bash
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006 --verify
python3 scripts/phoenix_eval_smoke.py --base-url http://10.0.0.200:6006 --broker-url http://127.0.0.1:11434
```

Expected: both exit 0 and Phoenix experiment run count increments.

---

## Task 10: Final verification and handoff

**Files:**
- Modify: `README.md`
- Optionally create: `CONTEXT.md` update if this repo uses it for current state.

- [ ] **Step 1: Run complete checks**

Run:

```bash
make verify
docker compose --profile db config --quiet
python3 scripts/phoenix_bootstrap.py --base-url http://10.0.0.200:6006 --verify
python3 scripts/send_test_alert.py
curl -fsS http://127.0.0.1:11434/broker/status | python3 -m json.tool | sed -n '1,80p'
```

Expected: all pass.

- [ ] **Step 2: Verify live DB and Phoenix**

Run:

```bash
PGPASSWORD=line psql 'postgres://llama@127.0.0.1:5432/llama_line?sslmode=disable' -c \
  "select count(*) app_logs from broker.app_log; select count(*) request_events from broker.request_event; select count(*) embeddings from broker.request_embedding;"
python3 - <<'PY'
import json, urllib.request, urllib.parse
base='http://10.0.0.200:6006'
for name in ['llama-line','subcorp','edda','jury-rigged','augr','paqr']:
    obj=json.load(urllib.request.urlopen(base+f'/v1/projects/{urllib.parse.quote(name)}/traces?limit=1'))
    print(name, len(obj.get('data', [])))
PY
```

Expected: DB counts are readable; Phoenix project queries return without error.

- [ ] **Step 3: Inspect intended git diff**

Run:

```bash
/usr/bin/git status --short
/usr/bin/git diff --stat
```

Expected: only intended files are modified/untracked. No secrets are introduced beyond existing local-only `config.yaml`.

- [ ] **Step 4: Commit only if explicitly requested**

Do not commit automatically. If user asks to commit, first run:

```bash
/usr/bin/git status --short
/usr/bin/git diff
/usr/bin/git log --oneline -10
```

Then stage only intended files, excluding live secrets in `config.yaml` unless user explicitly accepts that risk.

---

## Execution order recommendation

1. Task 1 — freeze state.
2. Task 2 — DB compose reproducibility.
3. Task 3 — systemd runtime.
4. Task 4 — DB backups.
5. Task 5 — config/repo hygiene.
6. Task 6 — Phoenix backup/auth docs.
7. Task 7 — alert route polish.
8. Task 8 — per-client Phoenix routing.
9. Task 9 — eval automation.
10. Task 10 — final verification.

## Self-review

- Spec coverage: stabilizing runtime, repo cleanup, Phoenix hardening, better alerts, per-client Phoenix projects, and eval automation are each covered by dedicated tasks.
- Placeholder scan: no `TBD` placeholders; every task has concrete files and commands. Task 8 includes exact interface/code shape for the highest-risk routing work.
- Type consistency: `PhoenixConfig.ProjectName`, `PhoenixConfig.ProjectMode`, `tracer.Manager`, `TracerForClient`, and `SetTracerResolver` names are consistent across tasks.
- Risk notes: `config.yaml` secrets, Phoenix LAN unauth, Phoenix SQLite backup, DB body privacy, systemd replacing manual process, and per-client tracer provider lifecycle are called out.
