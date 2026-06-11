package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testConfig(ollamaURL string) *config.Config {
	return &config.Config{
		ListenAddr:            "127.0.0.1:0",
		OllamaURL:             ollamaURL,
		MaxQueueDepth:         5,
		RequestTimeoutSecs:    30,
		QueueWaitTimeoutSecs:  60,
		HeartbeatIntervalSecs: 60,
		OllamaRetryMaxSecs:    1,
		Keys:                  []config.APIKey{{Name: "test", Key: "secret"}},
		AdminKey:              "admin-secret",
	}
}

type testHarness struct {
	serverURL string
	queue     *queue.Queue
	srv       *server.Server
	cfg       *config.Config
	cleanup   func()
}

func newTestHarness(t *testing.T, ollamaHandler http.HandlerFunc) testHarness {
	t.Helper()
	return newTestHarnessWithConfig(t, testConfig(""), ollamaHandler)
}

func newTestHarnessWithConfig(t *testing.T, cfg *config.Config, ollamaHandler http.HandlerFunc) testHarness {
	t.Helper()

	fakeOllama := httptest.NewServer(ollamaHandler)
	cfg.OllamaURL = fakeOllama.URL
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(fakeOllama.URL, cfg.OllamaRetryMaxSecs, testLogger())
	srv := server.New(cfg, q, fwd, testLogger())

	mux := http.NewServeMux()
	mux.Handle("/admin/", srv.AdminHandler())
	mux.HandleFunc("/broker/status", srv.HandleBrokerStatus)
	mux.Handle("/", server.AuthMiddlewareFromKeys(cfg.Keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if server.IsInferenceEndpoint(r.URL.Path) {
			srv.HandleInference(w, r)
			return
		}
		srv.HandlePassThrough(w, r)
	})))

	ctx, cancel := context.WithCancel(context.Background())
	srv.StartDispatchers(ctx, time.Duration(cfg.RequestTimeoutSecs)*time.Second, time.Duration(cfg.QueueWaitTimeoutSecs)*time.Second)
	httpSrv := httptest.NewServer(mux)

	cleanup := func() {
		cancel()
		httpSrv.CloseClientConnections()
		fakeOllama.CloseClientConnections()
	}

	t.Cleanup(cleanup)

	return testHarness{serverURL: httpSrv.URL, queue: q, srv: srv, cfg: cfg, cleanup: cleanup}
}

func authedRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	return req
}

func waitForLine(t *testing.T, body io.ReadCloser) map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var got map[string]any
		if err := json.Unmarshal([]byte(payload), &got); err != nil {
			t.Fatalf("decode SSE payload: %v", err)
		}
		return got
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan SSE: %v", err)
	}
	t.Fatal("no SSE data line received")
	return nil
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func newRecordingTracer() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	recorder := tracetest.NewSpanRecorder()
	p := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	return p, recorder
}

func TestHandlerInference_QueuedSSEPosition(t *testing.T) {
	var count atomic.Int32
	releaseHeaders := make(chan struct{})
	releaseBody := make(chan struct{})
	firstSeen := make(chan struct{}, 1)
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch count.Add(1) {
		case 1:
			firstSeen <- struct{}{}
			<-releaseHeaders
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-releaseBody
			_, _ = w.Write([]byte(`{"response":"first"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":"second"}`))
		}
	})

	client := &http.Client{}
	firstRespCh := make(chan *http.Response, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, err := client.Do(req)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstRespCh <- resp
	}()

	select {
	case <-firstSeen:
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	secondRespCh := make(chan *http.Response, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		secondReq := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, err := client.Do(secondReq)
		if err != nil {
			secondErrCh <- err
			return
		}
		secondRespCh <- resp
	}()

	waitForCondition(t, 2*time.Second, func() bool { return harness.queue.Depth() == 1 })
	close(releaseHeaders)

	var secondResp *http.Response
	select {
	case secondResp = <-secondRespCh:
	case err := <-secondErrCh:
		t.Fatalf("second request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second response")
	}
	defer secondResp.Body.Close()

	got := waitForLine(t, secondResp.Body)
	if got["position"] != float64(1) {
		t.Fatalf("position = %v, want 1", got["position"])
	}
	if got["status"] != "queued" {
		t.Fatalf("status = %v, want queued", got["status"])
	}
	if got["request_id"] == "" || got["request_id"] == nil {
		t.Fatalf("request_id missing or empty in status event")
	}

	close(releaseBody)

	select {
	case resp := <-firstRespCh:
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response")
	}
}

