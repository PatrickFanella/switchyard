package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionViewOpenCodeStatus struct{ status OpenCodeExecutorStatus }

func (f sessionViewOpenCodeStatus) Status(context.Context) OpenCodeExecutorStatus { return f.status }

type sessionViewRepositoryStatus struct{ status OpenPilotRepositoryStatus }

func (f sessionViewRepositoryStatus) Status(context.Context) OpenPilotRepositoryStatus {
	return f.status
}

type sessionViewProber struct {
	mu      sync.Mutex
	results map[string]ExecutorSessionProbe
	calls   []string
}

func (p *sessionViewProber) ProbeSession(_ context.Context, executorKey, sessionID string) ExecutorSessionProbe {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, executorKey+" "+sessionID)
	if result, ok := p.results[sessionID]; ok {
		return result
	}
	return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "unexpected probe"}
}

type sessionViewFixture struct {
	now    time.Time
	store  *MemoryStore
	prober *sessionViewProber
	runs   map[string]Run
}

const sessionViewSecret = "sk-proj-sessionviewsecret1234567890"

func newSessionViewFixture(t *testing.T) *sessionViewFixture {
	t.Helper()
	f := &sessionViewFixture{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC), runs: map[string]Run{}}
	f.store = NewStore(func() time.Time { return f.now })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.store.UpsertWorkflow(Workflow{ID: "agent-work", Kind: WorkflowDevlog, Name: "Agent work", Status: "active"}))
	must(f.store.UpsertWorkflow(Workflow{ID: "devlog", Kind: WorkflowDevlog, Name: "Devlog", Status: "active"}))

	create := func(name, workflow string, inputs map[string]string, start bool) Run {
		t.Helper()
		run, err := f.store.CreateRun(CreateRunInput{WorkflowID: workflow, Inputs: inputs})
		must(err)
		if start {
			run, err = f.store.StartRun(run.ID)
			must(err)
		}
		f.runs[name] = run
		return run
	}
	step := func(run Run, position int, correlation map[string]string, status RunStepStatus) {
		t.Helper()
		created, err := f.store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: position, Name: "open-pilot.agent-work", Executor: "opencode", Inputs: map[string]string{"prompt": "step prompt " + sessionViewSecret}, CorrelationIDs: correlation})
		must(err)
		if status == StepQueued {
			return
		}
		_, err = f.store.StartRunStep(created.ID)
		must(err)
		if status != StepRunning {
			_, err = f.store.FinishRunStep(created.ID, status, "done", "", nil)
			must(err)
		}
	}
	affinity := func(repositoryID, repo string) map[string]string {
		return map[string]string{openPilotRepositoryIDInputKey: repositoryID, openPilotExecutorGenerationInputKey: "gen-1", "repo": repo, "issue": "7", "api_key": sessionViewSecret, "prompt": "run prompt text"}
	}

	f.now = f.now.Add(-3 * time.Hour)
	done := create("done", "devlog", map[string]string{"topic": "devlog"}, true)
	step(done, 1, map[string]string{"opencode_session_id": "ses_done", "opencode_request_id": "req-done", "opencode_executor_backend": "native"}, StepSucceeded)
	_, err := f.store.FinishRun(done.ID, RunSucceeded, "ok")
	must(err)
	legacy := create("legacy", "devlog", map[string]string{"topic": "legacy"}, true)
	step(legacy, 1, map[string]string{"ocq_session_id": "ocq-legacy", "ocq_request_id": "ocq-req-1"}, StepSucceeded)
	_, err = f.store.FinishRun(legacy.ID, RunSucceeded, "ok")
	must(err)

	f.now = f.now.Add(2 * time.Hour)
	alpha := create("alpha", "agent-work", affinity("alpha", "acme/alpha"), true)
	step(alpha, 1, map[string]string{"opencode_session_id": "ses_alpha", "opencode_request_id": "req-alpha", "model_broker_request_id": "mb-alpha", "opencode_executor_backend": "native"}, StepRunning)
	gone := create("gone", "agent-work", affinity("alpha", "acme/alpha"), true)
	step(gone, 1, map[string]string{"opencode_session_id": "ses_gone", "opencode_request_id": "req-gone"}, StepRunning)
	beta := create("beta", "agent-work", affinity("beta", "acme/beta"), true)
	step(beta, 1, map[string]string{"opencode_session_id": "ses_beta", "opencode_request_id": "req-beta"}, StepRunning)
	f.now = f.now.Add(2 * time.Minute)
	f.prober = &sessionViewProber{results: map[string]ExecutorSessionProbe{}}
	executorSeen := f.now.Add(time.Minute)
	f.prober.results["ses_alpha"] = ExecutorSessionProbe{Presence: ExecutorSessionPresent, Reason: "executor holds this session", Activity: "busy", ObservedAt: &executorSeen}
	f.prober.results["ses_gone"] = ExecutorSessionProbe{Presence: ExecutorSessionMissing, Reason: "executor no longer has this session"}
	f.now = f.now.Add(3 * time.Minute)
	return f
}

