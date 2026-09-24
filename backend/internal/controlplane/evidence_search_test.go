package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// evidenceFixtureWindow spans the fixture clock back to exactly fixture.floor.
const evidenceFixtureWindow = 5 * time.Hour

type evidenceFixture struct {
	store    *MemoryStore
	now      time.Time
	floor    time.Time
	runOK    Run
	runFail  Run
	runOld   Run
	stepOK   RunStep
	stepFail RunStep
	reqOK    ModelBrokerRequest
	reqFail  ModelBrokerRequest
}

func newEvidenceFixture(t *testing.T) *evidenceFixture {
	t.Helper()
	fixture := &evidenceFixture{now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
	fixture.store = NewStore(func() time.Time { return fixture.now })
	fixture.floor = fixture.now.Add(-24 * time.Hour)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(fixture.store.UpsertWorkflow(Workflow{ID: "devlog", Kind: WorkflowDevlog, Name: "Devlog", Status: "active"}))
	must(fixture.store.UpsertWorkflow(Workflow{ID: "changemaker", Kind: WorkflowChangemaker, Name: "Changemaker", Status: "active"}))

	fixture.now = fixture.floor.Add(-time.Hour)
	var err error
	fixture.runOld, err = fixture.store.CreateRun(CreateRunInput{WorkflowID: "devlog", Inputs: map[string]string{"topic": "ancient-needle"}})
	must(err)
	_, err = fixture.store.FinishRun(fixture.runOld.ID, RunFailed, "ancient-needle failure")
	must(err)

	fixture.now = fixture.floor.Add(time.Hour)
	fixture.runOK, err = fixture.store.CreateRun(CreateRunInput{WorkflowID: "devlog", Inputs: map[string]string{"api_key": "sk-proj-verysecret1234567890", "topic": "input-only-needle"}})
	must(err)
	fixture.stepOK, err = fixture.store.CreateRunStep(CreateRunStepInput{RunID: fixture.runOK.ID, Position: 1, Name: "devlog.generate", Executor: "model", Model: "qwen", Inputs: map[string]string{"prompt": "step-input-needle"}, CorrelationIDs: map[string]string{"trace_id": "trace-alpha"}})
	must(err)
	_, err = fixture.store.FinishRunStep(fixture.stepOK.ID, StepSucceeded, "generated digest for orbit", "", map[string]string{"model_broker_request_id": "req-ok"})
	must(err)
	_, err = fixture.store.FinishRun(fixture.runOK.ID, RunSucceeded, "digest published")
	must(err)

	fixture.now = fixture.now.Add(time.Hour)
	fixture.reqOK, _, err = fixture.store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: "req-ok", Model: "qwen", Path: "/v1/chat/completions", Client: "cli", RequestBody: `{"messages":[{"content":"secret-body-needle"}]}`})
	must(err)
	fixture.reqOK, err = fixture.store.FinishModelBrokerRequest("req-ok", FinishModelBrokerRequestInput{Status: ModelBrokerRequestCompleted, ResponseBody: `{"content":"response-body-needle"}`, CorrelationIDs: map[string]string{"trace_id": "trace-alpha", "run_id": fixture.runOK.ID}})
	must(err)

	fixture.now = fixture.now.Add(time.Hour)
	fixture.runFail, err = fixture.store.CreateRun(CreateRunInput{WorkflowID: "changemaker"})
	must(err)
	fixture.stepFail, err = fixture.store.CreateRunStep(CreateRunStepInput{RunID: fixture.runFail.ID, Position: 1, Name: "changemaker.generate", Executor: "model", CorrelationIDs: map[string]string{"trace_id": "trace-beta", "authorization": "Bearer stored-cred"}})
	must(err)
	_, err = fixture.store.FinishRunStep(fixture.stepFail.ID, StepFailed, "", "upstream refused token=abc123secretvalue", nil)
	must(err)
	_, err = fixture.store.FinishRun(fixture.runFail.ID, RunFailed, "generation failed")
	must(err)

	fixture.now = fixture.now.Add(time.Hour)
	fixture.reqFail, _, err = fixture.store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: "req-fail", Model: "llama", Path: "/api/generate", Client: "cli"})
	must(err)
	fixture.reqFail, err = fixture.store.FinishModelBrokerRequest("req-fail", FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: "upstream unavailable", CorrelationIDs: map[string]string{"trace_id": "trace-beta"}})
	must(err)
	fixture.now = fixture.now.Add(time.Hour)
	return fixture
}

func (f *evidenceFixture) search(t *testing.T, input EvidenceSearchInput) EvidenceSearchPage {
	t.Helper()
	if input.RetainedSince.IsZero() {
		input.RetainedSince = f.floor
	}
	page, err := f.store.SearchEvidence(context.Background(), input)
	if err != nil {
		t.Fatalf("SearchEvidence(%+v) error = %v", input, err)
	}
	return page
}

