package controlplane

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestPostgresStepOutputContractRoundTrip checks that nested output shapes and
// the versioned snapshot contract survive PostgreSQL JSONB storage, that the
// runner enforces the contract against the durable store, and that restored
// outputs are validated on resume. It skips without
// SWITCHYARD_TEST_DATABASE_URL; run `make postgres-test`.
func TestPostgresStepOutputContractRoundTrip(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL step output contract tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	const workflowID = "pg-output-contract"
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), `delete from runs where workflow_id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definition_versions where id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflow_definitions where id=$1`, workflowID)
		_, _ = db.ExecContext(context.Background(), `delete from workflows where id=$1`, workflowID)
	}
	cleanup()
	t.Cleanup(cleanup)
	store := NewPostgresStore(db, nil)
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: workflowID, Name: "PG Output Contract"}); err != nil {
		t.Fatal(err)
	}
	definition := WorkflowDefinition{ID: workflowID, Name: "PG Output Contract", Status: "active", Pipeline: PipelineDefinition{ID: workflowID, WorkflowID: workflowID, Name: "PG Output Contract", Steps: []PipelineStepDefinition{
		{ID: "produce", Position: 1, Name: "contract.produce", Executor: "internal", Inputs: map[string]string{"output.count": "{{count}}", "output.report": "{{report}}"}, OutputShape: reportShape()},
		{ID: "consume", Position: 2, Name: "contract.consume", Executor: "internal", Inputs: map[string]string{"summary": "count={{steps.produce.outputs.count}}"}},
	}}}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: workflowID, Name: "bad", Status: "draft", Pipeline: PipelineDefinition{ID: workflowID, Name: "bad", Steps: []PipelineStepDefinition{{ID: "produce", Position: 1, Name: "produce", Executor: "internal", OutputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"x": {Type: "date"}}}}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported shape error = %v", err)
	}
	if _, err := store.UpsertWorkflowDefinition(definition); err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}
	saved, err := store.WorkflowDefinition(workflowID)
	if err != nil {
		t.Fatal(err)
	}
	items := saved.Pipeline.Steps[0].OutputShape.Properties["report"].Properties["items"]
	if items.MaxItems == nil || *items.MaxItems != 3 || items.Items == nil || items.Items.Required[0] != "id" {
		t.Fatalf("stored output shape = %+v", saved.Pipeline.Steps[0].OutputShape)
	}

	failConsumer := true
	runner := NewRunner(store, MapExecutorRegistry{workflowID: flakyConsumerExecutor{inner: genericWorkflowExecutor{env: &EnvWorkflowExecutors{store: store}}, fail: &failConsumer}})
	invalid, err := runner.RunOnce(ctx, CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{"count": "two", "report": validReport}})
	if err != nil || invalid.Status != RunPaused || !strings.Contains(invalid.Summary, `$.count: expected integer encoded as JSON text, got "two"`) {
		t.Fatalf("invalid run = %+v, %v", invalid, err)
	}
	invalidDetail, err := store.RunDetail(invalid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalidDetail.Steps) != 1 || invalidDetail.Steps[0].Status != StepFailed || len(invalidDetail.Steps[0].Outputs) != 0 || !hasEvent(invalidDetail, stepOutputContractViolatedEvent, "$.count") {
		t.Fatalf("invalid run detail = %+v", invalidDetail)
	}

	source, err := runner.RunOnce(ctx, CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{"count": "2", "report": validReport}})
	if err != nil || source.Status != RunFailed {
		t.Fatalf("source run = %+v, %v", source, err)
	}
	sourceDetail, err := store.RunDetail(source.ID)
	if err != nil {
		t.Fatal(err)
	}
	contract := sourceDetail.Run.PipelineSnapshot.Steps[0].OutputContract
	if contract == nil || contract.Version != StepOutputContractVersion || contract.Shape.Properties["report"].Properties["title"].MaxLength == nil {
		t.Fatalf("persisted snapshot contract = %+v", contract)
	}
	failConsumer = false
	steps := stepsByName(t, sourceDetail)
	resumed, err := runner.RunOnce(ctx, CreateRunInput{WorkflowID: workflowID, Inputs: retryRunInputsFromStep(sourceDetail, steps["contract.consume"]), PipelineSnapshot: sourceDetail.Run.PipelineSnapshot})
	if err != nil || resumed.Status != RunSucceeded {
		t.Fatalf("resumed run = %+v, %v", resumed, err)
	}
	resumedDetail, err := store.RunDetail(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stepsByName(t, resumedDetail)["contract.consume"].OutputSummary; got != "count=2" || resumedDetail.Run.PipelineVersion != sourceDetail.Run.PipelineVersion {
		t.Fatalf("resumed consumer summary = %q, versions %q/%q", got, resumedDetail.Run.PipelineVersion, sourceDetail.Run.PipelineVersion)
	}

	// A restored record that no longer satisfies the snapshot contract is
	// withheld from the consumer.
	if _, err := store.UpdateRunStep(steps["contract.produce"].ID, UpdateRunStepInput{Outputs: map[string]string{"count": "1.5", "report": validReport}}); err != nil {
		t.Fatal(err)
	}
	rejected, err := runner.RunOnce(ctx, CreateRunInput{WorkflowID: workflowID, Inputs: retryRunInputsFromStep(sourceDetail, steps["contract.consume"]), PipelineSnapshot: sourceDetail.Run.PipelineSnapshot})
	if err != nil || rejected.Status != RunPaused || !strings.Contains(rejected.Summary, "recorded outputs fail the producing step's output contract") || !strings.Contains(rejected.Summary, "$.count: expected integer, got non-integral number 1.5") {
		t.Fatalf("rejected restore run = %+v, %v", rejected, err)
	}
}