func (f *sessionViewFixture) api(defaultStatus OpenCodeExecutorStatus, repositories OpenPilotRepositoryStatus) *API {
	api := NewAPI(f.store, NewRunner(f.store))
	api.clock = func() time.Time { return f.now }
	api.openCode = sessionViewOpenCodeStatus{status: defaultStatus}
	api.openPilotRepositories = sessionViewRepositoryStatus{status: repositories}
	api.executorSessionProber = f.prober
	return api
}

func healthyDefaultExecutor() OpenCodeExecutorStatus {
	return OpenCodeExecutorStatus{ID: "switchyard-opencode-executor", Status: "healthy", Configured: true, Healthy: true, ConfiguredSelector: "native", BaseURL: "http://opencode-default:4096", Sessions: []OpenCodeExecutorStatusSession{}}
}

func partialRepositoryStatus() OpenPilotRepositoryStatus {
	return OpenPilotRepositoryStatus{ActivationMode: "registry", Configured: true, Repositories: []OpenPilotRepositoryExecutorStatus{
		{RepositoryID: "alpha", RepositorySlug: "acme/alpha", ExecutorGeneration: "gen-1", Healthy: true, OpenCode: OpenCodeExecutorStatus{Status: "healthy", Healthy: true, Configured: true, BaseURL: "http://opencode-alpha:4096"}},
		{RepositoryID: "beta", RepositorySlug: "acme/beta", ExecutorGeneration: "gen-1", OpenCode: OpenCodeExecutorStatus{Status: "unreachable", Configured: true, BaseURL: "http://opencode-beta:4096", Error: "dial tcp 10.9.9.9:4096: connect: connection refused password=hunter2"}},
	}}
}

func sessionViewRequest(t *testing.T, handler http.Handler, target, token string) (*httptest.ResponseRecorder, ExecutorSessionsResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body ExecutorSessionsResponse
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v: %s", err, response.Body.String())
		}
	}
	return response, body
}

func sessionByID(t *testing.T, body ExecutorSessionsResponse, id string) ExecutorSessionView {
	t.Helper()
	for _, session := range body.Sessions {
		if session.SessionID == id {
			return session
		}
	}
	t.Fatalf("session %s missing from %+v", id, body.Sessions)
	return ExecutorSessionView{}
}

