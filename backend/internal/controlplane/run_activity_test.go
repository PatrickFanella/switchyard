package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const activityMessageFixture = `{"data":[{"id":"msg-agent","type":"assistant","model":{"providerID":"switchyard","modelID":"gpt-5.6-sol"},"time":{"created":1790000000000},"content":[{"type":"reasoning","text":"hidden-reasoning"},{"type":"tool","id":"tool-read","name":"read","time":{"created":1790000001000},"state":{"status":"completed","input":{"path":"src/main.go","password":"private-password"},"content":[{"type":"text","text":"file content api_key=private-key"},{"type":"image","data":"private-image"}],"metadata":{"providerState":"private-state"}}},{"type":"text","text":"Public response"}]},{"id":"msg-user","type":"user","content":[{"type":"text","text":"Read the repository"}]},{"id":"msg-compaction","type":"compaction","summary":"hidden-summary","time":{"created":1790000000000}}],"cursor":{"next":"earlier"}}`

func TestAgentActivityProjectionPublicActions(t *testing.T) {
	var fixture struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(activityMessageFixture), &fixture); err != nil {
		t.Fatal(err)
	}
	actions := projectAgentMessages("ses_owned", fixture.Data)
	if len(actions) != 4 || actions[0].ID != "tool-read" || actions[0].Status != "completed" || actions[0].Title != "read" {
		t.Fatalf("actions: %+v", actions)
	}
	raw, _ := json.Marshal(actions)
	for _, private := range []string{"hidden-reasoning", "hidden-summary", "private-password", "private-key", "private-image", "private-state"} {
		if strings.Contains(string(raw), private) {
			t.Errorf("private value exposed: %s", private)
		}
	}
	for _, public := range []string{"src/main.go", "Public response", "Read the repository", "gpt-5.6-sol", "[REDACTED]"} {
		if !strings.Contains(string(raw), public) {
			t.Errorf("public evidence omitted: %s", public)
		}
	}
	if got := activitySafe(`{"password":"quoted-secret"}`).(string); strings.Contains(got, "quoted-secret") {
		t.Fatalf("quoted secret exposed: %s", got)
	}
	if got := activitySafe(strings.Repeat("a", 70000)).(string); len(got) > 65600 || !strings.Contains(got, "truncated") {
		t.Fatal("text was not explicitly bounded")
	}
}

func TestRunActivityOwnedSessionAuthenticationAndBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	f := newSessionViewFixture(t)
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "opencode" || password != "executor-password" {
			t.Error("executor authentication was omitted")
		}
		if r.URL.Query().Get("limit") != "101" || r.URL.Query().Get("order") != "desc" {
			t.Errorf("query: %s", r.URL.RawQuery)
		}
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(activityMessageFixture))
	}))
	defer server.Close()
	api := f.api(healthyDefaultExecutor(), partialRepositoryStatus())
	api.executorSessionProber = &envExecutorSessionProber{client: server.Client(), routes: map[string]executorSessionRoute{"alpha/gen-1": {baseURL: server.URL + "/api", password: "executor-password", native: true}}}
	target := "/api/runs/" + f.runs["alpha"].ID + "/activity?session_id=ses_foreign"
	request := func(handler http.Handler, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(api.ControlPlaneHandler(), ""); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	w := request(api.ControlPlaneHandler(), "read-token")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	if len(paths) != 1 || strings.Contains(paths[0], "foreign") || !strings.HasSuffix(paths[0], "/message") {
		t.Fatalf("unowned session selected: %v", paths)
	}
	if strings.Contains(w.Body.String(), sessionViewSecret) || strings.Contains(w.Body.String(), "private-password") {
		t.Fatal("response exposed a credential")
	}
	if strings.Contains(w.Body.String(), "earlier messages are omitted") {
		t.Fatal("cursor presence incorrectly claimed omitted history")
	}
	target = "/api/runs/" + f.runs["alpha"].ID
	if w := request(api.ControlPlaneHandler(), "read-token"); w.Code != 200 || strings.Contains(w.Body.String(), sessionViewSecret) {
		t.Fatalf("expanded context status=%d credential_present=%t body=%s", w.Code, strings.Contains(w.Body.String(), sessionViewSecret), RedactForLog(w.Body.String()))
	}
	target += "/activity"
	if w := request(api.ModelIngressHandler(), "admin-token"); w.Code != 404 {
		t.Fatalf("model listener: %d", w.Code)
	}
}

func TestActivityUpstreamFailureAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "oversized") {
			_, _ = w.Write([]byte(strings.Repeat("x", 2<<20+1)))
			return
		}
		http.Error(w, "password=must-not-disclose", http.StatusForbidden)
	}))
	defer server.Close()
	p := &envExecutorSessionProber{client: server.Client(), routes: map[string]executorSessionRoute{"default": {baseURL: server.URL + "/api", native: true}}}
	for _, session := range []string{"ses_forbidden", "ses_oversized"} {
		_, _, err := p.readActivity(context.Background(), "default", session)
		if err == nil || strings.Contains(err.Error(), "must-not-disclose") {
			t.Fatalf("error: %v", err)
		}
	}
	_, _, err := p.readActivity(context.Background(), "missing", "ses_owned")
	if err == nil {
		t.Fatal("missing executor generation was not reported")
	}
}

func TestActivityPaginationCursorDoesNotProveMoreHistory(t *testing.T) {
	for _, count := range []int{20, 101} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			messages := make([]map[string]any, count)
			for i := range messages {
				messages[i] = map[string]any{"id": fmt.Sprint(i), "type": "idle"}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": messages, "cursor": map[string]string{"next": "end-cursor"}})
		}))
		p := &envExecutorSessionProber{client: server.Client(), routes: map[string]executorSessionRoute{"default": {baseURL: server.URL + "/api", native: true}}}
		actions, truncated, err := p.readActivity(context.Background(), "default", "ses_owned")
		if err != nil || truncated != (count > 100) || len(actions) != min(count, 100) {
			t.Fatalf("count=%d actions=%d truncated=%t err=%v", count, len(actions), truncated, err)
		}
		server.Close()
	}
}

func TestExpandedRunContextPreservesInlineArtifactIntegrity(t *testing.T) {
	uri := "data:text/plain," + url.PathEscape("public docs password=example-value\n"+strings.Repeat("a", 70000))
	detail := RunDetail{Artifacts: []Artifact{{URI: uri}}, Run: Run{Inputs: map[string]string{"password": "private-password"}}}
	projected := redactedActivityJSON(detail).(map[string]any)
	artifacts := projected["artifacts"].([]any)
	if got := artifacts[0].(map[string]any)["uri"]; got != uri {
		t.Fatal("run detail redaction broke the existing inline artifact byte contract")
	}
	if projected["run"].(map[string]any)["inputs"].(map[string]any)["password"] != "[REDACTED]" {
		t.Fatal("credential redaction was lost")
	}
	if bounded := activitySafe(uri).(string); len(bounded) >= len(uri) || !strings.Contains(bounded, "truncated") {
		t.Fatal("activity window lost its explicit size bound")
	}
}
