package controlplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type recordingModelBroker struct {
	requests []ModelRequest
	response ModelResponse
	err      error
}

func TestGenericWorkflowHTTPExecutorUsesAllowlistedBoundedEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_HTTP_ALLOWED_HOSTS", target.Hostname())
	t.Setenv("SWITCHYARD_HTTP_ALLOW_INSECURE", "true")
	store := NewStore(nil)
	if err := store.UpsertWorkflow(Workflow{ID: "http-workflow", Kind: "http-workflow", Name: "HTTP Workflow"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: "http-workflow", Name: "HTTP Workflow", Status: "active", Pipeline: PipelineDefinition{ID: "http-pipeline", WorkflowID: "http-workflow", Name: "HTTP Pipeline", Steps: []PipelineStepDefinition{{ID: "fetch", Position: 1, Name: "http.fetch", Executor: "http", Inputs: map[string]string{"allow_http": "true", "url": server.URL, "method": "POST", "body": `{}`}}}}}); err != nil {
		t.Fatal(err)
	}
	run, err := NewRunner(store).RunOnce(t.Context(), CreateRunInput{WorkflowID: "http-workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("run = %+v", run)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Artifacts) != 1 || !strings.Contains(detail.Artifacts[0].URI, "%7B%22ok%22:true%7D") {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func (r *recordingModelBroker) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	_ = ctx
	r.requests = append(r.requests, request)
	if r.err != nil {
		return ModelResponse{}, r.err
	}
	return r.response, nil
}

func TestStoreBackedPipelineRegistryPrefersActiveWorkflowDefinition(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	_, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "custom-digest",
		Name:   "Custom Digest",
		Status: "active",
		Pipeline: PipelineDefinition{
			ID:         "custom-digest-pipeline",
			WorkflowID: "custom-digest",
			Name:       "Custom Digest",
			Steps: []PipelineStepDefinition{{
				ID:       "generate",
				Position: 1,
				Name:     "custom.generate",
				Executor: "model",
				Profile:  "content-writer",
				Model:    "${model}",
			}},
		},
	})
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	registry := NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry())
	pipeline, ok := registry.PipelineForWorkflow("custom-digest")
	if !ok {
		t.Fatal("expected custom pipeline")
	}
	if pipeline.ID != "custom-digest-pipeline" || pipeline.WorkflowID != "custom-digest" || pipeline.Steps[0].Name != "custom.generate" {
		t.Fatalf("pipeline = %+v", pipeline)
	}

	pipelines := registry.ListPipelines()
	if len(pipelines) != 3 {
		t.Fatalf("len(pipelines) = %d, want 3", len(pipelines))
	}
	if pipelines[0].ID != "custom-digest-pipeline" {
		t.Fatalf("pipelines = %+v", pipelines)
	}
	if got := workflowIDs(pipelines); !reflect.DeepEqual(got, []string{"custom-digest", "devlog", "open-pilot"}) {
		t.Fatalf("workflowIDs = %+v", got)
	}
}

func TestStoreBackedPipelineRegistryIgnoresDraftWorkflowDefinition(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	_, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "devlog",
		Name:   "Devlog Draft",
		Status: "draft",
		Pipeline: PipelineDefinition{
			ID:         "draft-devlog-pipeline",
			WorkflowID: "devlog",
			Name:       "Draft Devlog",
			Steps: []PipelineStepDefinition{{
				ID:       "draft-step",
				Position: 1,
				Name:     "draft.step",
				Executor: "internal",
			}},
		},
	})
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	registry := NewStoreBackedPipelineRegistry(store, DefaultPipelineRegistry())
	pipeline, ok := registry.PipelineForWorkflow("devlog")
	if !ok {
		t.Fatal("expected fallback static pipeline")
	}
	if pipeline.ID != "manual-devlog" || pipeline.WorkflowID != "devlog" || pipeline.Steps[0].Name != "devlog.collect-history" {
		t.Fatalf("pipeline = %+v", pipeline)
	}

	pipelines := registry.ListPipelines()
	if len(pipelines) != 2 {
		t.Fatalf("len(pipelines) = %d, want 2", len(pipelines))
	}
	if got := workflowIDs(pipelines); !reflect.DeepEqual(got, []string{"devlog", "open-pilot"}) {
		t.Fatalf("workflowIDs = %+v", got)
	}
}