func hitKeys(hits []EvidenceHit) []string {
	keys := make([]string, 0, len(hits))
	for _, hit := range hits {
		keys = append(keys, hit.Kind+":"+hit.ID)
	}
	return keys
}

func TestEvidenceSearchScopesAndFilters(t *testing.T) {
	f := newEvidenceFixture(t)

	all := f.search(t, EvidenceSearchInput{})
	want := []string{"request:req-fail", "step:" + f.stepFail.ID, "run:" + f.runFail.ID, "request:req-ok", "step:" + f.stepOK.ID, "run:" + f.runOK.ID}
	if got := hitKeys(all.Hits); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("all hits = %v, want %v", got, want)
	}
	if all.HasMore {
		t.Fatal("unexpected HasMore for complete result")
	}

	cases := []struct {
		name  string
		input EvidenceSearchInput
		want  []string
	}{
		{"query across kinds", EvidenceSearchInput{Query: "orbit"}, []string{"step:" + f.stepOK.ID}},
		{"query is case-insensitive", EvidenceSearchInput{Query: "DIGEST PUBLISHED"}, []string{"run:" + f.runOK.ID}},
		{"runs scope", EvidenceSearchInput{Scope: EvidenceScopeRuns, Query: "devlog"}, []string{"run:" + f.runOK.ID}},
		{"steps scope by name", EvidenceSearchInput{Scope: EvidenceScopeSteps, Query: "generate"}, []string{"step:" + f.stepFail.ID, "step:" + f.stepOK.ID}},
		{"requests scope by path", EvidenceSearchInput{Scope: EvidenceScopeRequests, Query: "/api/generate"}, []string{"request:req-fail"}},
		{"errors scope", EvidenceSearchInput{Scope: EvidenceScopeErrors}, []string{"request:req-fail", "step:" + f.stepFail.ID, "run:" + f.runFail.ID}},
		{"errors scope with query", EvidenceSearchInput{Scope: EvidenceScopeErrors, Query: "unavailable"}, []string{"request:req-fail"}},
		{"correlations scope matches step and request", EvidenceSearchInput{Scope: EvidenceScopeCorrelations, Query: "trace-alpha"}, []string{"request:req-ok", "step:" + f.stepOK.ID}},
		{"correlations scope ignores summaries", EvidenceSearchInput{Scope: EvidenceScopeCorrelations, Query: "orbit"}, nil},
		{"status filter", EvidenceSearchInput{Status: "failed"}, []string{"request:req-fail", "step:" + f.stepFail.ID, "run:" + f.runFail.ID}},
		{"workflow filter excludes requests", EvidenceSearchInput{WorkflowID: "devlog"}, []string{"step:" + f.stepOK.ID, "run:" + f.runOK.ID}},
		{"run filter links correlated request", EvidenceSearchInput{RunID: f.runOK.ID}, []string{"request:req-ok", "step:" + f.stepOK.ID, "run:" + f.runOK.ID}},
		{"run id query", EvidenceSearchInput{Query: f.runFail.ID}, []string{"step:" + f.stepFail.ID, "run:" + f.runFail.ID}},
		{"inputs are not matched", EvidenceSearchInput{Query: "input-only-needle"}, nil},
		{"step inputs are not matched", EvidenceSearchInput{Query: "step-input-needle"}, nil},
		{"request bodies are not matched", EvidenceSearchInput{Query: "secret-body-needle"}, nil},
		{"response bodies are not matched", EvidenceSearchInput{Query: "response-body-needle"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hitKeys(f.search(t, tc.input).Hits)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvidenceSearchExcludesRecordsOutsideRetentionWindow(t *testing.T) {
	f := newEvidenceFixture(t)
	if _, err := f.store.RunDetail(f.runOld.ID); err != nil {
		t.Fatalf("old run should still exist before pruning: %v", err)
	}
	if got := hitKeys(f.search(t, EvidenceSearchInput{Query: "ancient-needle"}).Hits); len(got) != 0 {
		t.Fatalf("retained-out run surfaced: %v", got)
	}
	widened := f.search(t, EvidenceSearchInput{Query: "ancient-needle", RetainedSince: f.floor.Add(-2 * time.Hour)})
	if got := hitKeys(widened.Hits); strings.Join(got, ",") != "run:"+f.runOld.ID {
		t.Fatalf("widened window hits = %v", got)
	}
	if _, err := f.store.SearchEvidence(context.Background(), EvidenceSearchInput{Query: "x"}); err == nil {
		t.Fatal("expected missing retention floor to be rejected")
	}
}

func TestEvidenceSearchPaginationIsStableAcrossTies(t *testing.T) {
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	if err := store.UpsertWorkflow(Workflow{ID: "devlog", Kind: WorkflowDevlog, Name: "Devlog", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if i%2 == 0 {
			now = now.Add(time.Minute)
		}
		run, err := store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateRunStep(CreateRunStepInput{RunID: run.ID, Position: 1, Name: "step", Executor: "internal"}); err != nil {
			t.Fatal(err)
		}
	}
	floor := now.Add(-time.Hour)
	full := func() []EvidenceHit {
		page, err := store.SearchEvidence(context.Background(), EvidenceSearchInput{Limit: 100, RetainedSince: floor})
		if err != nil {
			t.Fatal(err)
		}
		return page.Hits
	}()
	if len(full) != 14 {
		t.Fatalf("expected 14 hits, got %d", len(full))
	}
	for i := 1; i < len(full); i++ {
		if !evidenceHitNewer(full[i-1], full[i]) {
			t.Fatalf("hits out of order at %d: %+v then %+v", i, full[i-1], full[i])
		}
	}
	var walked []EvidenceHit
	input := EvidenceSearchInput{Limit: 3, RetainedSince: floor}
	for pages := 0; ; pages++ {
		page, err := store.SearchEvidence(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, page.Hits...)
		if !page.HasMore {
			if pages != 4 {
				t.Fatalf("expected 5 pages, got %d", pages+1)
			}
			break
		}
		if len(page.Hits) != 3 {
			t.Fatalf("page %d size = %d", pages, len(page.Hits))
		}
		last := page.Hits[len(page.Hits)-1]
		created, kind, id, err := decodeEvidenceCursor(encodeEvidenceCursor(last))
		if err != nil {
			t.Fatal(err)
		}
		input.BeforeCreated, input.BeforeKind, input.BeforeID = &created, kind, id
	}
	if strings.Join(hitKeys(walked), ",") != strings.Join(hitKeys(full), ",") {
		t.Fatalf("paginated walk %v != full listing %v", hitKeys(walked), hitKeys(full))
	}
}

func TestEvidenceSearchRejectsInvalidInput(t *testing.T) {
	floor := time.Now().UTC().Add(-time.Hour)
	store := NewStore(nil)
	for name, input := range map[string]EvidenceSearchInput{
		"scope":        {Scope: "artifacts", RetainedSince: floor},
		"limit":        {Limit: 101, RetainedSince: floor},
		"query length": {Query: strings.Repeat("q", evidenceSearchMaxQueryLength+1), RetainedSince: floor},
		"cursor kind":  {RetainedSince: floor, BeforeCreated: &floor, BeforeKind: "artifact", BeforeID: "x"},
	} {
		if _, err := store.SearchEvidence(context.Background(), input); err == nil {
			t.Errorf("%s: expected ErrInvalid", name)
		}
	}
}

type evidenceSearchResponse struct {
	Hits       []EvidenceHit     `json:"hits"`
	NextCursor string            `json:"next_cursor"`
	Scope      string            `json:"scope"`
	Query      string            `json:"query"`
	Limit      int               `json:"limit"`
	Retention  EvidenceRetention `json:"retention"`
}

func evidenceRequest(t *testing.T, handler http.Handler, target, token string) (*httptest.ResponseRecorder, evidenceSearchResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body evidenceSearchResponse
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v: %s", err, response.Body.String())
		}
	}
	return response, body
}

func TestEvidenceSearchRouteAuthorizationAndListenerBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	f := newEvidenceFixture(t)
	api := NewAPI(f.store, NewRunner(f.store))
	api.clock = func() time.Time { return f.now }
	api.SetRetentionManager(NewRetentionManager(f.store, evidenceFixtureWindow, time.Hour, api.clock))
	control := api.ControlPlaneHandler()

	if response, _ := evidenceRequest(t, control, "/api/evidence/search?q=orbit", ""); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", response.Code)
	}
	response, body := evidenceRequest(t, control, "/api/evidence/search?q=orbit", "read-token")
	if response.Code != http.StatusOK || len(body.Hits) != 1 || body.Hits[0].ID != f.stepOK.ID {
		t.Fatalf("read token status = %d body = %s", response.Code, response.Body.String())
	}
	if body.Retention.Window != evidenceFixtureWindow.String() || !body.Retention.Floor.Equal(f.floor) || body.Scope != EvidenceScopeAll || body.Limit != evidenceSearchDefaultLimit {
		t.Fatalf("retention/scope envelope = %+v", body)
	}
	if response, _ := evidenceRequest(t, control, "/api/evidence/search?q=orbit", "admin-token"); response.Code != http.StatusOK {
		t.Fatalf("admin token status = %d", response.Code)
	}
	model := api.ModelIngressHandler()
	if response, _ := evidenceRequest(t, model, "/api/evidence/search?q=orbit", "admin-token"); response.Code != http.StatusNotFound {
		t.Fatalf("model listener status = %d, want 404", response.Code)
	}
	for _, target := range []string{
		"/api/evidence/search?scope=artifacts",
		"/api/evidence/search?limit=0",
		"/api/evidence/search?limit=101",
		"/api/evidence/search?cursor=not-base64!",
		"/api/evidence/search?q=" + strings.Repeat("a", evidenceSearchMaxQueryLength+1),
	} {
		if response, _ := evidenceRequest(t, control, target, "read-token"); response.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", target, response.Code)
		}
	}
}

