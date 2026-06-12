package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestAPIListsWorkflowsAndRunsTrigger(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/workflows")
	if err != nil {
		t.Fatalf("GET workflows error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var workflows struct {
		Workflows []controlplane.Workflow `json:"workflows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&workflows); err != nil {
		t.Fatalf("decode workflows: %v", err)
	}
	if len(workflows.Workflows) != 3 {
		t.Fatalf("len(workflows) = %d, want 3", len(workflows.Workflows))
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/triggers/manual-devlog/run", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST trigger run error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.WorkflowID != "devlog" || body.Run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIRunsOpenPilotWorkflow(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/workflows/open-pilot/run", "application/json", bytes.NewBufferString(`{"repo":"OWNER/REPO","issue":"123"}`))
	if err != nil {
		t.Fatalf("POST workflow error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.WorkflowID != "open-pilot" || body.Run.Inputs["repo"] != "OWNER/REPO" || body.Run.Inputs["issue"] != "123" {
		t.Fatalf("run = %+v", body.Run)
	}
}

type fakeCanceler struct{ called bool }

func (f *fakeCanceler) Cancel(context.Context, controlplane.Run) controlplane.CancelResult {
	f.called = true
	return controlplane.CancelResult{Events: []controlplane.WorkflowExecutionEvent{{Type: "fake.cancel", Message: "called"}}}
}

func TestAPIUpdatesTriggerEnabled(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPatch, srv.URL+"/api/triggers/daily-changemaker", bytes.NewBufferString(`{"enabled":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH trigger error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	trigger, ok := store.Trigger("daily-changemaker")
	if !ok || !trigger.Enabled {
		t.Fatalf("trigger = %+v ok=%v", trigger, ok)
	}
}

func TestAPIRetryAndCancelRun(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	canceler := &fakeCanceler{}
	api.SetCancelDispatcher(canceler)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot", TriggerID: "manual-open-pilot"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/cancel", nil)
	if err != nil {
		t.Fatalf("new cancel request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
	if !canceler.called {
		t.Fatal("canceler was not called")
	}

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if body.Run.ID == run.ID || body.Run.TriggerID != run.TriggerID || body.Run.Status != controlplane.RunSucceeded {
		t.Fatalf("retry run = %+v original = %+v", body.Run, run)
	}
}

func TestAPIRunStepsListRetryAndCancel(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	failed, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "generate", Executor: "model"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if _, err := store.StartRunStep(failed.ID); err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}
	if _, err := store.FinishRunStep(failed.ID, controlplane.StepFailed, "", "boom", nil); err != nil {
		t.Fatalf("FinishRunStep() error = %v", err)
	}
	queued, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 2, Name: "attach", Executor: "internal"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store, controlplane.MapExecutorRegistry{}))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/runs/" + run.ID + "/steps")
	if err != nil {
		t.Fatalf("GET steps error = %v", err)
	}
	defer resp.Body.Close()
	var listBody struct {
		Steps []controlplane.RunStep `json:"steps"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode steps: %v", err)
	}
	if len(listBody.Steps) != 2 || listBody.Steps[0].ID != failed.ID || listBody.Steps[1].ID != queued.ID {
		t.Fatalf("steps = %+v", listBody.Steps)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+failed.ID+"/retry", nil)
	if err != nil {
		t.Fatalf("new retry request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST retry error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodPost, srv.URL+"/api/runs/"+run.ID+"/steps/"+queued.ID+"/cancel", nil)
	if err != nil {
		t.Fatalf("new cancel request: %v", err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cancel status = %d", resp.StatusCode)
	}
}

func TestAPISchedulerStatusDisabledByDefault(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/scheduler")
	if err != nil {
		t.Fatalf("GET scheduler error = %v", err)
	}
	defer resp.Body.Close()
	var body controlplane.SchedulerStatus
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode scheduler: %v", err)
	}
	if body.Enabled || body.Running {
		t.Fatalf("scheduler = %+v", body)
	}
}

func TestAPIAsyncDispatchEnqueuesRun(t *testing.T) {
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	api := controlplane.NewAPI(store, controlplane.NewRunner(store))
	api.SetAsyncDispatch(true)
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL+"/api/runs", "application/json", bytes.NewBufferString(`{"workflow_id":"devlog"}`))
	if err != nil {
		t.Fatalf("POST runs error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Location") == "" {
		t.Fatal("Location header missing")
	}
	var body struct {
		Run controlplane.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if body.Run.Status != controlplane.RunQueued || body.Run.StartedAt != nil {
		t.Fatalf("run = %+v", body.Run)
	}
}

func TestAPIWorkerStatusDisabledByDefault(t *testing.T) {
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/workers")
	if err != nil {
		t.Fatalf("GET workers error = %v", err)
	}
	defer resp.Body.Close()
	var body controlplane.WorkerPoolStatus
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode workers: %v", err)
	}
	if body.Enabled || body.Running {
		t.Fatalf("workers = %+v", body)
	}
}

func TestAPIAdaptersEndpointShape(t *testing.T) {
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", "")
	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body struct {
		Queues      []controlplane.AdapterQueue     `json:"queues"`
		Sessions    []controlplane.AdapterSession   `json:"sessions"`
		Logs        []controlplane.AdapterLog       `json:"logs"`
		Prompts     []controlplane.AdapterPrompt    `json:"prompts"`
		Config      controlplane.AdapterConfig      `json:"config"`
		Agents      []controlplane.AdapterAgent     `json:"agents"`
		Skills      []controlplane.AdapterSkill     `json:"skills"`
		Health      controlplane.AdapterHealth      `json:"health"`
		LlamaLine   controlplane.AdapterLlamaLine   `json:"llama_line"`
		OCQ         controlplane.AdapterOCQ         `json:"ocq"`
		Blacktower  controlplane.AdapterBlacktower  `json:"blacktower"`
		Changemaker controlplane.AdapterChangemaker `json:"changemaker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Queues) != 1 || body.Queues[0].Status != "healthy" {
		t.Fatalf("queues = %+v", body.Queues)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Surface != "sessions" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if len(body.Logs) != 1 || body.Logs[0].Level != "info" {
		t.Fatalf("logs = %+v", body.Logs)
	}
	if len(body.Prompts) != 1 || body.Prompts[0].Name == "" {
		t.Fatalf("prompts = %+v", body.Prompts)
	}
	if body.Config.Environment != "development" || len(body.Config.Features) == 0 {
		t.Fatalf("config = %+v", body.Config)
	}
	if len(body.Agents) != 1 || len(body.Skills) != 1 {
		t.Fatalf("agents/skills = %+v %+v", body.Agents, body.Skills)
	}
	if body.Health.Status != "healthy" || len(body.Health.Services) != 1 {
		t.Fatalf("health = %+v", body.Health)
	}
	if len(body.LlamaLine.Requests) != 1 || body.LlamaLine.Stats.ActiveRequests == 0 || len(body.LlamaLine.Upstreams) != 1 {
		t.Fatalf("llama_line = %+v", body.LlamaLine)
	}
	if len(body.OCQ.Requests) != 1 || len(body.OCQ.Sessions) != 1 || body.OCQ.Settings.Provider == "" {
		t.Fatalf("ocq = %+v", body.OCQ)
	}
	if len(body.Blacktower.Tasks) != 1 || len(body.Blacktower.Reports) != 1 || body.Blacktower.SchedulerHealth.Status != "healthy" {
		t.Fatalf("blacktower = %+v", body.Blacktower)
	}
	if len(body.Changemaker.Posts) != 1 || !body.Changemaker.Service.Healthy || !body.Changemaker.RunControl.Enabled {
		t.Fatalf("changemaker = %+v", body.Changemaker)
	}
}

func TestAPIAdaptersProbesOCQWhenConfigured(t *testing.T) {
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/ocq/sessions":
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"sessions":[{"id":"ses_alpha","title":"Alpha","status":"active","user":"operator","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:01:00Z"}]}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OCQ_GATEWAY_KEY", "test-key")

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.OCQ.Sessions) != 1 || body.OCQ.Sessions[0].ID != "ses_alpha" {
		t.Fatalf("ocq sessions = %+v", body.OCQ.Sessions)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Surface != "ocq" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
	if body.OCQ.Settings.Workspace != ocq.URL {
		t.Fatalf("settings = %+v", body.OCQ.Settings)
	}
}

func TestAPIAdaptersProbesLlamaLineWhenConfigured(t *testing.T) {
	llama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/broker/status" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"queue_depth":2,
			"max_depth":25,
			"in_flight_client":"operator",
			"connected_clients":3,
			"upstreams":[{"name":"ocq","url":"http://ocq:8088","healthy":true,"last_probe_at":"2026-01-01T00:00:00Z"}]
		}`))
	}))
	t.Cleanup(llama.Close)
	t.Setenv("LLAMA_LINE_BASE_URL", llama.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Queues) != 1 || body.Queues[0].ID != "llama-line" || body.Queues[0].Depth != 2 {
		t.Fatalf("queues = %+v", body.Queues)
	}
	if len(body.LlamaLine.Upstreams) != 1 || body.LlamaLine.Upstreams[0].Name != "ocq" || body.LlamaLine.Upstreams[0].Status != "healthy" {
		t.Fatalf("upstreams = %+v", body.LlamaLine.Upstreams)
	}
	if body.LlamaLine.Stats.ActiveRequests != 1 || body.LlamaLine.Stats.QueuedRequests != 2 {
		t.Fatalf("stats = %+v", body.LlamaLine.Stats)
	}
}

func TestAPIAdaptersProbesBlacktowerWhenConfigured(t *testing.T) {
	blacktower := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/snapshot" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"tasks":[{"name":"observe","badges":["daily"],"warnings":[],"latestRun":{"id":"run_bt_1","status":"completed","startedAt":"2026-01-01T00:00:00Z","completedAt":"2026-01-01T00:02:00Z","sessionId":"ses_bt_1"}}],
			"selectedTask":{"report":{"path":"/tmp/report.md","updatedAt":"2026-01-01T00:03:00Z","content":"# Scheduler health\n- no failed tasks\n- queue lag nominal"}},
			"health":{"status":"ok","summary":"scheduler ok","tasks":{"total":1,"enabled":1,"invalid":0},"recentFailureCount":0,"checkedAt":"2026-01-01T00:04:00Z"},
			"schedulerEvents":[{"source":"scheduler","severity":"info","text":"scheduler tick","taskName":"observe","timestamp":"2026-01-01T00:05:00Z"}],
			"generatedAt":"2026-01-01T00:06:00Z"
		}`))
	}))
	t.Cleanup(blacktower.Close)
	t.Setenv("BLACKTOWER_BASE_URL", blacktower.URL)

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if len(body.Blacktower.Tasks) != 1 || body.Blacktower.Tasks[0].Name != "observe" || body.Blacktower.Tasks[0].Progress != 100 {
		t.Fatalf("blacktower tasks = %+v", body.Blacktower.Tasks)
	}
	if len(body.Blacktower.Reports) != 1 || body.Blacktower.Reports[0].Title != "Scheduler health" {
		t.Fatalf("blacktower reports = %+v", body.Blacktower.Reports)
	}
	if body.Blacktower.SchedulerHealth.Status != "healthy" {
		t.Fatalf("scheduler health = %+v", body.Blacktower.SchedulerHealth)
	}
	if len(body.Sessions) < 2 || body.Sessions[len(body.Sessions)-1].Surface != "blacktower" {
		t.Fatalf("sessions = %+v", body.Sessions)
	}
}