func TestExecutorSessionsViewAcrossRepositoriesWithPartialUpstreamFailure(t *testing.T) {
	f := newSessionViewFixture(t)
	response, body := sessionViewRequest(t, f.api(healthyDefaultExecutor(), partialRepositoryStatus()).Handler(), "/api/executor-sessions", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if body.ExecutorAvailability != ExecutorAvailabilityPartial || len(body.Executors) != 3 {
		t.Fatalf("availability = %q executors = %+v", body.ExecutorAvailability, body.Executors)
	}
	if body.Counts != (ExecutorSessionCounts{Total: 5, Active: 2, Stale: 1, Ended: 1, HistoricalCompatibility: 1}) {
		t.Fatalf("counts = %+v", body.Counts)
	}
	if body.Abort.DirectAbort || body.Abort.Route != "POST /api/runs/{id}/cancel" || !body.Abort.AdminRequired {
		t.Fatalf("abort policy = %+v", body.Abort)
	}

	alpha := sessionByID(t, body, "ses_alpha")
	if alpha.Status != ExecutorSessionStatusActive || alpha.Presence != ExecutorSessionPresent || alpha.ExecutorActivity != "busy" || alpha.ExecutorKey != "alpha/gen-1" || alpha.ExecutorStatus != "healthy" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if alpha.Repository != (ExecutorSessionRepository{RepositoryID: "alpha", ExecutorGeneration: "gen-1", Repo: "acme/alpha"}) || alpha.Origin != ExecutorSessionOriginNative {
		t.Fatalf("alpha identity = %+v", alpha)
	}
	if alpha.LastObservedSource != "executor" || len(alpha.Runs) != 1 || alpha.Runs[0].RunID != f.runs["alpha"].ID || !alpha.Runs[0].Active || !alpha.Runs[0].Cancelable {
		t.Fatalf("alpha runs = %+v source = %s", alpha.Runs, alpha.LastObservedSource)
	}
	requests := map[string]string{}
	for _, request := range alpha.Requests {
		requests[request.Key] = request.Kind + ":" + request.Value
	}
	if requests["opencode_request_id"] != "executor_request:req-alpha" || requests["model_broker_request_id"] != "model_request:mb-alpha" {
		t.Fatalf("alpha requests = %+v", alpha.Requests)
	}

	gone := sessionByID(t, body, "ses_gone")
	if gone.Status != ExecutorSessionStatusStale || gone.Presence != ExecutorSessionMissing || !strings.Contains(gone.StatusReason, "no longer has this session") {
		t.Fatalf("stale session = %+v", gone)
	}

	beta := sessionByID(t, body, "ses_beta")
	if beta.ExecutorStatus != "unreachable" || beta.Presence != ExecutorSessionUnknown || beta.Status != ExecutorSessionStatusActive || beta.ExecutorKey != "beta/gen-1" {
		t.Fatalf("beta on failed upstream = %+v", beta)
	}
	for _, call := range f.prober.calls {
		if strings.Contains(call, "beta") {
			t.Fatalf("unavailable executor was probed: %v", f.prober.calls)
		}
	}

	done := sessionByID(t, body, "ses_done")
	if done.Status != ExecutorSessionStatusEnded || done.Active || done.ExecutorKey != ExecutorSessionDefaultKey || done.Presence != ExecutorSessionNotChecked {
		t.Fatalf("ended session = %+v", done)
	}
	legacy := sessionByID(t, body, "ocq-legacy")
	if legacy.Status != ExecutorSessionStatusHistoricalCompatibility || legacy.Origin != ExecutorSessionOriginCompatibility || legacy.CorrelationKey != "ocq_session_id" {
		t.Fatalf("compatibility session = %+v", legacy)
	}
	if body.Sessions[0].Active == false {
		t.Fatalf("active sessions must sort first: %+v", body.Sessions[0])
	}
}

func TestExecutorSessionsNeverExposeSecretsInputsOrEndpoints(t *testing.T) {
	f := newSessionViewFixture(t)
	response, _ := sessionViewRequest(t, f.api(healthyDefaultExecutor(), partialRepositoryStatus()).Handler(), "/api/executor-sessions", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	raw := response.Body.String()
	for _, forbidden := range []string{sessionViewSecret, "sk-proj", "hunter2", "prompt text", "step prompt", "opencode-alpha", "opencode-beta", "opencode-default", "dial tcp", "api_key", "base_url"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaks %q: %s", forbidden, raw)
		}
	}
}

func TestExecutorSessionsRouteReadOnlyAccessAndListenerBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	f := newSessionViewFixture(t)
	api := f.api(healthyDefaultExecutor(), partialRepositoryStatus())
	control := api.ControlPlaneHandler()
	if response, _ := sessionViewRequest(t, control, "/api/executor-sessions", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
	response, body := sessionViewRequest(t, control, "/api/executor-sessions", "read-token")
	if response.Code != http.StatusOK || body.Counts.Total != 5 {
		t.Fatalf("read token status = %d body = %s", response.Code, response.Body.String())
	}
	if response, _ := sessionViewRequest(t, control, "/api/executor-sessions", "admin-token"); response.Code != http.StatusOK {
		t.Fatalf("admin token status = %d", response.Code)
	}
	// A read-only operator cannot reach the only abort path: run cancellation.
	cancel := httptest.NewRequest(http.MethodPost, "/api/runs/"+f.runs["alpha"].ID+"/cancel", nil)
	cancel.Header.Set("Authorization", "Bearer read-token")
	cancelResponse := httptest.NewRecorder()
	control.ServeHTTP(cancelResponse, cancel)
	if cancelResponse.Code != http.StatusForbidden {
		t.Fatalf("read-token run cancel status = %d, want 403", cancelResponse.Code)
	}
	if run, err := f.store.RunDetail(f.runs["alpha"].ID); err != nil || run.Run.Status != RunRunning {
		t.Fatalf("read-token cancel changed the run: %+v %v", run.Run, err)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		request := httptest.NewRequest(method, "/api/executor-sessions", nil)
		request.Header.Set("Authorization", "Bearer admin-token")
		recorder := httptest.NewRecorder()
		control.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/executor-sessions status = %d, want 405", method, recorder.Code)
		}
	}
	model := api.ModelIngressHandler()
	if response, _ := sessionViewRequest(t, model, "/api/executor-sessions", "admin-token"); response.Code != http.StatusNotFound {
		t.Fatalf("model listener status = %d, want 404", response.Code)
	}
}

