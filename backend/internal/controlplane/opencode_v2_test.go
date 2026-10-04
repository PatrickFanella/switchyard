package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeV2CompletionAndInterrupt(t *testing.T) {
	prompted := false
	polls := 0
	interrupted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		if user != "opencode" || password != "fixture" {
			t.Errorf("wrong v2 auth")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/plugin":
			w.Write([]byte(`{"data":[{"id":"switchyard.rtk","state":{"status":"active"}},{"id":"switchyard.managed","state":{"status":"active"}}]}`))
		case "/api/agent":
			w.Write([]byte(`{"data":[{"id":"build","permissions":[{"action":"*","resource":"*","effect":"deny"}]}]}`))
		case "/api/session":
			w.Write([]byte(`{"data":{"id":"ses_v2"}}`))
		case "/api/session/ses_v2/model":
			w.WriteHeader(204)
		case "/api/session/ses_v2/prompt":
			prompted = true
			w.Write([]byte(`{"data":{"id":"msg_input"}}`))
		case "/api/session/ses_v2/message":
			if r.URL.Query().Get("order") != "desc" {
				t.Errorf("latest messages must use descending order")
			}
			if !prompted {
				w.Write([]byte(`{"data":[]}`))
				return
			}
			polls++
			if polls == 1 {
				w.Write([]byte(`{"data":[{"id":"msg_answer","type":"assistant","finish":"stop","content":[{"type":"text","text":"premature"}]}]}`))
				return
			}
			w.Write([]byte(`{"data":[{"id":"msg_idle","type":"idle","outcome":"succeeded"},{"id":"msg_answer","type":"assistant","content":[{"type":"reasoning","text":"private"},{"type":"text","text":"done"}]}]}`))
		case "/api/session/ses_v2/interrupt":
			interrupted = true
			w.Write([]byte(`{"interrupted":true}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	e := &EnvOpenCodeExecutor{baseURL: server.URL + "/api", username: "legacy-name", password: "fixture", client: server.Client()}
	response, err := e.SendPrompt(context.Background(), OpenCodeRequest{Prompt: "hello", Model: "fixture/fixture", WorkDir: "/fixture", RequestID: "fixture-request"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "done" || response.SessionID != "ses_v2" || polls < 2 {
		t.Fatalf("unexpected completion: %+v polls=%d", response, polls)
	}
	e.abortOpenCodeSession(context.Background(), response.SessionID)
	if !interrupted {
		t.Fatal("v2 interrupt was not called")
	}
}
func TestOpenCodeV2ProviderErrorIsRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"msg_idle","type":"idle","outcome":"failed"},{"id":"msg_error","type":"assistant","error":{"type":"provider","status":401,"message":"secret-marker"}}]}`))
	}))
	defer server.Close()
	e := &EnvOpenCodeExecutor{baseURL: server.URL + "/api", client: server.Client()}
	_, err := e.openCodeV2LatestMessage(context.Background(), "ses_failure")
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), "secret-marker") {
		t.Fatalf("wrong error: %v", err)
	}
}
func TestOpenCodeV2LiveFixture(t *testing.T) {
	base := os.Getenv("SWITCHYARD_OPENCODE_V2_FIXTURE_URL")
	if base == "" {
		t.Skip("isolated native fixture is not running")
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") || !openCodeV2(base) {
		t.Fatal("fixture must be an explicit loopback v2 endpoint")
	}
	e := &EnvOpenCodeExecutor{baseURL: base, password: "probe-only", client: &http.Client{Timeout: 30 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	response, err := e.SendPrompt(ctx, OpenCodeRequest{Prompt: "Reply SWITCHYARD_V2_OK without tools.", Model: "fixture/fixture", WorkDir: os.Getenv("SWITCHYARD_OPENCODE_V2_FIXTURE_DIRECTORY")})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "SWITCHYARD_V2_OK" {
		t.Fatalf("wrong fixture response %q", response.Content)
	}
	response, err = e.SendPrompt(ctx, OpenCodeRequest{SessionID: response.SessionID, Prompt: "Reply SWITCHYARD_V2_OK again without tools.", Model: "fixture/fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "SWITCHYARD_V2_OK" {
		t.Fatal("session reuse failed")
	}
	p := &EnvOpenCodeExecutorStatusProvider{openCodeURL: base, password: "probe-only", client: e.client}
	if status := p.Status(ctx); !status.Healthy {
		t.Fatalf("native status failed: %+v", status)
	}
	e.abortOpenCodeSession(ctx, response.SessionID)
}

func TestOpenCodeV2RefusesUnsettledPlugins(t *testing.T) {
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posted = true
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	e := &EnvOpenCodeExecutor{baseURL: server.URL + "/api", client: server.Client()}
	_, err := e.postOpenCodeV2Prompt(context.Background(), "ses_unready", OpenCodeRequest{Prompt: "must not dispatch"})
	if err == nil || posted {
		t.Fatalf("unsettled plugins admitted a prompt: posted=%v err=%v", posted, err)
	}
}
