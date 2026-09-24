package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"
)

func runtimeConfigRequest(t *testing.T, handler http.Handler, method, path, token, body string) (*httptest.ResponseRecorder, RuntimeConfigResponse) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var payload RuntimeConfigResponse
	if method == http.MethodGet && response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode runtime config: %v", err)
		}
	}
	return response, payload
}

func settingByKey(t *testing.T, payload RuntimeConfigResponse, key string) RuntimeConfigSetting {
	t.Helper()
	for _, setting := range payload.Settings {
		if setting.Key == key {
			return setting
		}
	}
	t.Fatalf("setting %s missing from response", key)
	return RuntimeConfigSetting{}
}

func TestRuntimeConfigRedactsSecretsByClassification(t *testing.T) {
	const adminToken = "admin-token-value-0123456789"
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", adminToken)
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "gitea-secret-value-abcdef")
	t.Setenv("DATABASE_URL", "postgres://switchyard:database-password-xyz@postgres/switchyard")
	t.Setenv("OPENCODE_SERVER_PASSWORD", "opencode-password-value")
	t.Setenv("SWITCHYARD_WEBHOOK_SECRET_DIR", "/run/switchyard/webhook-secrets")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))

	response, payload := runtimeConfigRequest(t, api.ControlPlaneHandler(), http.MethodGet, "/api/runtime-config", adminToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	for _, leaked := range []string{adminToken, "gitea-secret-value-abcdef", "database-password-xyz", "opencode-password-value"} {
		if strings.Contains(response.Body.String(), leaked) {
			t.Fatalf("runtime config leaked a secret value: %s", leaked)
		}
	}
	for _, setting := range payload.Settings {
		if runtimeconfig.SecretKeyName(setting.Key) || setting.Sensitivity == runtimeconfig.SensitivitySecret {
			if setting.Effective != nil || setting.Default != nil || setting.ExampleDefault != nil {
				t.Errorf("%s exposes a value despite secret classification: %+v", setting.Key, setting)
			}
			if setting.Configured == nil {
				t.Errorf("%s omits the configured flag", setting.Key)
			}
		}
	}
	for _, key := range []string{"GITEA_TOKEN", "DATABASE_URL", "SWITCHYARD_CONTROL_API_TOKEN", "OPENCODE_SERVER_PASSWORD"} {
		setting := settingByKey(t, payload, key)
		if setting.Configured == nil || !*setting.Configured || setting.Source != runtimeConfigSourceEnvironment {
			t.Errorf("%s should report configured from the environment: %+v", key, setting)
		}
	}
	readToken := settingByKey(t, payload, "SWITCHYARD_CONTROL_READ_TOKEN")
	if readToken.Configured == nil || *readToken.Configured || readToken.Source != runtimeConfigSourceUnset {
		t.Errorf("unset secret should report unset: %+v", readToken)
	}
	secretDir := settingByKey(t, payload, "SWITCHYARD_WEBHOOK_SECRET_DIR")
	if secretDir.Effective == nil || *secretDir.Effective != "/run/switchyard/webhook-secrets" {
		t.Errorf("secret directory path is a location, not a credential: %+v", secretDir)
	}
}