func TestHandlerInference_AdminDropClosesQueuedRequest(t *testing.T) {
	var count atomic.Int32
	releaseHeaders := make(chan struct{})
	firstSeen := make(chan struct{}, 1)
	cfg := testConfig("")
	cfg.HeartbeatIntervalSecs = 1
	harness := newTestHarnessWithConfig(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		switch count.Add(1) {
		case 1:
			firstSeen <- struct{}{}
			<-releaseHeaders
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			_, _ = w.Write([]byte(`{"response":"first"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"response":"second"}`))
		}
	})

	client := &http.Client{}
	firstRespCh := make(chan *http.Response, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, err := client.Do(req)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstRespCh <- resp
	}()

	select {
	case <-firstSeen:
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	secondRespCh := make(chan *http.Response, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		secondReq := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, err := client.Do(secondReq)
		if err != nil {
			secondErrCh <- err
			return
		}
		secondRespCh <- resp
	}()

	waitForCondition(t, 2*time.Second, func() bool { return harness.queue.Depth() == 1 })

	var secondResp *http.Response
	select {
	case secondResp = <-secondRespCh:
	case err := <-secondErrCh:
		t.Fatalf("second request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second response")
	}
	defer secondResp.Body.Close()

	firstEvent := waitForLine(t, secondResp.Body)
	if firstEvent["status"] != "queued" {
		t.Fatalf("status = %v, want queued", firstEvent["status"])
	}
	requestID, _ := firstEvent["request_id"].(string)
	if requestID == "" {
		t.Fatal("request_id missing from queued status")
	}

	bodyDone := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, err := io.ReadAll(secondResp.Body)
		bodyDone <- struct {
			data []byte
			err  error
		}{data: data, err: err}
	}()

	dropReq := adminRequest(t, http.MethodDelete, harness.serverURL+"/admin/queue/"+requestID, nil)
	dropResp, err := client.Do(dropReq)
	if err != nil {
		t.Fatalf("admin drop request failed: %v", err)
	}
	_, _ = io.ReadAll(dropResp.Body)
	dropResp.Body.Close()
	if dropResp.StatusCode != http.StatusOK {
		t.Fatalf("drop status = %d, want 200", dropResp.StatusCode)
	}

	select {
	case res := <-bodyDone:
		if res.err != nil {
			t.Fatalf("read queued response: %v", res.err)
		}
		if !strings.Contains(string(res.data), "dropped_by_admin") {
			t.Fatalf("response body = %q, want dropped_by_admin", res.data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dropped queued request to close")
	}

	close(releaseHeaders)

	select {
	case resp := <-firstRespCh:
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response")
	}
}

func TestHandlerInference_ClientDisconnect(t *testing.T) {
	started := make(chan struct{}, 1)
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	})

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")

	client := &http.Client{}
	go func() {
		resp, _ := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream request")
	}

	time.AfterFunc(50*time.Millisecond, cancel)

	waitForCondition(t, 2*time.Second, func() bool { return harness.queue.Depth() == 0 })
}

func TestHandlerPassThrough(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
	})

	req := authedRequest(t, http.MethodGet, harness.serverURL+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != `{"models":[]}` {
		t.Fatalf("body = %s, want {\"models\":[]}", body)
	}
	if resp.Header.Get("X-Ollama-Broker") != "true" {
		t.Fatalf("X-Ollama-Broker = %q, want true", resp.Header.Get("X-Ollama-Broker"))
	}
}

