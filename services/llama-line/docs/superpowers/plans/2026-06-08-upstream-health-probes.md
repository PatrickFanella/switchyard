# Upstream Health Probes Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Add cached broker/upstream health tracking so `/broker/status` and the web UI can report real upstream `up`, `down`, or `unknown` state.

**Architecture:** Each upstream gets a background probe loop that periodically calls a lightweight configured health path on that upstream, caches the result in `upstreamEntry`, and exposes that cached state through `/broker/status`. The status handler must never probe synchronously; UI polling should stay cheap and non-blocking.

**Tech Stack:** Go HTTP server, `net/http`, existing `proxy.Forwarder` outbound-auth handling, YAML config via `gopkg.in/yaml.v3`, React/Vite/TypeScript frontend, existing `make verify` workflow.

---

## File Structure

- Modify `internal/config/config.go`
  - Add top-level probe interval/timeout fields.
  - Add per-upstream `health_path` override.
- Modify `internal/config/load.go`
  - Add defaults and env overrides for probe interval/timeout.
- Modify `internal/config/load_test.go`
  - Cover defaults, YAML overrides, env overrides, and per-upstream health path load.
- Modify `internal/proxy/forwarder.go`
  - Add a reusable health probe method that applies outbound upstream auth.
- Modify `internal/proxy/forwarder_test.go` and/or `internal/proxy/auth_test.go`
  - Cover healthy, unhealthy, timeout/error, and bearer-auth probe behavior.
- Modify `internal/server/server.go`
  - Store cached health state in each `upstreamEntry`.
  - Add `StartHealthProbes(ctx, interval, timeout time.Duration)`.
- Modify `internal/server/status.go`
  - Include cached probe fields in each upstream status object.
- Modify `internal/server/status_test.go`
  - Cover initial unknown state and healthy/unhealthy cached state exposure.
- Modify `cmd/app/main.go`
  - Start health probes after dispatchers are started.
- Modify `web/src/lib/api/types.ts`
  - Add probe metadata fields to `UpstreamStatus`.
- Modify `web/src/pages/Config.tsx` and `web/src/pages/Queue.tsx`
  - Display probe freshness/latency/error when available.
- Modify `config.example.yaml`, `README.md`, `docs/api.md`, and `docs/app.md`
  - Document config and `/broker/status` response additions.

---

### Task 1: Config Shape and Defaults

**Files:**
- Modify: `internal/config/config.go:33-44`, `internal/config/config.go:75-104`
- Modify: `internal/config/load.go:61-98`, `internal/config/load.go:108-154`
- Test: `internal/config/load_test.go`

- [ ] **Step 1: Write the failing config tests**

Add these tests to `internal/config/load_test.go`:

```go
func TestLoad_HealthProbeDefaults(t *testing.T) {
	path := writeTemp(t, "listen_addr: \":8080\"\nollama_url: \"http://localhost:11434\"\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UpstreamHealthProbeIntervalSecs != 15 {
		t.Errorf("default UpstreamHealthProbeIntervalSecs = %d, want 15", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 2 {
		t.Errorf("default UpstreamHealthProbeTimeoutSecs = %d, want 2", cfg.UpstreamHealthProbeTimeoutSecs)
	}
}

func TestLoad_HealthProbeYAML(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":8080"
ollama_url: "http://localhost:11434"
upstream_health_probe_interval_secs: 30
upstream_health_probe_timeout_secs: 4
upstreams:
  - name: ocq
    url: http://ocq.local:8088
    health_path: /healthz
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UpstreamHealthProbeIntervalSecs != 30 {
		t.Errorf("UpstreamHealthProbeIntervalSecs = %d, want 30", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 4 {
		t.Errorf("UpstreamHealthProbeTimeoutSecs = %d, want 4", cfg.UpstreamHealthProbeTimeoutSecs)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].HealthPath != "/healthz" {
		t.Fatalf("upstream health_path not loaded: %+v", cfg.Upstreams)
	}
}

func TestApplyEnv_HealthProbeOverridesYAML(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":8080"
ollama_url: "http://localhost:11434"
upstream_health_probe_interval_secs: 30
upstream_health_probe_timeout_secs: 4
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Setenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS", "45")
	t.Setenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS", "6")
	config.ApplyEnv(cfg)
	if cfg.UpstreamHealthProbeIntervalSecs != 45 {
		t.Errorf("UpstreamHealthProbeIntervalSecs = %d, want 45", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 6 {
		t.Errorf("UpstreamHealthProbeTimeoutSecs = %d, want 6", cfg.UpstreamHealthProbeTimeoutSecs)
	}
}
```

