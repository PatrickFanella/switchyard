package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

type adminHarness struct {
	serverURL string
	queue     *queue.Queue
	cleanup   func()
}

type brokerStatusItem struct {
	ID string `json:"id"`
}

type brokerStatusUpstream struct {
	Name       string             `json:"name"`
	QueueDepth int                `json:"queue_depth"`
	InFlightID string             `json:"in_flight_id"`
	Items      []brokerStatusItem `json:"queue"`
}

type brokerStatusResponse struct {
	Upstreams []brokerStatusUpstream `json:"upstreams"`
}

func newAdminHarness(t *testing.T, ollamaHandler http.HandlerFunc) adminHarness {
	t.Helper()
	return newAdminHarnessWithUpstreams(t, ollamaHandler, nil)
}

func newAdminHarnessWithUpstreams(t *testing.T, ollamaHandler http.HandlerFunc, upstreams []config.UpstreamConfig) adminHarness {
	t.Helper()
	return newAdminHarnessFull(t, ollamaHandler, upstreams, "")
}

func newAdminHarnessWithConfigPath(t *testing.T, ollamaHandler http.HandlerFunc, configPath string) adminHarness {
	t.Helper()
	return newAdminHarnessFull(t, ollamaHandler, nil, configPath)
}

func newAdminHarnessFull(t *testing.T, ollamaHandler http.HandlerFunc, upstreams []config.UpstreamConfig, configPath string) adminHarness {
	t.Helper()
	fakeOllama := httptest.NewServer(ollamaHandler)
	cfg := &config.Config{
		ConfigPath:            configPath,
		ListenAddr:            "127.0.0.1:0",
		MaxQueueDepth:         5,
		RequestTimeoutSecs:    30,
		QueueWaitTimeoutSecs:  60,
		HeartbeatIntervalSecs: 60,
		OllamaRetryMaxSecs:    1,
		AdminKey:              "admin-secret",
		Keys:                  []config.APIKey{{Name: "test", Key: "secret"}},
	}
	if len(upstreams) == 0 {
		cfg.OllamaURL = fakeOllama.URL
	} else {
		cfg.Upstreams = make([]config.UpstreamConfig, len(upstreams))
		for i, u := range upstreams {
			u.URL = fakeOllama.URL
			cfg.Upstreams[i] = u
		}
	}
	q := queue.New(cfg.MaxQueueDepth)
	fwd := proxy.New(fakeOllama.URL, cfg.OllamaRetryMaxSecs, testLogger())
	srv := server.New(cfg, q, fwd, testLogger())

	mux := http.NewServeMux()
	mux.HandleFunc("/broker/status", srv.HandleBrokerStatus)
	mux.Handle("/admin/", srv.AdminHandler())
	mux.Handle("/", server.AuthMiddlewareFromKeys(cfg.Keys, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if server.IsInferenceEndpoint(r.URL.Path) {
			srv.HandleInference(w, r)
		} else {
			srv.HandlePassThrough(w, r)
		}
	})))

	ctx, cancel := context.WithCancel(context.Background())
	srv.StartDispatchers(ctx, time.Duration(cfg.RequestTimeoutSecs)*time.Second, time.Duration(cfg.QueueWaitTimeoutSecs)*time.Second)
	httpSrv := httptest.NewServer(mux)

	t.Cleanup(func() {
		cancel()
		httpSrv.CloseClientConnections()
		fakeOllama.CloseClientConnections()
	})

	return adminHarness{serverURL: httpSrv.URL, queue: q, cleanup: func() {
		cancel()
		httpSrv.CloseClientConnections()
		fakeOllama.CloseClientConnections()
	}}
}

type configKeyFile struct {
	Keys []config.APIKey `yaml:"keys"`
}

func readConfigKeys(t *testing.T, path string) []config.APIKey {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out configKeyFile
	if err := yaml.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out.Keys
}

func adminRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Admin-Key", "admin-secret")
	return req
}

func decodeJSON(t *testing.T, body io.ReadCloser) map[string]any {
	t.Helper()
	defer body.Close()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return m
}

func getBrokerStatus(t *testing.T, baseURL string) brokerStatusResponse {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/broker/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out brokerStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode broker status: %v", err)
	}
	return out
}