func TestAuditLog_RedactsBodiesByDefaultAndReportsToolErrors(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"}}`))
	})

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	body := `{
		"model":"qwen3:14b",
		"stream":false,
		"tools":[{"function":{"name":"web_fetch"}}],
		"messages":[
			{"role":"user","content":"research this"},
			{"role":"tool","content":"{\"error\":\"Fetch failed: permission denied while trying to connect to the docker API at unix:///var/run/docker.sock\\n\"}"},
			{"role":"tool","content":"ERROR: Tool \"file_write\" does not exist. Available tools: web_search, web_fetch"}
		]
	}`
	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/chat", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if _, ok := entry["request_body"]; ok {
		t.Fatal("request_body present by default, want redacted")
	}
	if _, ok := entry["response_body"]; ok {
		t.Fatal("response_body present by default, want redacted")
	}
	if entry["request_body_bytes"] == nil || entry["response_body_bytes"] == nil {
		t.Fatalf("body byte metadata missing: %v", entry)
	}
	if got := entry["request_tool_error_count"]; got != float64(2) {
		t.Fatalf("request_tool_error_count = %v, want 2", got)
	}
	toolErrors, ok := entry["request_tool_errors"].([]any)
	if !ok || len(toolErrors) != 2 || !strings.Contains(toolErrors[0].(string), "docker API") || !strings.Contains(toolErrors[1].(string), "does not exist") {
		t.Fatalf("request_tool_errors = %#v, want docker API and missing-tool errors", entry["request_tool_errors"])
	}
	toolNames, ok := entry["request_tool_names"].([]any)
	if !ok || len(toolNames) != 1 || toolNames[0] != "web_fetch" {
		t.Fatalf("request_tool_names = %#v, want web_fetch", entry["request_tool_names"])
	}
}

func TestAuditLog_FlagsMalformedUpstreamJSON(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":`))
	})

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/chat", strings.NewReader(`{"model":"qwen3:14b","stream":false,"messages":[{"role":"user","content":"hi"}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if got := entry["response_json_valid"]; got != false {
		t.Fatalf("response_json_valid = %v, want false", got)
	}
}

func TestAuditLog_IncludesOllamaTokenCounts(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok","done":true,"prompt_eval_count":12,"eval_count":34}`))
	})

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{"model":"qwen3:14b","stream":false}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if got := entry["prompt_tokens"]; got != float64(12) {
		t.Fatalf("prompt_tokens = %v, want 12", got)
	}
	if got := entry["completion_tokens"]; got != float64(34) {
		t.Fatalf("completion_tokens = %v, want 34", got)
	}
	if got := entry["total_tokens"]; got != float64(46) {
		t.Fatalf("total_tokens = %v, want 46", got)
	}
}

func TestAuditLog_IncludesZeroTokenCountsWhenMissing(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok","done":true}`))
	})

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{"model":"qwen3:14b","stream":false}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if got := entry["prompt_tokens"]; got != float64(0) {
		t.Fatalf("prompt_tokens = %v, want 0", got)
	}
	if got := entry["completion_tokens"]; got != float64(0) {
		t.Fatalf("completion_tokens = %v, want 0", got)
	}
	if got := entry["total_tokens"]; got != float64(0) {
		t.Fatalf("total_tokens = %v, want 0", got)
	}
}

func TestAuditLog_IncludesZeroTokenCountsForMalformedResponse(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":`))
	})

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{"model":"qwen3:14b","stream":false}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if got := entry["response_json_valid"]; got != false {
		t.Fatalf("response_json_valid = %v, want false", got)
	}
	if got := entry["prompt_tokens"]; got != float64(0) {
		t.Fatalf("prompt_tokens = %v, want 0", got)
	}
	if got := entry["completion_tokens"]; got != float64(0) {
		t.Fatalf("completion_tokens = %v, want 0", got)
	}
	if got := entry["total_tokens"]; got != float64(0) {
		t.Fatalf("total_tokens = %v, want 0", got)
	}
}

func TestAuditLog_IncludesBodiesForNonStreamingGenerateWhenEnabled(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok","done":true}`))
	})
	harness.cfg.AuditLogIncludeBody = true

	var audit bytes.Buffer
	harness.srv.SetAuditLog(slog.New(slog.NewJSONHandler(&audit, nil)))

	body := `{"model":"qwen3:14b","prompt":"hello","stream":false}`
	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(audit.Bytes()), &entry); err != nil {
		t.Fatalf("decode audit log: %v\n%s", err, audit.String())
	}
	if got := entry["request_body"]; got != body {
		t.Fatalf("request_body = %v, want %s", got, body)
	}
	if got := entry["response_body"]; got != `{"response":"ok","done":true}` {
		t.Fatalf("response_body = %v, want upstream body", got)
	}
}