- [ ] **Step 2: Run config tests and verify they fail**

Run:

```bash
go test ./internal/config -run 'TestLoad_HealthProbe|TestApplyEnv_HealthProbe' -v
```

Expected: compile failure because `UpstreamHealthProbeIntervalSecs`, `UpstreamHealthProbeTimeoutSecs`, and `HealthPath` do not exist yet.

- [ ] **Step 3: Add config fields**

Update `internal/config/config.go`.

Add `HealthPath` to `UpstreamConfig`:

```go
// HealthPath is the path probed for cached health status. Empty uses /api/version.
type UpstreamConfig struct {
	Name          string             `yaml:"name"`
	URL           string             `yaml:"url"`
	Models        []string           `yaml:"models"`
	MaxQueueDepth int                `yaml:"max_queue_depth"`
	HealthPath    string             `yaml:"health_path"`
	Auth          UpstreamAuthConfig `yaml:"auth"`
}
```

Add top-level probe settings to `Config` near the other upstream/runtime fields:

```go
	UpstreamHealthProbeIntervalSecs int `yaml:"upstream_health_probe_interval_secs"`
	UpstreamHealthProbeTimeoutSecs  int `yaml:"upstream_health_probe_timeout_secs"`
```

- [ ] **Step 4: Add env overrides and defaults**

Update `internal/config/load.go`.

Add env overrides in `ApplyEnv` after `LLAMA_LINE_OLLAMA_RETRY_MAX_SECS`:

```go
	if v := os.Getenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.UpstreamHealthProbeIntervalSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.UpstreamHealthProbeTimeoutSecs = n
		}
	}
```

Add defaults in `applyDefaults` after `OllamaRetryMaxSecs`:

```go
	if cfg.UpstreamHealthProbeIntervalSecs == 0 {
		cfg.UpstreamHealthProbeIntervalSecs = 15
	}
	if cfg.UpstreamHealthProbeTimeoutSecs == 0 {
		cfg.UpstreamHealthProbeTimeoutSecs = 2
	}
```

- [ ] **Step 5: Run config tests and commit**

Run:

```bash
go test ./internal/config -v
```

Expected: PASS.

Commit:

```bash
git add internal/config/config.go internal/config/load.go internal/config/load_test.go
git commit -m "feat: add upstream health probe config"
```

---

### Task 2: Forwarder Health Probe

**Files:**
- Modify: `internal/proxy/forwarder.go`
- Test: `internal/proxy/forwarder_test.go`, `internal/proxy/auth_test.go`

- [ ] **Step 1: Write failing proxy tests**

Add these tests to `internal/proxy/forwarder_test.go`:

```go
func TestForwarderHealthCheck_HealthyOn2xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("path = %q, want /api/version", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"0.0.0-test"}`))
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "/api/version")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
	if result.Duration <= 0 {
		t.Fatalf("Duration = %s, want > 0", result.Duration)
	}
}

func TestForwarderHealthCheck_UnhealthyOn5xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`broken`))
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "/api/version")
	if result.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if !strings.Contains(result.Error, "status 500") {
		t.Fatalf("Error = %q, want status 500", result.Error)
	}
}