func TestAPIAdaptersProbesChangemakerWhenConfigured(t *testing.T) {
	changemaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/runs/status":
			if got := r.Header.Get("Authorization"); got != "Bearer changemaker-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"state":"running","startedAt":"2026-01-01T00:00:00Z","logs":["started","collecting changes"]}`))
		case "/api/posts":
			if got := r.Header.Get("Authorization"); got != "Bearer changemaker-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_, _ = w.Write([]byte(`[{"fileName":"2026-01-01-post.md","date":"2026-01-01","title":"Field report"}]`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(changemaker.Close)
	t.Setenv("CHANGEMAKER_BASE_URL", changemaker.URL)
	t.Setenv("CHANGEMAKER_ADMIN_TOKEN", "changemaker-token")

	api := controlplane.NewAPI(controlplane.NewStore(nil), controlplane.NewRunner(controlplane.NewStore(nil)))
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/adapters")
	if err != nil {
		t.Fatalf("GET adapters error = %v", err)
	}
	defer resp.Body.Close()

	var body controlplane.AdapterPayload
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode adapters: %v", err)
	}
	if !body.Changemaker.Service.Healthy || body.Changemaker.Service.Status != "healthy" {
		t.Fatalf("changemaker service = %+v", body.Changemaker.Service)
	}
	if body.Changemaker.RunControl.Mode != "running" || body.Changemaker.RunControl.ActiveRunID == "" {
		t.Fatalf("run control = %+v", body.Changemaker.RunControl)
	}
	if len(body.Changemaker.Posts) != 1 || body.Changemaker.Posts[0].Title != "Field report" {
		t.Fatalf("posts = %+v", body.Changemaker.Posts)
	}
}