func TestEvidenceSearchRoutePaginatesWithOpaqueCursor(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	f := newEvidenceFixture(t)
	api := NewAPI(f.store, NewRunner(f.store))
	api.clock = func() time.Time { return f.now }
	api.SetRetentionManager(NewRetentionManager(f.store, evidenceFixtureWindow, time.Hour, api.clock))
	control := api.ControlPlaneHandler()
	var seen []string
	target := "/api/evidence/search?limit=4"
	for page := 0; ; page++ {
		response, body := evidenceRequest(t, control, target, "admin-token")
		if response.Code != http.StatusOK {
			t.Fatalf("page %d status = %d", page, response.Code)
		}
		seen = append(seen, hitKeys(body.Hits)...)
		if body.NextCursor == "" {
			if page != 1 || len(body.Hits) != 2 {
				t.Fatalf("expected two pages of 4 then 2, got page %d with %d hits", page, len(body.Hits))
			}
			break
		}
		target = "/api/evidence/search?limit=4&cursor=" + body.NextCursor
	}
	want := []string{"request:req-fail", "step:" + f.stepFail.ID, "run:" + f.runFail.ID, "request:req-ok", "step:" + f.stepOK.ID, "run:" + f.runOK.ID}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("walked %v, want %v", seen, want)
	}
}