func TestForwarderHealthCheck_UsesDefaultPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("path = %q, want /api/version", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	result := fwd.HealthCheck(context.Background(), "")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}
```

Add this auth-specific test to `internal/proxy/auth_test.go`:

```go
func TestForwarderHealthCheck_AppliesOutboundBearerAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			t.Fatalf("Authorization = %q, want Bearer upstream-secret", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	fwd := proxy.New(upstream.URL, 1, testLogger())
	fwd.SetOutboundAuth(config.UpstreamAuthConfig{Type: "bearer", Token: "upstream-secret"})
	result := fwd.HealthCheck(context.Background(), "/healthz")
	if !result.Healthy {
		t.Fatalf("Healthy = false, error = %q", result.Error)
	}
}
```

Ensure imports include `context`, `net/http`, `net/http/httptest`, `strings`, `git.subcult.tv/PatrickFanella/llama-line/internal/config`, and `git.subcult.tv/PatrickFanella/llama-line/internal/proxy` as needed by each file.

- [ ] **Step 2: Run proxy tests and verify they fail**

Run:

```bash
go test ./internal/proxy -run 'HealthCheck' -v
```

Expected: compile failure because `HealthCheck` and `HealthCheckResult` do not exist.

- [ ] **Step 3: Add health probe result and method**

Update `internal/proxy/forwarder.go`.

Add this type after `Forwarder`:

```go
// HealthCheckResult is the cached-safe result of probing an upstream.
type HealthCheckResult struct {
	Healthy  bool
	CheckedAt time.Time
	Duration time.Duration
	Error    string
}
```

Add this method near `Forward`:

```go
// HealthCheck probes the upstream with a lightweight GET request.
func (f *Forwarder) HealthCheck(ctx context.Context, healthPath string) HealthCheckResult {
	if healthPath == "" {
		healthPath = "/api/version"
	}
	if !strings.HasPrefix(healthPath, "/") {
		healthPath = "/" + healthPath
	}

	orig := &url.URL{Path: healthPath}
	target, err := buildTargetURL(f.OllamaURL, orig)
	checkedAt := time.Now()
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Error: fmt.Sprintf("build URL: %v", err)}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Error: fmt.Sprintf("build request: %v", err)}
	}
	f.applyOutboundAuth(req)

	start := time.Now()
	resp, err := f.client.Do(req)
	duration := time.Since(start)
	if err != nil {
		return HealthCheckResult{CheckedAt: checkedAt, Duration: duration, Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return HealthCheckResult{CheckedAt: checkedAt, Duration: duration, Error: fmt.Sprintf("status %d", resp.StatusCode)}
	}
	return HealthCheckResult{Healthy: true, CheckedAt: checkedAt, Duration: duration}
}
```

- [ ] **Step 4: Run proxy tests and commit**

Run:

```bash
go test ./internal/proxy -v
```

Expected: PASS.

Commit:

```bash
git add internal/proxy/forwarder.go internal/proxy/forwarder_test.go internal/proxy/auth_test.go
git commit -m "feat: add upstream health probe request"
```

---

### Task 3: Server Probe Loop and Status Contract

**Files:**
- Modify: `internal/server/server.go:26-32`, `internal/server/server.go:151-159`
- Modify: `internal/server/status.go:18-32`, `internal/server/status.go:86-100`
- Test: `internal/server/status_test.go`

- [ ] **Step 1: Write failing server status tests**

Add helper response structs and tests to `internal/server/status_test.go`:

```go
type healthStatusUpstream struct {
	Name                string `json:"name"`
	Healthy             *bool  `json:"healthy,omitempty"`
	LastProbeAt         string `json:"last_probe_at,omitempty"`
	LastProbeMS         int64  `json:"last_probe_ms,omitempty"`
	LastProbeError      string `json:"last_probe_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
}

type healthStatusResponse struct {
	Upstreams []healthStatusUpstream `json:"upstreams"`
}

func decodeHealthStatus(t *testing.T, srv *server.Server) healthStatusResponse {
	t.Helper()
	r := httptest.NewRequest("GET", "/broker/status", nil)
	w := httptest.NewRecorder()
	srv.HandleBrokerStatus(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp healthStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode broker status: %v", err)
	}
	return resp
}

func TestHandleBrokerStatus_HealthUnknownBeforeProbe(t *testing.T) {
	srv := newTestServer()
	resp := decodeHealthStatus(t, srv)
	if len(resp.Upstreams) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(resp.Upstreams))
	}
	if resp.Upstreams[0].Healthy != nil {
		t.Fatalf("healthy = %v, want omitted before first probe", *resp.Upstreams[0].Healthy)
	}
}

func TestStartHealthProbes_ReportsHealthyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Fatalf("path = %q, want /api/version", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		ListenAddr:                         ":0",
		OllamaURL:                          upstream.URL,
		MaxQueueDepth:                      10,
		HeartbeatIntervalSecs:              5,
		UpstreamHealthProbeIntervalSecs:    1,
		UpstreamHealthProbeTimeoutSecs:     1,
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(upstream.URL, 1, logger.New("error"))
	srv := server.New(cfg, q, fwd, logger.New("error"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartHealthProbes(ctx, 10*time.Millisecond, 500*time.Millisecond)

	waitForCondition(t, time.Second, func() bool {
		resp := decodeHealthStatus(t, srv)
		return len(resp.Upstreams) == 1 && resp.Upstreams[0].Healthy != nil && *resp.Upstreams[0].Healthy
	})
	resp := decodeHealthStatus(t, srv)
	up := resp.Upstreams[0]
	if up.LastProbeAt == "" {
		t.Fatal("last_probe_at is empty")
	}
	if up.LastProbeMS < 0 {
		t.Fatalf("last_probe_ms = %d, want >= 0", up.LastProbeMS)
	}
}

func TestStartHealthProbes_ReportsUnhealthyUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		ListenAddr:                         ":0",
		Upstreams: []config.UpstreamConfig{{
			Name:       "broken",
			URL:        upstream.URL,
			HealthPath: "/healthz",
		}},
		MaxQueueDepth:                      10,
		HeartbeatIntervalSecs:              5,
		UpstreamHealthProbeIntervalSecs:    1,
		UpstreamHealthProbeTimeoutSecs:     1,
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(upstream.URL, 1, logger.New("error"))
	srv := server.New(cfg, q, fwd, logger.New("error"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.StartHealthProbes(ctx, 10*time.Millisecond, 500*time.Millisecond)

	waitForCondition(t, time.Second, func() bool {
		resp := decodeHealthStatus(t, srv)
		return len(resp.Upstreams) == 1 && resp.Upstreams[0].Healthy != nil && !*resp.Upstreams[0].Healthy
	})
	resp := decodeHealthStatus(t, srv)
	up := resp.Upstreams[0]
	if up.LastProbeError == "" {
		t.Fatal("last_probe_error is empty")
	}
	if up.ConsecutiveFailures == 0 {
		t.Fatal("consecutive_failures = 0, want > 0")
	}
}
```

Ensure `internal/server/status_test.go` imports `context`, `time`, `net/http/httptest`, `git.subcult.tv/PatrickFanella/llama-line/internal/proxy`, and `git.subcult.tv/PatrickFanella/llama-line/internal/queue` if not already present.

- [ ] **Step 2: Run server tests and verify they fail**

Run:

```bash
go test ./internal/server -run 'Health|BrokerStatus' -v
```

Expected: compile failure because `StartHealthProbes` and the status fields do not exist.

- [ ] **Step 3: Add cached health state to upstream entries**

Update `internal/server/server.go`.

Add this type near `upstreamEntry`:

```go
type upstreamHealthState struct {
	known               bool
	healthy             bool
	lastProbeAt         time.Time
	lastProbeDuration   time.Duration
	lastProbeError      string
	consecutiveFailures int
}
```

Extend `upstreamEntry`:

```go
type upstreamEntry struct {
	cfg        config.UpstreamConfig
	queue      *queue.Queue
	dispatcher *queue.Dispatcher
	forwarder  *proxy.Forwarder
	healthMu   sync.RWMutex
	health     upstreamHealthState
}
```

Add these methods near `StartDispatchers`:

```go
func (e *upstreamEntry) setHealth(result proxy.HealthCheckResult) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	e.health.known = true
	e.health.healthy = result.Healthy
	e.health.lastProbeAt = result.CheckedAt
	e.health.lastProbeDuration = result.Duration
	e.health.lastProbeError = result.Error
	if result.Healthy {
		e.health.consecutiveFailures = 0
	} else {
		e.health.consecutiveFailures++
	}
}

func (e *upstreamEntry) healthSnapshot() upstreamHealthState {
	e.healthMu.RLock()
	defer e.healthMu.RUnlock()
	return e.health
}

// StartHealthProbes starts one cached health probe loop per upstream.
func (s *Server) StartHealthProbes(ctx context.Context, interval, timeout time.Duration) {
	if interval <= 0 || timeout <= 0 {
		return
	}
	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		go func() {
			runProbe := func() {
				probeCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				e.setHealth(e.forwarder.HealthCheck(probeCtx, e.cfg.HealthPath))
			}
			runProbe()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					runProbe()
				}
			}
		}()
	}
}
```

- [ ] **Step 4: Expose cached fields in `/broker/status`**

Update `internal/server/status.go`.

Extend `brokerStatusUpstream`:

```go
	Healthy             *bool     `json:"healthy,omitempty"`
	LastProbeAt         time.Time `json:"last_probe_at,omitempty"`
	LastProbeMS         int64     `json:"last_probe_ms,omitempty"`
	LastProbeError      string    `json:"last_probe_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
```

Inside the upstream loop, before appending `brokerStatusUpstream`, add:

```go
		health := e.healthSnapshot()
		var healthy *bool
		if health.known {
			healthyValue := health.healthy
			healthy = &healthyValue
		}
```

Add these fields to the appended `brokerStatusUpstream` literal:

```go
			Healthy:             healthy,
			LastProbeAt:         health.lastProbeAt,
			LastProbeMS:         health.lastProbeDuration.Milliseconds(),
			LastProbeError:      health.lastProbeError,
			ConsecutiveFailures: health.consecutiveFailures,
```

- [ ] **Step 5: Run server tests and commit**

Run:

```bash
go test ./internal/server -run 'Health|BrokerStatus' -v
go test ./internal/server -v
```

Expected: PASS.

Commit:

```bash
git add internal/server/server.go internal/server/status.go internal/server/status_test.go
git commit -m "feat: track upstream health in broker status"
```

---

### Task 4: Wire Runtime Startup

**Files:**
- Modify: `cmd/app/main.go:84-150`
- Test: `go test ./cmd/app -v`

- [ ] **Step 1: Wire probe durations in main**

Update `cmd/app/main.go` near the existing request timeout setup:

```go
	requestTimeout := time.Duration(cfg.RequestTimeoutSecs) * time.Second
	queueWaitTimeout := time.Duration(cfg.QueueWaitTimeoutSecs) * time.Second
	healthProbeInterval := time.Duration(cfg.UpstreamHealthProbeIntervalSecs) * time.Second
	healthProbeTimeout := time.Duration(cfg.UpstreamHealthProbeTimeoutSecs) * time.Second
```

Start health probes immediately after dispatchers:

```go
	// Start one dispatcher goroutine per upstream.
	srv.StartDispatchers(ctx, requestTimeout, queueWaitTimeout)
	srv.StartHealthProbes(ctx, healthProbeInterval, healthProbeTimeout)
```

- [ ] **Step 2: Run app tests and commit**

Run:

```bash
go test ./cmd/app -v
```

Expected: PASS.

Commit:

```bash
git add cmd/app/main.go
git commit -m "feat: start upstream health probes"
```

---

### Task 5: Frontend Status Display

**Files:**
- Modify: `web/src/lib/api/types.ts:21-36`
- Modify: `web/src/pages/Config.tsx`
- Modify: `web/src/pages/Queue.tsx`
- Test: `cd web && pnpm build && pnpm lint`

- [ ] **Step 1: Update TypeScript contract**

Update `UpstreamStatus` in `web/src/lib/api/types.ts`:

```ts
export interface UpstreamStatus {
  name: string
  url: string
  healthy?: boolean
  last_probe_at?: string
  last_probe_ms?: number
  last_probe_error?: string
  consecutive_failures?: number
  models?: string[]
  queue_depth: number
  max_depth: number
  in_flight_client?: string
  in_flight_id?: string
  in_flight_priority?: number
  in_flight_enqueued_at?: string
  in_flight_started_at?: string
  in_flight_job_seconds?: number
  in_flight_total_seconds?: number
  queue: QueueItem[]
}
```

- [ ] **Step 2: Add reusable probe summary helpers in `Config.tsx`**

Update `web/src/pages/Config.tsx` to import the type:

```ts
import type { UpstreamStatus } from '@/lib/api/types'
```

Replace `UpstreamHealthBadge` with:

```tsx
function UpstreamHealthBadge({ healthy }: { healthy?: boolean }) {
  if (healthy === undefined) {
    return <Badge variant="neutral">unknown</Badge>
  }
  return <Badge variant={healthy ? 'success' : 'error'}>{healthy ? 'up' : 'down'}</Badge>
}

function ProbeSummary({ upstream }: { upstream: UpstreamStatus }) {
  if (!upstream.last_probe_at) {
    return <span className="text-[var(--color-muted)]">not checked yet</span>
  }
  const checkedAt = new Date(upstream.last_probe_at)
  const checked = Number.isNaN(checkedAt.getTime()) ? upstream.last_probe_at : checkedAt.toLocaleTimeString()
  const latency = upstream.last_probe_ms !== undefined ? `${upstream.last_probe_ms}ms` : '—'
  if (upstream.last_probe_error) {
    return <span className="text-[var(--color-error)]">{checked} · {latency} · {upstream.last_probe_error}</span>
  }
  return <span className="text-[var(--color-muted)]">{checked} · {latency}</span>
}
```

Add a fourth table cell in the upstreams table after the health badge:

```tsx
<td className="px-3 py-2 text-xs"><ProbeSummary upstream={u} /></td>
```

- [ ] **Step 3: Add probe metadata to queue upstream cards**

In `web/src/pages/Queue.tsx`, add this helper near `Metric`:

```tsx
function probeSummary(upstream: UpstreamStatus) {
  if (!upstream.last_probe_at) return 'health not checked yet'
  const checkedAt = new Date(upstream.last_probe_at)
  const checked = Number.isNaN(checkedAt.getTime()) ? upstream.last_probe_at : checkedAt.toLocaleTimeString()
  const latency = upstream.last_probe_ms !== undefined ? `${upstream.last_probe_ms}ms` : '—'
  if (upstream.last_probe_error) return `${checked} · ${latency} · ${upstream.last_probe_error}`
  return `${checked} · ${latency}`
}
```

Then add this line under the URL in `UpstreamQueueCard`:

```tsx
<span className="font-mono text-xs text-[var(--color-muted)]">{probeSummary(upstream)}</span>
```

- [ ] **Step 4: Build frontend and commit**

Run:

```bash
cd web && pnpm build && pnpm lint
```

Expected: PASS.

Commit:

```bash
git add web/src/lib/api/types.ts web/src/pages/Config.tsx web/src/pages/Queue.tsx
git commit -m "feat: show upstream health probe metadata"
```

---

### Task 6: Docs and Examples

**Files:**
- Modify: `config.example.yaml`
- Modify: `README.md`
- Modify: `docs/api.md`
- Modify: `docs/app.md`

- [ ] **Step 1: Update `config.example.yaml`**

Add after `ollama_retry_max_secs`:

```yaml
# Upstream health probes shown in /broker/status and the web UI.
# Probes are cached in the background; /broker/status never blocks on probing.
# Default path is /api/version unless an upstream sets health_path.
upstream_health_probe_interval_secs: 15
upstream_health_probe_timeout_secs: 2
```

Add to the OCQ example upstream block:

```yaml
#     health_path: /healthz  # override when an upstream is not Ollama-compatible
```

- [ ] **Step 2: Update `README.md` config table and example**

Add rows after `ollama_retry_max_secs`:

```markdown
| `upstream_health_probe_interval_secs` | int | `LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS` | `15` | Seconds between background upstream health probes |
| `upstream_health_probe_timeout_secs` | int | `LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS` | `2` | Per-probe timeout in seconds |
```

Add to the example YAML near `ollama_retry_max_secs`:

```yaml
upstream_health_probe_interval_secs: 15
upstream_health_probe_timeout_secs: 2
```

Add this sentence in the routing/upstreams section:

```markdown
Each upstream can set `health_path` to override the default `/api/version` probe path; use this for non-Ollama-compatible gateways such as custom OpenAI-compatible proxies.
```

- [ ] **Step 3: Update `docs/api.md` broker status example**

Add these fields to each upstream object in the `/broker/status` example:

```json
      "healthy": true,
      "last_probe_at": "2026-06-08T12:00:00Z",
      "last_probe_ms": 12,
      "last_probe_error": "",
      "consecutive_failures": 0,
```

Add this note below the example:

```markdown
`healthy` is omitted until the first background probe completes. When an upstream probe fails, `healthy` is `false`, `last_probe_error` contains the latest failure summary, and `consecutive_failures` increments until a successful probe resets it to `0`.
```

- [ ] **Step 4: Update `docs/app.md`**

Add an operator note in the configuration/status section:

```markdown
### Upstream health probes

llama-line probes each configured upstream in the background and caches the result for `/broker/status` and the web UI. The default probe path is `/api/version`, which matches Ollama. For non-Ollama-compatible upstreams, configure `health_path` on that upstream:

```yaml
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    health_path: /healthz
```

Tune probe cadence globally with `upstream_health_probe_interval_secs` and `upstream_health_probe_timeout_secs`.
```

- [ ] **Step 5: Commit docs**

Run:

```bash
git diff --check
```

Expected: no whitespace errors.

Commit:

```bash
git add config.example.yaml README.md docs/api.md docs/app.md
git commit -m "docs: document upstream health probes"
```

---

### Task 7: Full Verification

**Files:**
- Verify all modified files.

- [ ] **Step 1: Run frontend build and lint**

Run:

```bash
cd web && pnpm build && pnpm lint
```

Expected: PASS.

- [ ] **Step 2: Run repository verification**

Run:

```bash
make verify
```

Expected: PASS.

- [ ] **Step 3: Manual smoke test with a healthy upstream**

Run llama-line against a running Ollama-compatible upstream, then query status:

```bash
go run ./cmd/app --config config.example.yaml
curl -s http://127.0.0.1:11434/broker/status | jq '.upstreams[] | {name, healthy, last_probe_at, last_probe_ms, last_probe_error, consecutive_failures}'
```

Expected for reachable upstream:

```json
{
  "name": "default",
  "healthy": true,
  "last_probe_at": "<recent RFC3339 timestamp>",
  "last_probe_ms": 0,
  "last_probe_error": "",
  "consecutive_failures": 0
}
```

- [ ] **Step 4: Manual smoke test with an unreachable upstream**

Temporarily set the upstream URL to an unused local port, start llama-line, then query status:

```bash
curl -s http://127.0.0.1:11434/broker/status | jq '.upstreams[] | {name, healthy, last_probe_error, consecutive_failures}'
```

Expected:

```json
{
  "name": "default",
  "healthy": false,
  "last_probe_error": "<connection or timeout error>",
  "consecutive_failures": 1
}
```

- [ ] **Step 5: Final commit if verification required changes**

If verification revealed fixes, commit them:

```bash
git add <changed-files>
git commit -m "fix: stabilize upstream health probes"
```

---

## Acceptance Criteria

- `/broker/status` includes `healthy` for each upstream after its first background probe.
- `healthy` is omitted before the first probe, so the UI can show `unknown` instead of false `down`.
- Failed probes set `healthy: false`, populate `last_probe_error`, and increment `consecutive_failures`.
- Successful probes set `healthy: true`, clear `last_probe_error`, and reset `consecutive_failures` to `0`.
- `/broker/status` does not perform network I/O and remains fast.
- Per-upstream bearer auth is applied to probe requests.
- Non-Ollama-compatible upstreams can set `health_path`.
- `make verify` passes.

## Design Notes

- Do not make `/broker/status` probe on demand; that couples UI refresh latency to upstream network latency.
- Use pointer bool (`*bool`) in the JSON response so “known down” is distinguishable from “not checked yet”.
- Keep probe bodies small and discard them; health depends on status code only.
- Default `/api/version` is Ollama-compatible. Configure `health_path` for OCQ or OpenAI-compatible gateways if they do not support that endpoint.
- If a future need arises to disable probes entirely, add an explicit `upstream_health_probe_enabled` setting later. Do not overload `interval_secs: 0` while defaults also use zero-value detection.