func TestSeedDefaultsCreatesChangemakerWorkflowTemplate(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}

	definition, err := store.WorkflowDefinition("changemaker-daily-digest")
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if definition.Status != "active" || definition.InputShape.Type != "object" || definition.InputShape.Properties["topic"].Type != "string" || definition.InputShape.Properties["prompt"].Type != "string" {
		t.Fatalf("definition = %+v", definition)
	}
	if len(definition.Pipeline.Steps) != 4 || definition.Pipeline.Steps[0].Executor != "internal" || definition.Pipeline.Steps[1].Executor != "source" || definition.Pipeline.Steps[2].Executor != "model" || definition.Pipeline.Steps[3].Executor != "internal" {
		t.Fatalf("definition pipeline = %+v", definition.Pipeline)
	}
	if definition.Version != 1 {
		t.Fatalf("fresh seed version = %d, want 1", definition.Version)
	}
	if err := validateWorkflowDefinitionForActivation(definition); err != nil {
		t.Fatalf("seeded definition fails activation validation: %v", err)
	}

	trigger, ok := store.Trigger("daily-changemaker-native")
	if !ok {
		t.Fatal("daily-changemaker-native not found")
	}
	if trigger.WorkflowID != "changemaker-daily-digest" || trigger.Type != TriggerCron || trigger.Enabled || trigger.Schedule != "0 9 * * *" {
		t.Fatalf("trigger = %+v", trigger)
	}
}

func TestRunnerExecutesPersistedInternalWorkflow(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "custom-digest", Kind: WorkflowKind("custom-digest"), Name: "Custom Digest"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "custom-digest",
		Name:   "Custom Digest",
		Status: "active",
		Pipeline: PipelineDefinition{
			ID:         "custom-digest-pipeline",
			WorkflowID: "custom-digest",
			Name:       "Custom Digest Pipeline",
			Steps: []PipelineStepDefinition{
				{ID: "collect", Position: 1, Name: "custom.collect", Executor: "internal", Inputs: map[string]string{"summary": "Collect {{topic}}", "artifact_name": "collect.md", "artifact_kind": "markdown"}},
				{ID: "publish", Position: 2, Name: "custom.publish", Executor: "internal"},
			},
		},
	}); err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	runner := NewRunner(store)
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "custom-digest", Inputs: map[string]string{"topic": "release notes"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("run status = %s, want succeeded", run.Status)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 2 {
		t.Fatalf("len(detail.Steps) = %d, want 2", len(detail.Steps))
	}
	if detail.Steps[0].Name != "custom.collect" || detail.Steps[0].Status != StepSucceeded || detail.Steps[0].OutputSummary != "Collect release notes" {
		t.Fatalf("first step = %+v", detail.Steps[0])
	}
	if detail.Steps[1].Name != "custom.publish" || detail.Steps[1].Status != StepSucceeded {
		t.Fatalf("second step = %+v", detail.Steps[1])
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Name != "collect.md" || detail.Artifacts[0].Kind != "markdown" || !strings.Contains(detail.Artifacts[0].URI, "Collect%20release%20notes") {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerExecutesPersistedModelWorkflow(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := store.UpsertWorkflow(Workflow{ID: "custom-digest", Kind: WorkflowKind("custom-digest"), Name: "Custom Digest"}); err != nil {
		t.Fatalf("UpsertWorkflow() error = %v", err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{
		ID:     "custom-digest",
		Name:   "Custom Digest",
		Status: "active",
		Pipeline: PipelineDefinition{
			ID:         "custom-digest-pipeline",
			WorkflowID: "custom-digest",
			Name:       "Custom Digest Pipeline",
			Steps:      []PipelineStepDefinition{{ID: "generate", Position: 1, Name: "custom.generate", Executor: "model", Model: "${model}", Inputs: map[string]string{"prompt": "Digest about {{topic}}"}}},
		},
	}); err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}

	broker := &recordingModelBroker{response: ModelResponse{Content: "# Digest\n\nSwitchyard", CorrelationIDs: map[string]string{"request_id": "req-123"}}}
	runner := NewRunner(store, MapExecutorRegistry{"custom-digest": genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store, modelExecutor: broker}}})
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: "custom-digest", Inputs: map[string]string{"topic": "Switchyard", "model": "digest-model"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != RunSucceeded {
		t.Fatalf("run status = %s, want succeeded", run.Status)
	}
	if len(broker.requests) != 1 {
		t.Fatalf("len(broker.requests) = %d, want 1", len(broker.requests))
	}
	if broker.requests[0].Model != "digest-model" || len(broker.requests[0].Messages) != 1 || broker.requests[0].Messages[0].Content != "Digest about Switchyard" {
		t.Fatalf("broker request = %+v", broker.requests[0])
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Name != "custom.generate" || detail.Steps[0].Status != StepSucceeded || detail.Steps[0].CorrelationIDs["request_id"] != "req-123" {
		t.Fatalf("step = %+v", detail.Steps)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Name != "generate.md" || detail.Artifacts[0].Kind != "markdown" || !strings.Contains(detail.Artifacts[0].URI, "Switchyard") {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func workflowIDs(pipelines []Pipeline) []string {
	ids := make([]string, 0, len(pipelines))
	for _, pipeline := range pipelines {
		ids = append(ids, pipeline.WorkflowID)
	}
	return ids
}
