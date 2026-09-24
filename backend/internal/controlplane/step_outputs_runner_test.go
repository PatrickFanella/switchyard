package controlplane

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fetchSummarizeAttachFixture wires an allowlisted HTTP source, a recording
// model broker, and the generic executor behind a three-step definition whose
// later steps bind earlier outputs.
type fetchSummarizeAttachFixture struct {
	store     *MemoryStore
	runner    *Runner
	broker    *recordingModelBroker
	hits      *atomic.Int32
	body      string
	serverURL string
}

func newFetchSummarizeAttachFixture(t *testing.T, body string) *fetchSummarizeAttachFixture {
	t.Helper()
	hits := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_HTTP_ALLOWED_HOSTS", target.Hostname())
	t.Setenv("SWITCHYARD_HTTP_ALLOW_INSECURE", "true")
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "fetch-summarize-attach", Kind: "fetch-summarize-attach", Name: "Fetch Summarize Attach"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "fetch-summarize-attach", Name: "Fetch Summarize Attach", Status: "active", Pipeline: PipelineDefinition{ID: "fetch-summarize-attach", WorkflowID: "fetch-summarize-attach", Name: "Fetch Summarize Attach", Steps: []PipelineStepDefinition{
		{ID: "fetch", Position: 1, Name: "source.fetch", Executor: "http", Inputs: map[string]string{"allow_http": "true", "url": server.URL + "/changelog"}},
		{ID: "summarize", Position: 2, Name: "source.summarize", Executor: "model", Model: "summary-model", Inputs: map[string]string{"prompt": "Summarize for {{audience}}:\n{{steps.fetch.outputs.body}}"}},
		{ID: "attach", Position: 3, Name: "source.attach", Executor: "internal", Inputs: map[string]string{"artifact_name": "summary.md", "artifact_kind": "markdown", "summary": "Summarized {{steps.fetch.outputs.body_bytes}} bytes with status {{steps.fetch.outputs.status}}", "output.summary_text": "{{steps.summarize.outputs.content}}"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	broker := &recordingModelBroker{response: ModelResponse{Content: "# Summary\n\nTwo fixes shipped.", CorrelationIDs: map[string]string{"request_id": "req-summary"}}}
	runner := NewRunner(store, MapExecutorRegistry{"fetch-summarize-attach": genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store, modelExecutor: broker}}})
	return &fetchSummarizeAttachFixture{store: store, runner: runner, broker: broker, hits: hits, body: body, serverURL: server.URL}
}

func (f *fetchSummarizeAttachFixture) assertCompleted(t *testing.T, runID string, wantHits int32, wantFetchAttempts, wantSummarizeAttempts int) RunDetail {
	t.Helper()
	detail, err := f.store.RunDetail(runID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != RunSucceeded {
		t.Fatalf("run = %+v", detail.Run)
	}
	if got := f.hits.Load(); got != wantHits {
		t.Fatalf("http source hits = %d, want %d", got, wantHits)
	}
	steps := map[string]RunStep{}
	for _, step := range detail.Steps {
		steps[step.Name] = step
	}
	fetch, summarize, attach := steps["source.fetch"], steps["source.summarize"], steps["source.attach"]
	if wantFetchAttempts > 0 && (fetch.Status != StepSucceeded || fetch.AttemptCount != wantFetchAttempts) {
		t.Fatalf("fetch step = %+v", fetch)
	}
	if wantFetchAttempts > 0 && (fetch.Outputs["body"] != f.body || fetch.Outputs["status"] != "200" || fetch.Outputs["body_bytes"] != strconv.Itoa(len(f.body)) || fetch.Outputs["body_sha256"] != stepOutputDigest(f.body) || fetch.Outputs["method"] != "GET" || fetch.Outputs["artifact_name"] != "fetch.response" || !strings.HasPrefix(fetch.Outputs["content_type"], "text/plain")) {
		t.Fatalf("fetch outputs = %+v", fetch.Outputs)
	}
	if summarize.Status != StepSucceeded || summarize.AttemptCount != wantSummarizeAttempts {
		t.Fatalf("summarize step = %+v", summarize)
	}
	if summarize.Outputs["content"] != f.broker.response.Content || summarize.Outputs["model"] != "summary-model" || summarize.Outputs["artifact_name"] != "summarize.md" || summarize.Outputs["content_bytes"] == "" {
		t.Fatalf("summarize outputs = %+v", summarize.Outputs)
	}
	if attach.Status != StepSucceeded || attach.OutputSummary != "Summarized "+strconv.Itoa(len(f.body))+" bytes with status 200" || attach.Outputs["summary_text"] != f.broker.response.Content || attach.Outputs["artifact_name"] != "summary.md" {
		t.Fatalf("attach step = %+v", attach)
	}
	last := f.broker.requests[len(f.broker.requests)-1]
	if len(last.Messages) != 1 || last.Messages[0].Content != strings.TrimSpace("Summarize for operators:\n"+f.body) {
		t.Fatalf("summarize prompt = %+v", last.Messages)
	}
	var attached bool
	for _, artifact := range detail.Artifacts {
		if artifact.Name != "summary.md" {
			continue
		}
		_, payload, _ := strings.Cut(artifact.URI, ",")
		decoded, err := url.PathUnescape(payload)
		if err != nil {
			t.Fatalf("decode summary.md artifact: %v", err)
		}
		if strings.Contains(decoded, "Two fixes shipped.") {
			attached = true
		}
	}
	if !attached {
		t.Fatalf("summary.md artifact with model content missing: %+v", detail.Artifacts)
	}
	return detail
}

func TestRunnerBindsStepOutputsThroughFetchSummarizeAttach(t *testing.T) {
	fixture := newFetchSummarizeAttachFixture(t, "release notes: 2 fixes\n")
	run, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: map[string]string{"audience": "operators"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	fixture.assertCompleted(t, run.ID, 1, 1, 1)
}

func TestRunnerRestoresStepOutputsOnSameRunRetryWithoutRefetching(t *testing.T) {
	fixture := newFetchSummarizeAttachFixture(t, "release notes: 2 fixes\n")
	fixture.broker.err = errors.New("model unavailable")
	queued, err := fixture.runner.Enqueue(CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: map[string]string{"audience": "operators"}})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	claimed, err := fixture.store.ClaimNextRun("worker-one", time.Minute)
	if err != nil || claimed.ID != queued.ID {
		t.Fatalf("ClaimNextRun() = %+v, %v", claimed, err)
	}
	finished, err := fixture.runner.ExecuteClaimedRun(t.Context(), claimed, "worker-one")
	if err != nil || finished.Status != RunFailed || finished.Summary != "model unavailable" {
		t.Fatalf("first attempt = %+v, %v", finished, err)
	}
	if fixture.hits.Load() != 1 {
		t.Fatalf("http hits after first attempt = %d", fixture.hits.Load())
	}

	fixture.broker.err = nil
	if _, err := fixture.store.RequeueFailedRun(queued.ID, time.Now().Add(-time.Second), "retry"); err != nil {
		t.Fatalf("RequeueFailedRun() error = %v", err)
	}
	claimed, err = fixture.store.ClaimNextRun("worker-two", time.Minute)
	if err != nil || claimed.ID != queued.ID || claimed.AttemptCount != 2 {
		t.Fatalf("second ClaimNextRun() = %+v, %v", claimed, err)
	}
	if _, err := fixture.runner.ExecuteClaimedRun(t.Context(), claimed, "worker-two"); err != nil {
		t.Fatalf("second attempt error = %v", err)
	}
	fixture.assertCompleted(t, queued.ID, 1, 1, 2)
}

func TestRunnerRestoresStepOutputsForOperatorResumeRun(t *testing.T) {
	fixture := newFetchSummarizeAttachFixture(t, "release notes: 2 fixes\n")
	fixture.broker.err = errors.New("model unavailable")
	failed, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: map[string]string{"audience": "operators"}})
	if err != nil || failed.Status != RunFailed {
		t.Fatalf("source run = %+v, %v", failed, err)
	}
	detail, err := fixture.store.RunDetail(failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	var summarize RunStep
	for _, step := range detail.Steps {
		if step.Name == "source.summarize" {
			summarize = step
		}
	}
	if summarize.ID == "" {
		t.Fatalf("summarize step missing: %+v", detail.Steps)
	}

	fixture.broker.err = nil
	resumed, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: retryRunInputsFromStep(detail, summarize), PipelineSnapshot: detail.Run.PipelineSnapshot})
	if err != nil {
		t.Fatalf("resume RunOnce() error = %v", err)
	}
	resumedDetail := fixture.assertCompleted(t, resumed.ID, 1, 0, 1)
	if len(resumedDetail.Steps) != 2 {
		t.Fatalf("resumed run reran earlier steps: %+v", resumedDetail.Steps)
	}
	var restored bool
	for _, event := range resumedDetail.Events {
		if event.Type == stepOutputsRestoredEvent && strings.Contains(event.Message, "restored outputs of 1 succeeded step(s) before position 2 from run "+failed.ID) {
			restored = true
		}
	}
	if !restored {
		t.Fatalf("restore event missing: %+v", resumedDetail.Events)
	}

	// A resume against a different pipeline version must not read foreign outputs.
	other := clonePipeline(*detail.Run.PipelineSnapshot)
	other.Description = "changed"
	fixture.broker.requests = nil
	mismatch, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: retryRunInputsFromStep(detail, summarize), PipelineSnapshot: &other})
	if err != nil || mismatch.Status != RunPaused || !strings.Contains(mismatch.Summary, `unresolved step output reference "steps.fetch.outputs.body"`) || len(fixture.broker.requests) != 0 {
		t.Fatalf("version mismatch resume = %+v, %v, broker requests = %d", mismatch, err, len(fixture.broker.requests))
	}
	mismatchDetail, err := fixture.store.RunDetail(mismatch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var skipped bool
	for _, event := range mismatchDetail.Events {
		if event.Type == stepOutputsRestoredEvent && strings.HasPrefix(event.Message, "skipped: source run "+failed.ID+" pipeline version") {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("version mismatch event missing: %+v", mismatchDetail.Events)
	}
}

func TestRunnerFailsClearlyOnMissingStepOutputReference(t *testing.T) {
	fixture := newFetchSummarizeAttachFixture(t, "release notes: 2 fixes\n")
	if _, err := fixture.store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "fetch-summarize-attach", Name: "Fetch Summarize Attach", Status: "active", Pipeline: PipelineDefinition{ID: "fetch-summarize-attach", WorkflowID: "fetch-summarize-attach", Name: "Fetch Summarize Attach", Steps: []PipelineStepDefinition{
		{ID: "fetch", Position: 1, Name: "source.fetch", Executor: "http", Inputs: map[string]string{"allow_http": "true", "url": fixture.serverURL + "/changelog"}},
		{ID: "summarize", Position: 2, Name: "source.summarize", Executor: "model", Model: "summary-model", Inputs: map[string]string{"prompt": "{{steps.fetch.outputs.headers}}"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach"})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunPaused || !strings.Contains(run.Summary, `unresolved step output reference "steps.fetch.outputs.headers"`) {
		t.Fatalf("run = %+v", run)
	}
	detail, err := fixture.store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Steps) != 2 || detail.Steps[0].Status != StepSucceeded || detail.Steps[1].Status != StepFailed || !strings.Contains(detail.Steps[1].Error, "no succeeded step output with that name") {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if len(fixture.broker.requests) != 0 {
		t.Fatalf("model broker was called despite unresolved reference: %+v", fixture.broker.requests)
	}
}

func TestRunnerOmitsOversizedStepOutputsAndRedactsSecrets(t *testing.T) {
	t.Setenv("SWITCHYARD_MAX_STEP_OUTPUT_BYTES", "1024")
	fixture := newFetchSummarizeAttachFixture(t, strings.Repeat("x", 1500))
	run, err := fixture.runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "fetch-summarize-attach", Inputs: map[string]string{"audience": "operators"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunPaused || !strings.Contains(run.Summary, `unresolved step output reference "steps.fetch.outputs.body"`) {
		t.Fatalf("run = %+v", run)
	}
	detail, err := fixture.store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fetch := detail.Steps[0]
	if fetch.Status != StepSucceeded || fetch.Outputs["body"] != "" || fetch.Outputs["body_bytes"] != "1500" || fetch.Outputs["status"] != "200" {
		t.Fatalf("fetch step = %+v", fetch)
	}
	var omitted bool
	for _, event := range detail.Events {
		if event.Type == stepOutputOmittedEvent && strings.Contains(event.Message, "fetch.body (1500 bytes exceeds the 1024-byte step output limit)") {
			omitted = true
		}
	}
	if !omitted {
		t.Fatalf("omission event missing: %+v", detail.Events)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Name != "fetch.response" || detail.Artifacts[0].ByteSize != 1500 {
		t.Fatalf("full payload artifact missing: %+v", detail.Artifacts)
	}

	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "notes", Kind: "notes", Name: "Notes"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "notes", Name: "Notes", Status: "active", Pipeline: PipelineDefinition{ID: "notes", WorkflowID: "notes", Name: "Notes", Steps: []PipelineStepDefinition{
		{ID: "note", Position: 1, Name: "note", Executor: "internal", Inputs: map[string]string{"output.note": "Authorization: Bearer abc.def-123 for {{service}}"}},
		{ID: "echo", Position: 2, Name: "echo", Executor: "internal", Inputs: map[string]string{"summary": "note={{steps.note.outputs.note}}"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store, MapExecutorRegistry{"notes": genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}})
	notes, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "notes", Inputs: map[string]string{"service": "gitea"}})
	if err != nil || notes.Status != RunSucceeded {
		t.Fatalf("notes run = %+v, %v", notes, err)
	}
	notesDetail, err := store.RunDetail(notes.ID)
	if err != nil {
		t.Fatal(err)
	}
	if notesDetail.Steps[0].Outputs["note"] != "Authorization: Bearer [REDACTED] for gitea" {
		t.Fatalf("persisted note output = %q", notesDetail.Steps[0].Outputs["note"])
	}
	if notesDetail.Steps[1].OutputSummary != "note=Authorization: Bearer [REDACTED] for gitea" {
		t.Fatalf("downstream summary = %q", notesDetail.Steps[1].OutputSummary)
	}
}