func TestAdminAuth_MissingKey(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req, _ := http.NewRequest(http.MethodGet, h.serverURL+"/admin/queue", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminAuth_WrongKey(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req, _ := http.NewRequest(http.MethodGet, h.serverURL+"/admin/queue", nil)
	req.Header.Set("X-Admin-Key", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestAdminCreateKeyPersistsToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("keys:\n  - name: test\n    key: secret\n    priority: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAdminHarnessWithConfigPath(t, func(w http.ResponseWriter, r *http.Request) {}, path)

	req := adminRequest(t, http.MethodPost, h.serverURL+"/admin/keys", strings.NewReader(`{"name":"new-app","priority":9}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %v", resp.StatusCode, m)
	}
	keyValue, ok := m["key"].(string)
	if !ok || keyValue == "" {
		t.Fatalf("response key = %v, want non-empty string", m["key"])
	}

	keys := readConfigKeys(t, path)
	if len(keys) != 2 {
		t.Fatalf("len(keys) = %d, want 2", len(keys))
	}
	created := keys[1]
	if created.Name != "new-app" || created.Key != keyValue || created.Priority != 9 {
		t.Fatalf("created config key = %+v, response key %q", created, keyValue)
	}
}

func TestAdminUpdateKeyPersistsToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("keys:\n  - name: test\n    key: secret\n    priority: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAdminHarnessWithConfigPath(t, func(w http.ResponseWriter, r *http.Request) {}, path)

	req := adminRequest(t, http.MethodPatch, h.serverURL+"/admin/keys/test", strings.NewReader(`{"name":"renamed","priority":4}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	keys := readConfigKeys(t, path)
	if len(keys) != 1 {
		t.Fatalf("len(keys) = %d, want 1", len(keys))
	}
	if keys[0].Name != "renamed" || keys[0].Key != "secret" || keys[0].Priority != 4 {
		t.Fatalf("updated config key = %+v", keys[0])
	}
}

func TestAdminRevokeKeyRemovesFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("keys:\n  - name: test\n    key: secret\n    priority: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newAdminHarnessWithConfigPath(t, func(w http.ResponseWriter, r *http.Request) {}, path)

	req := adminRequest(t, http.MethodDelete, h.serverURL+"/admin/keys/test", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	keys := readConfigKeys(t, path)
	if len(keys) != 0 {
		t.Fatalf("keys = %+v, want empty", keys)
	}
}

func TestAdminGetQueue_Empty(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req := adminRequest(t, http.MethodGet, h.serverURL+"/admin/queue", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if m["depth"] != float64(0) {
		t.Errorf("depth = %v, want 0", m["depth"])
	}
}

func TestAdminDropRequest_NotFound(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req := adminRequest(t, http.MethodDelete, h.serverURL+"/admin/queue/nonexistent-id", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAdminDrainQueue(t *testing.T) {
	// Block ollama so requests pile up in queue.
	block := make(chan struct{})
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"ok"}`))
	})

	client := &http.Client{}

	// Send first request (will be in-flight, blocking ollama).
	go func() {
		req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, _ := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	// Wait for queue to have in-flight.
	waitForCondition(t, 2*time.Second, func() bool {
		snap := h.queue.Snapshot()
		return snap.InFlightClient != ""
	})

	// Enqueue a second request.
	go func() {
		req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{}`))
		resp, _ := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	waitForCondition(t, 2*time.Second, func() bool { return h.queue.Depth() == 1 })

	// Drain.
	req := adminRequest(t, http.MethodDelete, h.serverURL+"/admin/queue", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if m["dropped"] != float64(1) {
		t.Errorf("dropped = %v, want 1", m["dropped"])
	}
	if h.queue.Depth() != 0 {
		t.Errorf("queue depth after drain = %d, want 0", h.queue.Depth())
	}

	close(block) // unblock in-flight
}

func TestAdminGetInflight_None(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req := adminRequest(t, http.MethodGet, h.serverURL+"/admin/inflight", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, resp.Body)
	if m["in_flight"] != false {
		t.Errorf("in_flight = %v, want false", m["in_flight"])
	}
}

func TestAdminCancelInflight_None(t *testing.T) {
	h := newAdminHarness(t, func(w http.ResponseWriter, r *http.Request) {})
	req := adminRequest(t, http.MethodPost, h.serverURL+"/admin/inflight/cancel", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestAdminGetInflight_SecondaryUpstream(t *testing.T) {
	block := make(chan struct{})
	h := newAdminHarnessWithUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
	}, []config.UpstreamConfig{
		{Name: "primary", Models: nil},
		{Name: "secondary", Models: []string{"secondary*"}},
	})

	go func() {
		req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"secondary-model"}`))
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	var inflightID string
	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		for _, up := range status.Upstreams {
			if up.Name == "secondary" && up.InFlightID != "" {
				inflightID = up.InFlightID
				return true
			}
		}
		return false
	})

	req := adminRequest(t, http.MethodGet, h.serverURL+"/admin/inflight", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	m := decodeJSON(t, resp.Body)
	if m["in_flight"] != true {
		t.Fatalf("in_flight = %v, want true", m["in_flight"])
	}
	if m["id"] != inflightID {
		t.Fatalf("id = %v, want %s", m["id"], inflightID)
	}

	close(block)
}

func TestAdminDropRequest_SecondaryUpstream(t *testing.T) {
	block := make(chan struct{})
	h := newAdminHarnessWithUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"ok"}`))
	}, []config.UpstreamConfig{
		{Name: "primary", Models: nil},
		{Name: "secondary", Models: []string{"secondary*"}},
	})

	inflightClient := &http.Client{}
	queuedClient := &http.Client{}
	go func() {
		req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"secondary-model"}`))
		resp, _ := inflightClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		for _, up := range status.Upstreams {
			if up.Name == "secondary" {
				return up.InFlightID != ""
			}
		}
		return false
	})

	go func() {
		req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"secondary-model"}`))
		resp, _ := queuedClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	var queuedID string
	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		for _, up := range status.Upstreams {
			if up.Name == "secondary" && up.QueueDepth == 1 && len(up.Items) == 1 {
				queuedID = up.Items[0].ID
				return true
			}
		}
		return false
	})

	req := adminRequest(t, http.MethodDelete, h.serverURL+"/admin/queue/"+queuedID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	status := getBrokerStatus(t, h.serverURL)
	for _, up := range status.Upstreams {
		if up.Name == "secondary" && up.QueueDepth != 0 {
			t.Fatalf("secondary queue depth = %d, want 0", up.QueueDepth)
		}
	}

	close(block)
}

func TestAdminDrainQueue_AllUpstreams(t *testing.T) {
	blocks := map[string]chan struct{}{
		"primary":   make(chan struct{}),
		"secondary": make(chan struct{}),
	}
	h := newAdminHarnessWithUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocks["primary"]:
		case <-blocks["secondary"]:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"ok"}`))
	}, []config.UpstreamConfig{
		{Name: "primary", Models: nil},
		{Name: "secondary", Models: []string{"secondary*"}},
	})

	client := &http.Client{}
	for _, model := range []string{"primary-model", "secondary-model"} {
		go func(model string) {
			req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"`+model+`"}`))
			resp, _ := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
		}(model)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		seen := map[string]bool{}
		for _, up := range status.Upstreams {
			if up.InFlightID != "" {
				seen[up.Name] = true
			}
		}
		return seen["primary"] && seen["secondary"]
	})

	for _, model := range []string{"primary-model", "secondary-model"} {
		go func(model string) {
			req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"`+model+`"}`))
			resp, _ := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
		}(model)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		total := 0
		for _, up := range status.Upstreams {
			total += up.QueueDepth
		}
		return total == 2
	})

	req := adminRequest(t, http.MethodDelete, h.serverURL+"/admin/queue", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	m := decodeJSON(t, resp.Body)
	if m["dropped"] != float64(2) {
		t.Fatalf("dropped = %v, want 2", m["dropped"])
	}

	status := getBrokerStatus(t, h.serverURL)
	for _, up := range status.Upstreams {
		if up.QueueDepth != 0 {
			t.Fatalf("upstream %s queue depth = %d, want 0", up.Name, up.QueueDepth)
		}
	}

	close(blocks["primary"])
	close(blocks["secondary"])
}

func TestAdminCancelInflight_AllUpstreams(t *testing.T) {
	blocks := map[string]chan struct{}{
		"primary":   make(chan struct{}),
		"secondary": make(chan struct{}),
	}
	h := newAdminHarnessWithUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocks["primary"]:
		case <-blocks["secondary"]:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"response":"ok"}`))
	}, []config.UpstreamConfig{
		{Name: "primary", Models: nil},
		{Name: "secondary", Models: []string{"secondary*"}},
	})

	client := &http.Client{}
	for _, model := range []string{"primary-model", "secondary-model"} {
		go func(model string) {
			req := authedRequest(t, http.MethodPost, h.serverURL+"/api/generate", strings.NewReader(`{"model":"`+model+`"}`))
			resp, _ := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
		}(model)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		seen := map[string]bool{}
		for _, up := range status.Upstreams {
			if up.InFlightID != "" {
				seen[up.Name] = true
			}
		}
		return seen["primary"] && seen["secondary"]
	})

	req := adminRequest(t, http.MethodPost, h.serverURL+"/admin/inflight/cancel", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	waitForCondition(t, 2*time.Second, func() bool {
		status := getBrokerStatus(t, h.serverURL)
		for _, up := range status.Upstreams {
			if up.InFlightID != "" {
				return false
			}
		}
		return true
	})

	close(blocks["primary"])
	close(blocks["secondary"])
}