func TestExecutorSessionsEmptyListNeverImpliesHealth(t *testing.T) {
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	api.openCode = sessionViewOpenCodeStatus{status: OpenCodeExecutorStatus{Status: "unreachable", Configured: true, Sessions: []OpenCodeExecutorStatusSession{}}}
	api.openPilotRepositories = sessionViewRepositoryStatus{status: OpenPilotRepositoryStatus{Repositories: []OpenPilotRepositoryExecutorStatus{}, Error: "Open Pilot repository status is unavailable"}}
	api.executorSessionProber = &sessionViewProber{}
	response, body := sessionViewRequest(t, api.Handler(), "/api/executor-sessions", "")
	if response.Code != http.StatusOK || len(body.Sessions) != 0 || body.Sessions == nil {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if body.ExecutorAvailability != ExecutorAvailabilityUnavailable {
		t.Fatalf("availability = %q", body.ExecutorAvailability)
	}
	joined := strings.Join(body.Warnings, " ")
	if !strings.Contains(joined, "does not mean the executor is idle or healthy") || !strings.Contains(joined, "repository executor status is unavailable") {
		t.Fatalf("warnings = %v", body.Warnings)
	}

	api.openCode = sessionViewOpenCodeStatus{status: OpenCodeExecutorStatus{Status: "not_configured", Sessions: []OpenCodeExecutorStatusSession{}}}
	api.openPilotRepositories = sessionViewRepositoryStatus{status: OpenPilotRepositoryStatus{Repositories: []OpenPilotRepositoryExecutorStatus{}}}
	_, body = sessionViewRequest(t, api.Handler(), "/api/executor-sessions", "")
	if body.ExecutorAvailability != ExecutorAvailabilityNotConfigured || len(body.Warnings) != 1 {
		t.Fatalf("not configured availability = %q warnings = %v", body.ExecutorAvailability, body.Warnings)
	}

	api.openCode = sessionViewOpenCodeStatus{status: healthyDefaultExecutor()}
	_, body = sessionViewRequest(t, api.Handler(), "/api/executor-sessions", "")
	if body.ExecutorAvailability != ExecutorAvailabilityAvailable || len(body.Warnings) != 0 {
		t.Fatalf("healthy empty availability = %q warnings = %v", body.ExecutorAvailability, body.Warnings)
	}
}

func TestExecutorSessionsFiltersAndBounds(t *testing.T) {
	f := newSessionViewFixture(t)
	handler := f.api(healthyDefaultExecutor(), partialRepositoryStatus()).Handler()
	_, body := sessionViewRequest(t, handler, "/api/executor-sessions?session_id=ses_beta", "")
	if len(body.Sessions) != 1 || body.Sessions[0].SessionID != "ses_beta" || body.SessionFilter != "ses_beta" || body.ScannedSteps != 1 {
		t.Fatalf("session filter = %+v", body)
	}
	_, body = sessionViewRequest(t, handler, "/api/executor-sessions?repository=alpha", "")
	if body.Counts.Total != 2 {
		t.Fatalf("repository filter = %+v", body.Sessions)
	}
	_, body = sessionViewRequest(t, handler, "/api/executor-sessions?repository=default", "")
	if body.Counts.Total != 2 || body.Sessions[0].ExecutorKey != ExecutorSessionDefaultKey {
		t.Fatalf("default filter = %+v", body.Sessions)
	}
	_, body = sessionViewRequest(t, handler, "/api/executor-sessions?limit=1", "")
	if len(body.Sessions) != 1 || !body.Truncated || body.Limit != 1 {
		t.Fatalf("limit = %+v", body)
	}
	for _, target := range []string{
		"/api/executor-sessions?limit=0",
		"/api/executor-sessions?limit=101",
		"/api/executor-sessions?session_id=" + strings.Repeat("a", executorSessionMaxIDLength+1),
		"/api/executor-sessions?session_id=bad%2Fid",
		"/api/executor-sessions?repository=" + strings.Repeat("r", evidenceSearchMaxFilterLength+1),
	} {
		if response, _ := sessionViewRequest(t, handler, target, ""); response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", target, response.Code)
		}
	}
}