func TestPhoenixSpans_NilTracerDoesNotChangeInference(t *testing.T) {
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":3,"eval_count":4}`))
	})

	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/chat", strings.NewReader(`{"model":"qwen3:14b","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed with nil tracer: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"content":"ok"`) {
		t.Fatalf("body = %s, want upstream response", body)
	}
}

func TestPhoenixSpans_EmitLLMAndToolSpans(t *testing.T) {
	var count atomic.Int32
	releaseHeaders := make(chan struct{})
	releaseBody := make(chan struct{})
	firstSeen := make(chan struct{}, 1)
	harness := newTestHarness(t, func(w http.ResponseWriter, r *http.Request) {
		switch count.Add(1) {
		case 1:
			firstSeen <- struct{}{}
			<-releaseHeaders
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-releaseBody
			_, _ = w.Write([]byte(`{"response":"first"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":12,"eval_count":34}`))
		}
	})

	provider, recorder := newRecordingTracer()
	defer func() { _ = provider.Shutdown(context.Background()) }()
	harness.srv.SetTracer(provider.Tracer("test"))

	firstRespCh := make(chan *http.Response, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/generate", strings.NewReader(`{"model":"qwen3:14b","stream":false}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstRespCh <- resp
	}()

	select {
	case <-firstSeen:
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	body := `{
		"model":"qwen3:14b",
		"stream":false,
		"tools":[{"function":{"name":"web_fetch"}}],
		"messages":[
			{"role":"user","content":"research this"},
			{"role":"tool","content":"{\"error\":\"Fetch failed\"}"}
		]
	}`
	req := authedRequest(t, http.MethodPost, harness.serverURL+"/api/chat", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	time.Sleep(100 * time.Millisecond)
	close(releaseHeaders)
	close(releaseBody)
	select {
	case resp := <-firstRespCh:
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response")
	}

	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}
	findSpan := func(name string) sdktrace.ReadOnlySpan {
		for _, span := range spans {
			if span.Name() == name {
				return span
			}
		}
		return nil
	}
	var attrs map[string]any
	for _, span := range spans {
		if span.Name() != "llama-line.request" {
			continue
		}
		candidate := map[string]any{}
		for _, a := range span.Attributes() {
			candidate[string(a.Key)] = a.Value.AsInterface()
		}
		if candidate["llm.input_messages.0.message.content"] == "research this" {
			attrs = candidate
			break
		}
	}
	if attrs == nil {
		t.Fatal("missing target root LLM span")
	}
	for _, want := range []struct {
		k string
		v any
	}{
		{"openinference.span.kind", "LLM"},
		{"llm.model_name", "qwen3:14b"},
		{"llama_line.client", "test"},
		{"llama_line.path", "/api/chat"},
		{"llama_line.status", "success"},
		{"llm.token_count.prompt", int64(12)},
		{"llm.token_count.completion", int64(34)},
		{"llm.token_count.total", int64(46)},
		{"llm.input_messages.0.message.role", "user"},
		{"llm.input_messages.0.message.content", "research this"},
		{"llm.output_messages.0.message.role", "assistant"},
		{"llm.output_messages.0.message.content", "ok"},
	} {
		if got := attrs[want.k]; got != want.v {
			t.Fatalf("%s = %v, want %v", want.k, got, want.v)
		}
	}
	if findSpan("llm.queue_wait") == nil {
		t.Fatal("missing queue wait span")
	}
	if findSpan("llm.upstream_call") == nil {
		t.Fatal("missing upstream span")
	}
	tool := findSpan("tool.call")
	if tool == nil {
		t.Fatal("missing tool span")
	}
	toolAttrs := map[string]any{}
	for _, a := range tool.Attributes() {
		toolAttrs[string(a.Key)] = a.Value.AsInterface()
	}
	if toolAttrs["openinference.span.kind"] != "TOOL" {
		t.Fatalf("tool span kind = %v, want TOOL", toolAttrs["openinference.span.kind"])
	}
	if toolAttrs["tool.name"] != "web_fetch" {
		t.Fatalf("tool.name = %v, want web_fetch", toolAttrs["tool.name"])
	}
	if toolAttrs["tool.error"] != "Fetch failed" {
		t.Fatalf("tool.error = %v, want Fetch failed", toolAttrs["tool.error"])
	}
}

