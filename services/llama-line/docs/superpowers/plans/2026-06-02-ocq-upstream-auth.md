# OCQ Upstream Auth Implementation Plan

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Route `opencode*` chat-completion requests from llama-line to an OCQ gateway hosted on almaz, with outbound per-upstream bearer auth and session metadata preserved.

**Architecture:** Keep OCQ as a normal HTTP upstream. Add optional outbound auth fields to each `upstreams[]` entry, inject that auth in `proxy.Forwarder`, and let the existing model router/queue/dispatcher send `opencode*` models to OCQ. Do not implement native OpenCode session management in llama-line for this path.

**Tech Stack:** Go 1.21+, `net/http`, YAML config via `gopkg.in/yaml.v3`, existing llama-line queue/router/proxy/server packages, OCQ OpenAI-compatible `/v1/chat/completions` endpoint.

---

## File Structure

- Modify `internal/config/config.go`: add outbound auth config structs to `UpstreamConfig`.
- Modify `internal/config/load.go`: resolve `token_env` into in-memory token and validate auth types.
- Modify `internal/config/load_test.go`: cover YAML parsing and env token resolution.
- Modify `internal/proxy/forwarder.go`: add optional outbound auth and set it on forwarded upstream requests.
- Add `internal/proxy/auth_test.go`: test outbound auth behavior without server routing noise.
- Modify `internal/server/server.go`: construct forwarders with per-upstream auth.
- Modify `internal/server/handler_test.go`: add routing test proving `opencode*` reaches OCQ upstream with bearer auth and returns `x-ocq-session`.
- Modify `config.example.yaml`: document `ocq-almaz` upstream.
- Modify `README.md`: document the OCQ-on-almaz path.

## Invariants

- Incoming client `Authorization: Bearer <llama-line-key>` must never be forwarded upstream.
- Outbound upstream auth is configured per upstream only.
- OCQ remains responsible for OpenCode sessions. llama-line only preserves request body and proxied response headers.
- Existing Ollama routes continue unchanged.
- `make verify` remains final verification.

---

### Task 1: Add upstream outbound auth config

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/load.go`
- Test: `internal/config/load_test.go`

- [ ] **Step 1: Write failing config tests**

Append these tests to `internal/config/load_test.go`:

```go
func TestLoad_ParsesUpstreamAuth(t *testing.T) {
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "opencode*"
    auth:
      type: bearer
      token: literal-token
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Upstreams[0].Auth.Type; got != "bearer" {
		t.Fatalf("auth type = %q, want bearer", got)
	}
	if got := cfg.Upstreams[0].Auth.Token; got != "literal-token" {
		t.Fatalf("auth token = %q, want literal-token", got)
	}
}

func TestLoad_ResolvesUpstreamAuthTokenEnv(t *testing.T) {
	t.Setenv("OCQ_GATEWAY_KEY", "env-token")
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "opencode*"
    auth:
      type: bearer
      token_env: OCQ_GATEWAY_KEY
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Upstreams[0].Auth.Token; got != "env-token" {
		t.Fatalf("resolved auth token = %q, want env-token", got)
	}
	if got := cfg.Upstreams[0].Auth.TokenEnv; got != "OCQ_GATEWAY_KEY" {
		t.Fatalf("token env = %q, want OCQ_GATEWAY_KEY", got)
	}
}

func TestLoad_RejectsUnknownUpstreamAuthType(t *testing.T) {
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: bad
    url: http://127.0.0.1:8088
    auth:
      type: digest
      token: nope
`)

	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "upstreams[0].auth.type") {
		t.Fatalf("Load() error = %v, want auth type validation error", err)
	}
}
```

If `strings` is not imported in `internal/config/load_test.go`, add it to the import block.

- [ ] **Step 2: Run config tests and verify failure**

Run:

```bash
go test ./internal/config
```

Expected: compile failure or test failure because `UpstreamConfig.Auth` does not exist.

- [ ] **Step 3: Add config structs**

In `internal/config/config.go`, replace the existing `UpstreamConfig` definition with:

```go
// UpstreamAuthConfig describes credentials llama-line adds when forwarding to an upstream.
// Type may be empty/"none" for no auth or "bearer" for Authorization: Bearer <token>.
// TokenEnv, when set, is resolved during config load and takes precedence over Token.
type UpstreamAuthConfig struct {
	Type     string `yaml:"type"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
}