func TestExecutorSessionsRetentionFloorAndLegacySingletonRouting(t *testing.T) {
	f := newSessionViewFixture(t)
	api := f.api(healthyDefaultExecutor(), OpenPilotRepositoryStatus{ActivationMode: "legacy-singleton", Repositories: []OpenPilotRepositoryExecutorStatus{}})
	api.SetRetentionManager(NewRetentionManager(f.store, 2*time.Hour, time.Hour, api.clock))
	_, body := sessionViewRequest(t, api.Handler(), "/api/executor-sessions", "")
	for _, session := range body.Sessions {
		if session.SessionID == "ses_done" || session.SessionID == "ocq-legacy" {
			t.Fatalf("record older than the retention floor was returned: %+v", session)
		}
		// Legacy-singleton runs keep affinity inputs but execute on the default executor.
		if session.ExecutorKey != ExecutorSessionDefaultKey {
			t.Fatalf("legacy singleton session routed to %q", session.ExecutorKey)
		}
	}
	if body.Retention.Window != (2*time.Hour).String() || body.Counts.Total != 3 {
		t.Fatalf("retention = %+v counts = %+v", body.Retention, body.Counts)
	}
}

func TestClassifyExecutorSessionStaleness(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	active := func(run ExecutorSessionRun, presence string, observed time.Time) ExecutorSessionView {
		run.Active = true
		return ExecutorSessionView{Active: true, Origin: ExecutorSessionOriginNative, Presence: presence, LastObservedAt: observed, Runs: []ExecutorSessionRun{run}}
	}
	cases := []struct {
		name    string
		session ExecutorSessionView
		status  string
		reason  string
	}{
		{"run finished", active(ExecutorSessionRun{RunStatus: RunFailed}, ExecutorSessionPresent, now), ExecutorSessionStatusStale, "after its run finished"},
		{"lease expired", active(ExecutorSessionRun{RunStatus: RunRunning, LeaseExpires: &expired}, ExecutorSessionUnknown, now), ExecutorSessionStatusStale, "lease expired"},
		{"unobserved", active(ExecutorSessionRun{RunStatus: RunRunning}, ExecutorSessionUnknown, now.Add(-2*time.Hour)), ExecutorSessionStatusStale, "no observation"},
		{"present old", active(ExecutorSessionRun{RunStatus: RunRunning}, ExecutorSessionPresent, now.Add(-2*time.Hour)), ExecutorSessionStatusActive, "running or queued"},
		{"compat active", ExecutorSessionView{Active: true, Origin: ExecutorSessionOriginCompatibility, Presence: ExecutorSessionNotChecked, LastObservedAt: now, Runs: []ExecutorSessionRun{{RunStatus: RunRunning, Active: true}}}, ExecutorSessionStatusActive, "running or queued"},
	}
	for _, tc := range cases {
		session := tc.session
		classifyExecutorSession(&session, now, time.Hour)
		if session.Status != tc.status || !strings.Contains(session.StatusReason, tc.reason) {
			t.Errorf("%s: status = %s reason = %s", tc.name, session.Status, session.StatusReason)
		}
	}
}

func TestExecutorSessionProbeBudgetIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	sessions := make([]ExecutorSessionView, executorSessionMaxProbes+4)
	prober := &sessionViewProber{results: map[string]ExecutorSessionProbe{}}
	for index := range sessions {
		id := "ses_" + strings.Repeat("x", index+1)
		prober.results[id] = ExecutorSessionProbe{Presence: ExecutorSessionPresent}
		sessions[index] = ExecutorSessionView{SessionID: id, Active: true, Origin: ExecutorSessionOriginNative, ExecutorKey: ExecutorSessionDefaultKey, LastObservedAt: now}
	}
	probeSessions(context.Background(), sessions, map[string]ExecutorAvailability{ExecutorSessionDefaultKey: {Key: ExecutorSessionDefaultKey, Status: "healthy", Available: true}}, prober)
	if len(prober.calls) != executorSessionMaxProbes {
		t.Fatalf("probes = %d, want %d", len(prober.calls), executorSessionMaxProbes)
	}
	if last := sessions[len(sessions)-1]; last.Presence != ExecutorSessionNotChecked || !strings.Contains(last.PresenceReason, "budget") {
		t.Fatalf("over-budget session = %+v", last)
	}
}

func TestEnvExecutorSessionProberReadsMetadataOnly(t *testing.T) {
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		sawAuth = sawAuth || (ok && user == "switchyard" && password == "route-password")
		if r.URL.Query().Get("limit") != "1" {
			t.Errorf("probe query = %s", r.URL.RawQuery)
		}
		switch r.URL.Path {
		case "/session/ses_gone/message":
			http.NotFound(w, r)
		case "/session/ses_busy/message":
			_, _ = w.Write([]byte(`[{"info":{"id":"m1","role":"user","time":{"created":1790000000000}},"parts":[{"type":"text","text":"secret prompt"}]}]`))
		case "/session/ses_idle/message":
			_, _ = w.Write([]byte(`[{"info":{"id":"m2","role":"assistant","finish":"stop","time":{"created":1790000000000,"completed":1790000005000}},"parts":[{"type":"text","text":"answer"}]}]`))
		case "/session/ses_empty/message":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	prober := &envExecutorSessionProber{client: server.Client(), routes: map[string]executorSessionRoute{
		ExecutorSessionDefaultKey: {baseURL: server.URL, password: "route-password", native: true},
		"legacy/gen":              {baseURL: server.URL, native: false},
	}}
	ctx := context.Background()
	if probe := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "ses_gone"); probe.Presence != ExecutorSessionMissing {
		t.Fatalf("missing probe = %+v", probe)
	}
	busy := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "ses_busy")
	if busy.Presence != ExecutorSessionPresent || busy.Activity != "busy" || busy.ObservedAt == nil || strings.Contains(busy.Reason, "secret") {
		t.Fatalf("busy probe = %+v", busy)
	}
	idle := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "ses_idle")
	if idle.Presence != ExecutorSessionPresent || idle.Activity != "idle" || !idle.ObservedAt.Equal(time.UnixMilli(1790000005000).UTC()) {
		t.Fatalf("idle probe = %+v", idle)
	}
	if empty := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "ses_empty"); empty.Presence != ExecutorSessionPresent || empty.Activity != "idle" {
		t.Fatalf("empty probe = %+v", empty)
	}
	if failed := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "ses_error"); failed.Presence != ExecutorSessionUnknown || failed.Reason != "executor returned HTTP 500" {
		t.Fatalf("failed probe = %+v", failed)
	}
	if !sawAuth {
		t.Fatal("probe did not authenticate with the route credentials")
	}
	if compat := prober.ProbeSession(ctx, "legacy/gen", "ses_idle"); compat.Presence != ExecutorSessionNotChecked {
		t.Fatalf("compatibility route probe = %+v", compat)
	}
	if unknown := prober.ProbeSession(ctx, "missing/gen", "ses_idle"); unknown.Presence != ExecutorSessionUnknown || !strings.Contains(unknown.Reason, "no executor route") {
		t.Fatalf("unknown route probe = %+v", unknown)
	}
	if invalid := prober.ProbeSession(ctx, ExecutorSessionDefaultKey, "../etc"); invalid.Presence != ExecutorSessionUnknown {
		t.Fatalf("invalid id probe = %+v", invalid)
	}
}