func TestHandlerInference_RoutesProviderPrefixedOpenCodeModelToOCQWithOutboundAuth(t *testing.T) {
	var gotAuth string
	var gotBody string
	var gotRequestID string
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("OCQ path = %q, want /v1/chat/completions", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotRequestID = r.Header.Get("X-Llama-Line-Request-ID")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-ocq-session", "session-123")
		w.Header().Set("x-ocq-request-id", gotRequestID)
		w.Header().Set("x-ocq-status", "completed")
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
		{Name: "ocq-almaz", URL: ocq.URL, Models: []string{"openai/gpt-*", "github-copilot/*"}, Auth: config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"}},
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

	req := authedRequest(t, http.MethodPost, httpSrv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-5.4-mini","messages":[{"role":"user","content":"hi"}],"stream":false,"ocq_session_id":"keep-me"}`))
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
	if gotRequestID == "" {
		t.Fatal("OCQ X-Llama-Line-Request-ID is empty")
	}
	if !strings.Contains(gotBody, `"ocq_session_id":"keep-me"`) {
		t.Fatalf("OCQ body = %s, want ocq_session_id preserved", gotBody)
	}
	if !strings.Contains(string(body), "from ocq") {
		t.Fatalf("broker body = %s, want OCQ response", body)
	}
	if !strings.Contains(string(body), `"ocq_session_id":"session-123"`) {
		t.Fatalf("broker body = %s, want ocq_session_id copied into body", body)
	}
	if resp.Header.Get("x-ocq-session") != "session-123" {
		t.Fatalf("x-ocq-session = %q, want session-123", resp.Header.Get("x-ocq-session"))
	}
	if resp.Header.Get("x-ocq-request-id") != gotRequestID {
		t.Fatalf("x-ocq-request-id = %q, want %q", resp.Header.Get("x-ocq-request-id"), gotRequestID)
	}
	if resp.Header.Get("x-ocq-status") != "completed" {
		t.Fatalf("x-ocq-status = %q, want completed", resp.Header.Get("x-ocq-status"))
	}
	if resp.Header.Get("X-Llama-Line-Request-ID") == "" {
		t.Fatal("X-Llama-Line-Request-ID response header is empty")
	}
}

func TestHandlerInference_RewritesProviderPrefixedAPIChatToOCQChatCompletions(t *testing.T) {
	var gotPath string
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"from ocq"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(ocq.Close)

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("ollama upstream should not receive provider-prefixed opencode request")
	}))
	t.Cleanup(ollama.Close)

	cfg := testConfig(ollama.URL)
	cfg.Upstreams = []config.UpstreamConfig{
		{Name: "ollama", URL: ollama.URL},
		{Name: "ocq-almaz", URL: ocq.URL, Models: []string{"openai/gpt-*", "github-copilot/*"}},
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

	req := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/chat", strings.NewReader(`{"model":"openai/gpt-5.5","messages":[{"role":"user","content":"hi"}],"stream":false}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("OCQ path = %q, want /v1/chat/completions", gotPath)
	}
	if !strings.Contains(string(body), "from ocq") {
		t.Fatalf("broker body = %s, want OCQ response", body)
	}
}