func TestEvidenceSearchRouteNeverExposesPayloadsOrCredentials(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	f := newEvidenceFixture(t)
	api := NewAPI(f.store, NewRunner(f.store))
	api.clock = func() time.Time { return f.now }
	api.SetRetentionManager(NewRetentionManager(f.store, evidenceFixtureWindow, time.Hour, api.clock))
	control := api.ControlPlaneHandler()

	for _, needle := range []string{"verysecret", "input-only-needle", "step-input-needle", "secret-body-needle", "response-body-needle"} {
		response, body := evidenceRequest(t, control, "/api/evidence/search?q="+needle, "admin-token")
		if response.Code != http.StatusOK || len(body.Hits) != 0 {
			t.Errorf("payload needle %q matched: status=%d hits=%v", needle, response.Code, hitKeys(body.Hits))
		}
	}

	response, body := evidenceRequest(t, control, "/api/evidence/search?scope=errors", "admin-token")
	if response.Code != http.StatusOK || len(body.Hits) != 3 {
		t.Fatalf("errors status = %d body = %s", response.Code, response.Body.String())
	}
	raw := response.Body.String()
	for _, forbidden := range []string{"abc123secretvalue", "stored-cred", "sk-proj-", "request_body", "response_body", `"inputs"`, "secret-body-needle", "input-only-needle"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("response exposes %q: %s", forbidden, raw)
		}
	}
	var failedStep *EvidenceHit
	for i := range body.Hits {
		if body.Hits[i].ID == f.stepFail.ID {
			failedStep = &body.Hits[i]
		}
	}
	if failedStep == nil || failedStep.Error != "upstream refused token=[REDACTED]" {
		t.Fatalf("failed step hit = %+v", failedStep)
	}
	if failedStep.CorrelationIDs["authorization"] != "[REDACTED]" || failedStep.CorrelationIDs["trace_id"] != "trace-beta" {
		t.Fatalf("correlation redaction = %v", failedStep.CorrelationIDs)
	}
	if hit := body.Hits[0]; hit.Kind != EvidenceKindRequest || hit.Error != "upstream unavailable" {
		t.Fatalf("request hit = %+v", hit)
	}
}

func TestBoundedEvidenceTextTruncatesAndRedacts(t *testing.T) {
	long := strings.Repeat("é", evidenceSearchMaxTextLength)
	bounded := boundedEvidenceText(long)
	if !strings.HasSuffix(bounded, "…") || len([]rune(bounded)) > evidenceSearchMaxTextLength/2+1 {
		t.Fatalf("bounded text = %q", bounded)
	}
	if got := boundedEvidenceText("Bearer abc.def password: hunter2"); got != "Bearer [REDACTED] password: [REDACTED]" {
		t.Fatalf("redacted text = %q", got)
	}
}