// UpstreamConfig describes one upstream model-serving endpoint.
// Models is a list of exact model names or glob patterns (e.g. "llama3*", "opencode*").
// Requests whose model matches any pattern are routed to this upstream.
// An upstream with an empty Models list is the fallback for unmatched models.
// MaxQueueDepth overrides the global max_queue_depth for this upstream (0 = use global).
// Auth optionally adds outbound credentials to forwarded upstream requests.
type UpstreamConfig struct {
	Name          string             `yaml:"name"`
	URL           string             `yaml:"url"`
	Models        []string           `yaml:"models"`
	MaxQueueDepth int                `yaml:"max_queue_depth"`
	Auth          UpstreamAuthConfig `yaml:"auth"`
}
```

- [ ] **Step 4: Resolve and validate auth config**

In `internal/config/load.go`, add `strings` to imports and add this helper after `Load`:

```go
func resolveUpstreamAuth(cfg *Config) error {
	for i := range cfg.Upstreams {
		auth := &cfg.Upstreams[i].Auth
		auth.Type = strings.ToLower(strings.TrimSpace(auth.Type))
		switch auth.Type {
		case "", "none":
			auth.Type = ""
		case "bearer":
			if auth.TokenEnv != "" {
				auth.Token = os.Getenv(auth.TokenEnv)
			}
		default:
			return fmt.Errorf("config: upstreams[%d].auth.type must be bearer or none", i)
		}
	}
	return nil
}
```

Then in `Load`, immediately after `applyDefaults(&cfg)`, add:

```go
	if err := resolveUpstreamAuth(&cfg); err != nil {
		return nil, err
	}
```

- [ ] **Step 5: Run config tests and verify pass**

Run:

```bash
go test ./internal/config
```

Expected: PASS.

- [ ] **Step 6: Commit config change**

Run:

```bash
git add internal/config/config.go internal/config/load.go internal/config/load_test.go
git commit -m "feat: add upstream outbound auth config"
```

---

### Task 2: Inject per-upstream bearer auth in proxy forwarder

**Files:**
- Modify: `internal/proxy/forwarder.go`
- Add: `internal/proxy/auth_test.go`

- [ ] **Step 1: Write failing proxy tests**

Create `internal/proxy/auth_test.go`:

```go
package proxy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
)