func TestRuntimeConfigReportsEffectiveValuesAndDrift(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_WORKER_COUNT", "4")
	t.Setenv("SWITCHYARD_RETENTION", "")
	t.Setenv("MODEL_BROKER_TIMEOUT", "10m")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	response, payload := runtimeConfigRequest(t, api.ControlPlaneHandler(), http.MethodGet, "/api/runtime-config", "admin-token", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if payload.ContractVersion != 1 || payload.Revision != 0 || len(payload.Changes) != 0 {
		t.Fatalf("unexpected envelope: version=%d revision=%d changes=%d", payload.ContractVersion, payload.Revision, len(payload.Changes))
	}
	if len(payload.Settings) != len(runtimeconfig.Inventory()) {
		t.Fatalf("settings = %d, want the full inventory %d", len(payload.Settings), len(runtimeconfig.Inventory()))
	}
	workers := settingByKey(t, payload, "SWITCHYARD_WORKER_COUNT")
	if workers.Source != runtimeConfigSourceEnvironment || workers.Effective == nil || *workers.Effective != "4" || workers.Default == nil || *workers.Default != "1" || workers.Overridden == nil || !*workers.Overridden {
		t.Errorf("environment override not reported: %+v", workers)
	}
	retention := settingByKey(t, payload, "SWITCHYARD_RETENTION")
	if retention.Source != runtimeConfigSourceDefault || retention.Effective == nil || *retention.Effective != "720h" || retention.Overridden == nil || *retention.Overridden {
		t.Errorf("default fallback not reported: %+v", retention)
	}
	timeout := settingByKey(t, payload, "MODEL_BROKER_TIMEOUT")
	if timeout.Source != runtimeConfigSourceEnvironment || timeout.Overridden == nil || *timeout.Overridden {
		t.Errorf("environment value equal to the default must not count as drift: %+v", timeout)
	}
	modelListener := settingByKey(t, payload, "LLAMA_LINE_LISTEN_ADDR")
	if modelListener.DeploymentOverride == "" || modelListener.ComposeDefault == nil || *modelListener.ComposeDefault != ":11434" {
		t.Errorf("compose drift against the runtime default must be reported: %+v", modelListener)
	}
	port := settingByKey(t, payload, "API_PORT")
	if port.Source != runtimeConfigSourceNotObservable || port.Effective != nil || !strings.Contains(port.Instructions, "cannot observe") {
		t.Errorf("compose-only key must be marked not observable: %+v", port)
	}
	fileManaged := settingByKey(t, payload, "SWITCHYARD_SCHEDULER_INTERVAL")
	if fileManaged.Mutability != runtimeconfig.MutabilityFileManaged || !strings.Contains(fileManaged.Instructions, "SWITCHYARD_ENV_FILE") || !strings.Contains(fileManaged.Instructions, "systemctl reload switchyard") {
		t.Errorf("file-managed instructions missing: %+v", fileManaged)
	}
	paused := settingByKey(t, payload, "scheduler.dispatch_paused")
	if paused.Source != runtimeConfigSourceUnavailable || paused.Available == nil || *paused.Available || paused.Effective != nil {
		t.Errorf("runtime-mutable setting without a scheduler must be unavailable: %+v", paused)
	}
}

func TestRuntimeConfigListenerAndAuthorizationBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	api.SetScheduler(NewScheduler(store, NewRunner(store), time.Minute, nil))
	body := `{"value":true,"expected_revision":0}`
	for _, test := range []struct {
		name    string
		handler http.Handler
		method  string
		path    string
		token   string
		body    string
		want    int
	}{
		{name: "model listener hides the read route", handler: api.ModelIngressHandler(), method: http.MethodGet, path: "/api/runtime-config", token: "admin-token", want: http.StatusNotFound},
		{name: "model listener hides the write route", handler: api.ModelIngressHandler(), method: http.MethodPut, path: "/api/runtime-config/scheduler.dispatch_paused", token: "admin-token", body: body, want: http.StatusNotFound},
		{name: "control read requires authentication", handler: api.ControlPlaneHandler(), method: http.MethodGet, path: "/api/runtime-config", want: http.StatusUnauthorized},
		{name: "read token may read", handler: api.ControlPlaneHandler(), method: http.MethodGet, path: "/api/runtime-config", token: "read-token", want: http.StatusOK},
		{name: "read token write is denied", handler: api.ControlPlaneHandler(), method: http.MethodPut, path: "/api/runtime-config/scheduler.dispatch_paused", token: "read-token", body: body, want: http.StatusForbidden},
		{name: "unauthenticated write is denied", handler: api.ControlPlaneHandler(), method: http.MethodPut, path: "/api/runtime-config/scheduler.dispatch_paused", body: body, want: http.StatusUnauthorized},
	} {
		response, _ := runtimeConfigRequest(t, test.handler, test.method, test.path, test.token, test.body)
		if response.Code != test.want {
			t.Errorf("%s: status = %d, want %d (%s)", test.name, response.Code, test.want, response.Body.String())
		}
	}
	if api.scheduler.Paused() {
		t.Fatal("denied writes must not change runtime state")
	}
}