func TestHandlerInference_OCQQueuedResponseIncludesSessionInBody(t *testing.T) {
	var count atomic.Int32
	releaseFirst := make(chan struct{})
	firstSeen := make(chan struct{}, 1)
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch count.Add(1) {
		case 1:
			firstSeen <- struct{}{}
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-ocq-session", "session-123")
			_, _ = w.Write([]byte(`{"response":"first"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-ocq-session", "session-123")
			_, _ = w.Write([]byte(`{"response":"second"}`))
		}
	}))
	t.Cleanup(ocq.Close)

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("ollama upstream should not receive opencode model request")
	}))
	t.Cleanup(ollama.Close)

	cfg := testConfig(ollama.URL)
	cfg.Upstreams = []config.UpstreamConfig{
		{Name: "ollama", URL: ollama.URL},
		{Name: "ocq-almaz", URL: ocq.URL, Models: []string{"openai/gpt-*", "github-copilot/*"}, Auth: config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"}},
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

	client := &http.Client{}
	firstErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/generate", strings.NewReader(`{"model":"openai/gpt-5.4-mini","stream":false}`))
		resp, err := client.Do(req)
		if err != nil {
			firstErrCh <- err
			return
		}
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	}()

	select {
	case <-firstSeen:
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	secondRespCh := make(chan *http.Response, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		secondReq := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/generate", strings.NewReader(`{"model":"openai/gpt-5.4-mini","stream":false}`))
		resp, err := client.Do(secondReq)
		if err != nil {
			secondErrCh <- err
			return
		}
		secondRespCh <- resp
	}()

	close(releaseFirst)

	var secondResp *http.Response
	select {
	case secondResp = <-secondRespCh:
	case err := <-secondErrCh:
		t.Fatalf("second request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second response")
	}
	defer secondResp.Body.Close()

	got := waitForLine(t, secondResp.Body)
	if got["status"] != "queued" {
		t.Logf("status = %v, want queued", got["status"])
	}
	if got["position"] != float64(1) {
		t.Logf("position = %v, want 1", got["position"])
	}
	body, _ := io.ReadAll(secondResp.Body)
	if !strings.Contains(string(body), `"ocq_session_id":"session-123"`) && got["ocq_session_id"] != "session-123" {
		t.Fatalf("body = %s, want ocq_session_id in SSE payload", body)
	}
}

func TestHandlerInference_SkipsDedupAndCacheForOpenCodeModel(t *testing.T) {
	var count atomic.Int32
	releaseFirst := make(chan struct{})
	firstSeen := make(chan struct{}, 1)
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch count.Add(1) {
		case 1:
			firstSeen <- struct{}{}
			<-releaseFirst
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-ocq-session", "session-123")
			_, _ = w.Write([]byte(`{"response":"first"}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-ocq-session", "session-123")
			_, _ = w.Write([]byte(`{"response":"second"}`))
		}
	}))
	t.Cleanup(ocq.Close)

	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("ollama upstream should not receive opencode model request")
	}))
	t.Cleanup(ollama.Close)

	cfg := testConfig(ollama.URL)
	cfg.Keys[0].Deduplicate = true
	cfg.Keys[0].CacheTTLSecs = 60
	cfg.Keys[0].CacheMaxEntries = 10
	cfg.Upstreams = []config.UpstreamConfig{
		{Name: "ollama", URL: ollama.URL},
		{Name: "ocq-almaz", URL: ocq.URL, Models: []string{"openai/gpt-*", "github-copilot/*"}, Auth: config.UpstreamAuthConfig{Type: "bearer", Token: "ocq-secret"}},
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

	client := &http.Client{}
	firstRespCh := make(chan *http.Response, 1)
	firstErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/generate", strings.NewReader(`{"model":"openai/gpt-5.4-mini","stream":false}`))
		resp, err := client.Do(req)
		if err != nil {
			firstErrCh <- err
			return
		}
		firstRespCh <- resp
	}()

	select {
	case <-firstSeen:
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first upstream request")
	}

	secondRespCh := make(chan *http.Response, 1)
	secondErrCh := make(chan error, 1)
	go func() {
		req := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/generate", strings.NewReader(`{"model":"openai/gpt-5.4-mini","stream":false}`))
		resp, err := client.Do(req)
		if err != nil {
			secondErrCh <- err
			return
		}
		secondRespCh <- resp
	}()

	close(releaseFirst)

	select {
	case resp := <-firstRespCh:
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	case err := <-firstErrCh:
		t.Fatalf("first request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first response")
	}

	select {
	case resp := <-secondRespCh:
		defer resp.Body.Close()
		_, _ = io.ReadAll(resp.Body)
	case err := <-secondErrCh:
		t.Fatalf("second request failed: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second response")
	}

	thirdReq := authedRequest(t, http.MethodPost, httpSrv.URL+"/api/generate", strings.NewReader(`{"model":"openai/gpt-5.4-mini","stream":false}`))
	thirdResp, err := client.Do(thirdReq)
	if err != nil {
		t.Fatalf("third request failed: %v", err)
	}
	defer thirdResp.Body.Close()
	_, _ = io.ReadAll(thirdResp.Body)

	if got := count.Load(); got != 3 {
		t.Fatalf("upstream call count = %d, want 3", got)
	}
}