func TestForwarder_ReplacesIncomingAuthorizationWithConfiguredBearer(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	fwd := New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fwd.SetOutboundAuth(config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"})

	req, err := http.NewRequest(http.MethodPost, "http://broker.test/v1/chat/completions", strings.NewReader(`{"model":"opencode"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer llama-line-client-key")

	result := fwd.Forward(context.Background(), &queue.Request{HTTPReq: req, StatusCh: make(chan queue.StatusUpdate, 1)})
	if result.Err != nil {
		t.Fatalf("Forward() error = %v", result.Err)
	}
	defer result.Resp.Body.Close()

	if gotAuth != "Bearer ocq-secret" {
		t.Fatalf("Authorization = %q, want Bearer ocq-secret", gotAuth)
	}
}

func TestForwarder_DropsIncomingAuthorizationWhenNoOutboundAuth(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	fwd := New(upstream.URL, 1, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, err := http.NewRequest(http.MethodPost, "http://broker.test/v1/chat/completions", strings.NewReader(`{"model":"qwen"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer llama-line-client-key")

	result := fwd.Forward(context.Background(), &queue.Request{HTTPReq: req, StatusCh: make(chan queue.StatusUpdate, 1)})
	if result.Err != nil {
		t.Fatalf("Forward() error = %v", result.Err)
	}
	defer result.Resp.Body.Close()

	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
}
```

- [ ] **Step 2: Run proxy tests and verify failure**

Run:

```bash
go test ./internal/proxy
```

Expected: compile failure because `SetOutboundAuth` does not exist.

- [ ] **Step 3: Implement outbound auth on `Forwarder`**

In `internal/proxy/forwarder.go`, add this import:

```go
	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
```

Add this field to `Forwarder`:

```go
	outboundAuth config.UpstreamAuthConfig
```

Add this method after `New`:

```go
// SetOutboundAuth configures credentials added to requests sent to this upstream.
func (f *Forwarder) SetOutboundAuth(auth config.UpstreamAuthConfig) {
	f.outboundAuth = auth
}
```

Add this helper near the bottom of the file:

```go
func (f *Forwarder) applyOutboundAuth(req *http.Request) {
	switch f.outboundAuth.Type {
	case "bearer":
		if f.outboundAuth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+f.outboundAuth.Token)
		}
	}
}
```

In `Forward`, immediately after the header-copy loop, add:

```go
		f.applyOutboundAuth(req)
```

The resulting section should be:

```go
		for k, vv := range r.HTTPReq.Header {
			if k == "Authorization" {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		f.applyOutboundAuth(req)
```

- [ ] **Step 4: Run proxy tests and verify pass**

Run:

```bash
go test ./internal/proxy
```

Expected: PASS.

- [ ] **Step 5: Commit proxy auth change**

Run:

```bash
git add internal/proxy/forwarder.go internal/proxy/auth_test.go
git commit -m "feat: inject upstream bearer auth"
```

---

### Task 3: Wire upstream auth into server construction

**Files:**
- Modify: `internal/server/server.go`
- Test: `internal/server/handler_test.go`

- [ ] **Step 1: Write failing OCQ routing test**

Append this test to `internal/server/handler_test.go`:

```go
func TestHandlerInference_RoutesOpenCodeModelToOCQWithOutboundAuth(t *testing.T) {
	var gotAuth string
	var gotBody string
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("OCQ path = %q, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ocq-session", "session-123")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from ocq"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(ocq.Close)

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("ollama upstream should not receive opencode model request")
	}))
	t.Cleanup(ollama.Close)

	cfg := testConfig(ollama.URL)
	cfg.Upstreams = []config.UpstreamConfig{
		{Name: "ollama", URL: ollama.URL},
		{Name: "ocq-almaz", URL: ocq.URL, Models: []string{"opencode*"}, Auth: config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"}},
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(ollama.URL, cfg.OllamaRetryMaxSecs, testLogger())
	srv := server.New(cfg, q, fwd, testLogger())

	mux := http.NewServeMux()
	mux.Handle("/", server.AuthMiddlewareFromKeys(cfg.Keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if server.IsInferenceEndpoint(r.URL.Path) {
			srv.HandleInference(w, r)
			return
		}
		srv.HandlePassThrough(w, r)
	})))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv.StartDispatchers(ctx, time.Duration(cfg.RequestTimeoutSecs)*time.Second, time.Duration(cfg.QueueWaitTimeoutSecs)*time.Second)
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	req := authedRequest(t, http.MethodPost, httpSrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"opencode-gpt","messages":[{"role":"user","content":"hi"}],"stream":false,"ocq_session_id":"keep-me"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if gotAuth != "Bearer ocq-secret" {
		t.Fatalf("OCQ Authorization = %q, want Bearer ocq-secret", gotAuth)
	}
	if !strings.Contains(gotBody, `"ocq_session_id":"keep-me"`) {
		t.Fatalf("OCQ body = %s, want ocq_session_id preserved", gotBody)
	}
	if !strings.Contains(string(body), "from ocq") {
		t.Fatalf("broker body = %s, want OCQ response", body)
	}
	if resp.Header.Get("x-ocq-session") != "session-123" {
		t.Fatalf("x-ocq-session = %q, want session-123", resp.Header.Get("x-ocq-session"))
	}
}
```

- [ ] **Step 2: Run server test and verify failure**

Run:

```bash
go test ./internal/server -run TestHandlerInference_RoutesOpenCodeModelToOCQWithOutboundAuth -count=1
```

Expected: failure because server-created upstream forwarders do not apply `Auth` yet.

- [ ] **Step 3: Apply auth when creating forwarders**

In `internal/server/server.go`, add this helper near `New`:

```go
func newForwarderForUpstream(u config.UpstreamConfig, retryMaxSecs int, log *slog.Logger) *proxy.Forwarder {
	fwd := proxy.New(u.URL, retryMaxSecs, log)
	fwd.SetOutboundAuth(u.Auth)
	return fwd
}
```

Then in `New`, before assigning upstream 0, call `f.SetOutboundAuth(resolved[0].Auth)` when `i == 0`:

```go
		if i == 0 {
			f.SetOutboundAuth(u.Auth)
			entries[i] = upstreamEntry{cfg: u, queue: q, forwarder: f}
		} else {
			entries[i] = upstreamEntry{
				cfg:       u,
				queue:     queue.New(maxDepth),
				forwarder: newForwarderForUpstream(u, cfg.OllamaRetryMaxSecs, log),
			}
		}
```

- [ ] **Step 4: Run targeted server test and verify pass**

Run:

```bash
go test ./internal/server -run TestHandlerInference_RoutesOpenCodeModelToOCQWithOutboundAuth -count=1
```

Expected: PASS.

- [ ] **Step 5: Run server package tests**

Run:

```bash
go test ./internal/server
```

Expected: PASS.

- [ ] **Step 6: Commit server wiring**

Run:

```bash
git add internal/server/server.go internal/server/handler_test.go
git commit -m "feat: route opencode models to authenticated ocq upstream"
```

---

### Task 4: Document OCQ-on-almaz config

**Files:**
- Modify: `config.example.yaml`
- Modify: `README.md`

- [ ] **Step 1: Update `config.example.yaml` upstream documentation**

In `config.example.yaml`, in the multi-upstream comment block above `upstreams: []`, add this example:

```yaml
# Example OCQ/OpenCode route hosted on almaz:
# upstreams:
#   - name: ollama
#     url: http://127.0.0.1:11435
#     # no models — catches normal Ollama requests
#   - name: ocq-almaz
#     url: http://almaz:8088
#     models:
#       - "opencode*"
#     auth:
#       type: bearer
#       token_env: OCQ_GATEWAY_KEY
#
# Requests with model "opencode*" are forwarded to OCQ's OpenAI-compatible
# /v1/chat/completions endpoint. OCQ owns OpenCode sessions; pass
# ocq_session_id in the JSON body to continue a session.
```

- [ ] **Step 2: Update `README.md`**

Add this section under the multi-upstream/routing documentation in `README.md`:

```markdown
### Routing OpenCode requests through OCQ

llama-line can route OpenAI-compatible chat requests to an OCQ gateway, for
example an OCQ instance hosted on `almaz` that talks to a local OpenCode server.
This keeps llama-line responsible for auth, queueing, model routing, and status
events while OCQ owns OpenCode session creation and continuation.

```yaml
upstreams:
  - name: ollama
    url: http://127.0.0.1:11435
    models: []
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "opencode*"
    auth:
      type: bearer
      token_env: OCQ_GATEWAY_KEY
```

Clients call llama-line normally with their llama-line bearer key:

```bash
curl -N http://localhost:11434/v1/chat/completions \
  -H "Authorization: Bearer $LLAMA_LINE_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"opencode-gpt",
    "messages":[{"role":"user","content":"Inspect this repo"}],
    "stream":false
  }'
```

To continue an OCQ/OpenCode session, include OCQ's session id in the body:

```json
{"model":"opencode-gpt","ocq_session_id":"session-id","messages":[{"role":"user","content":"Continue"}]}
```

The incoming llama-line `Authorization` header is never forwarded upstream.
When `auth.type: bearer` is configured, llama-line sends
`Authorization: Bearer $OCQ_GATEWAY_KEY` to OCQ.
```

If `README.md` already has a more specific routing section, place the section there rather than duplicating concepts.

- [ ] **Step 3: Run docs-adjacent verification**

Run:

```bash
go test ./internal/config ./internal/proxy ./internal/server
```

Expected: PASS.

- [ ] **Step 4: Commit docs**

Run:

```bash
git add config.example.yaml README.md
git commit -m "docs: show ocq upstream routing"
```

---

### Task 5: Full verification and cleanup

**Files:**
- Verify: entire repo

- [ ] **Step 1: Run gofmt**

Run:

```bash
gofmt -w internal/config/config.go internal/config/load.go internal/config/load_test.go internal/proxy/forwarder.go internal/proxy/auth_test.go internal/server/server.go internal/server/handler_test.go
```

Expected: no output.

- [ ] **Step 2: Run package tests**

Run:

```bash
go test ./internal/config ./internal/proxy ./internal/server
```

Expected: PASS.

- [ ] **Step 3: Run full Go tests**

Run:

```bash
go test ./...
```

Expected: PASS.

- [ ] **Step 4: Run repository verification**

Run:

```bash
make verify
```

Expected: PASS. If `web-build` dependency install fails due network, record the exact failure and still report Go/package test status.

- [ ] **Step 5: Inspect final diff**

Run:

```bash
git status --short && git diff --stat
```

Expected: only intended files changed.

- [ ] **Step 6: Final commit if prior tasks were not committed individually**

Run:

```bash
git add internal/config/config.go internal/config/load.go internal/config/load_test.go internal/proxy/forwarder.go internal/proxy/auth_test.go internal/server/server.go internal/server/handler_test.go config.example.yaml README.md
git commit -m "feat: support authenticated ocq upstreams"
```

Expected: commit created, unless task-level commits already covered all changes.

---

## Self-Review

- Spec coverage: plan implements OCQ-on-almaz as an HTTP upstream, outbound bearer auth, model routing, `ocq_session_id` body preservation, `x-ocq-session` header pass-through, docs, and verification.
- Placeholder scan: no TODO/TBD/implement-later placeholders remain.
- Type consistency: `config.UpstreamAuthConfig`, `UpstreamConfig.Auth`, `proxy.Forwarder.SetOutboundAuth`, and `auth.type/token/token_env` names match across tasks.
- Scope control: native OpenCode session management and process supervision are explicitly excluded because OCQ owns that layer.