func TestRuntimeConfigWriteValidationAndConflicts(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	handler := api.ControlPlaneHandler()
	put := func(path, body string) (*httptest.ResponseRecorder, runtimeConfigConflict) {
		response, _ := runtimeConfigRequest(t, handler, http.MethodPut, path, "admin-token", body)
		var conflict runtimeConfigConflict
		_ = json.Unmarshal(response.Body.Bytes(), &conflict)
		return response, conflict
	}

	response, _ := put("/api/runtime-config/NOT_A_SETTING", `{"value":true,"expected_revision":0}`)
	if response.Code != http.StatusNotFound {
		t.Errorf("unknown key status = %d", response.Code)
	}
	response, conflict := put("/api/runtime-config/SWITCHYARD_WORKER_COUNT", `{"value":"2","expected_revision":0}`)
	if response.Code != http.StatusConflict || conflict.Error["code"] != "file-managed" || !strings.Contains(conflict.Error["message"], "SWITCHYARD_ENV_FILE") {
		t.Errorf("file-managed write = %d %s", response.Code, response.Body.String())
	}
	response, conflict = put("/api/runtime-config/scheduler.dispatch_paused", `{"value":true,"expected_revision":0}`)
	if response.Code != http.StatusConflict || conflict.Error["code"] != "unavailable" {
		t.Errorf("write without a scheduler = %d %s", response.Code, response.Body.String())
	}

	scheduler := NewScheduler(store, NewRunner(store), time.Minute, nil)
	api.SetScheduler(scheduler)
	handler = api.ControlPlaneHandler()
	for name, body := range map[string]string{
		"string value":     `{"value":"true","expected_revision":0}`,
		"missing revision": `{"value":true}`,
		"unknown field":    `{"value":true,"expected_revision":0,"restart":true}`,
		"invalid json":     `{`,
	} {
		response, _ := put("/api/runtime-config/scheduler.dispatch_paused", body)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", name, response.Code, response.Body.String())
		}
	}
	if scheduler.Paused() {
		t.Fatal("invalid writes must not change runtime state")
	}

	response, conflict = put("/api/runtime-config/scheduler.dispatch_paused", `{"value":true,"expected_revision":7}`)
	if response.Code != http.StatusConflict || conflict.Error["code"] != "stale-revision" || conflict.Revision != 0 {
		t.Errorf("stale revision = %d %s", response.Code, response.Body.String())
	}
	if scheduler.Paused() {
		t.Fatal("stale writes must not change runtime state")
	}

	response, _ = put("/api/runtime-config/scheduler.dispatch_paused", `{"value":true,"expected_revision":0,"reason":"drain before maintenance"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("accepted write = %d %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Setting  RuntimeConfigSetting `json:"setting"`
		Revision uint64               `json:"revision"`
		Change   RuntimeSettingChange `json:"change"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if !scheduler.Paused() || accepted.Revision != 1 || accepted.Setting.Effective == nil || *accepted.Setting.Effective != "true" || accepted.Setting.Source != runtimeConfigSourceRuntime {
		t.Fatalf("accepted write did not apply: %+v", accepted)
	}
	if accepted.Change.Actor != "token:admin" || accepted.Change.Previous != "false" || accepted.Change.Value != "true" || accepted.Change.Reason != "drain before maintenance" {
		t.Fatalf("audit record = %+v", accepted.Change)
	}

	response, conflict = put("/api/runtime-config/scheduler.dispatch_paused", `{"value":false,"expected_revision":0}`)
	if response.Code != http.StatusConflict || conflict.Error["code"] != "stale-revision" || conflict.Revision != 1 {
		t.Errorf("reused revision = %d %s", response.Code, response.Body.String())
	}
	if !scheduler.Paused() {
		t.Fatal("stale write after a change must not apply")
	}

	_, payload := runtimeConfigRequest(t, handler, http.MethodGet, "/api/runtime-config", "admin-token", "")
	if payload.Revision != 1 || len(payload.Changes) != 1 || payload.Changes[0].Key != "scheduler.dispatch_paused" {
		t.Fatalf("audit trail not visible: revision=%d changes=%+v", payload.Revision, payload.Changes)
	}
	if paused := settingByKey(t, payload, "scheduler.dispatch_paused"); paused.Effective == nil || *paused.Effective != "true" || paused.Overridden == nil || !*paused.Overridden {
		t.Fatalf("effective runtime value not reported: %+v", paused)
	}
}

func TestSchedulerPauseSkipsDispatchUntilResumed(t *testing.T) {
	now := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTriggerEnabled("daily-changemaker-native", true); err != nil {
		t.Fatal(err)
	}
	due := now.Add(-time.Minute)
	if _, err := store.UpdateTriggerScheduleState("daily-changemaker-native", nil, &due, ""); err != nil {
		t.Fatal(err)
	}
	dispatched := 0
	runner := NewRunner(store, MapExecutorRegistry{"changemaker-daily-digest": WorkflowExecutorFunc(func(context.Context, Run) (WorkflowExecution, error) {
		dispatched++
		return WorkflowExecution{Summary: "dispatched"}, nil
	})})
	scheduler := NewScheduler(store, runner, time.Minute, func() time.Time { return now })
	scheduler.SetPaused(true)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dispatched != 0 || len(store.ListRuns()) != 0 {
		t.Fatalf("paused scheduler dispatched work: runs=%d", len(store.ListRuns()))
	}
	if status := scheduler.Status(); !status.Paused || status.LastTick.IsZero() {
		t.Fatalf("status = %+v", status)
	}
	scheduler.SetPaused(false)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if dispatched != 1 {
		t.Fatalf("resumed scheduler dispatched %d runs", dispatched)
	}
}

func TestWorkerPoolPauseStopsClaimsUntilResumed(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	executed := make(chan struct{}, 1)
	runner := NewRunner(store, MapExecutorRegistry{"changemaker-daily-digest": WorkflowExecutorFunc(func(context.Context, Run) (WorkflowExecution, error) {
		executed <- struct{}{}
		return WorkflowExecution{Summary: "claimed"}, nil
	})})
	if _, err := store.SetTriggerEnabled("daily-changemaker-native", true); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.EnqueueTrigger("daily-changemaker-native"); err != nil {
		t.Fatal(err)
	}
	pool := NewWorkerPool(store, runner, 1, time.Minute, 5*time.Millisecond, nil)
	pool.SetPaused(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx)
	select {
	case <-executed:
		t.Fatal("paused pool claimed a run")
	case <-time.After(50 * time.Millisecond):
	}
	if status := pool.Status(); !status.Paused || !status.Running {
		t.Fatalf("status = %+v", status)
	}
	pool.SetPaused(false)
	select {
	case <-executed:
	case <-time.After(2 * time.Second):
		t.Fatal("resumed pool did not claim the queued run")
	}
}
