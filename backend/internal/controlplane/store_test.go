package controlplane_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestSeedDefaultsCreatesWorkflowAndTriggerCatalog(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}

	workflows := store.ListWorkflows()
	if len(workflows) != 3 {
		t.Fatalf("len(workflows) = %d, want 3", len(workflows))
	}
	if workflows[0].ID != "changemaker-daily-digest" || workflows[1].ID != "devlog" || workflows[2].ID != "open-pilot" {
		t.Fatalf("workflows sorted by id = %+v", workflows)
	}

	triggers := store.ListTriggers()
	if len(triggers) != 4 {
		t.Fatalf("len(triggers) = %d, want 4", len(triggers))
	}
}

func TestStoreWorkflowDefinitionsCRUD(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	definition := controlplane.WorkflowDefinition{
		ID:          "daily-digest",
		Name:        "Daily Digest",
		Description: "Generate a daily digest from a prompt.",
		Status:      "draft",
		InputShape:  controlplane.JSONShape{Type: "object", Properties: map[string]controlplane.JSONShapeProperty{"topic": {Type: "string", Description: "Digest topic"}}, Required: []string{"topic"}},
		Pipeline: controlplane.PipelineDefinition{
			ID:         "daily-digest-pipeline",
			WorkflowID: "daily-digest",
			Name:       "Daily Digest Pipeline",
			Steps: []controlplane.PipelineStepDefinition{
				{ID: "build-prompt", Position: 1, Name: "digest.build-prompt", Executor: "internal", Inputs: map[string]string{"template": "Write about {{topic}}"}},
				{ID: "generate", Position: 2, Name: "digest.generate", Executor: "model", Profile: "content-writer", Model: "${model}"},
			},
		},
	}

	created, err := store.UpsertWorkflowDefinition(definition)
	if err != nil {
		t.Fatalf("UpsertWorkflowDefinition() error = %v", err)
	}
	if created.ID != "daily-digest" || created.Pipeline.Steps[1].Executor != "model" {
		t.Fatalf("created = %+v", created)
	}

	fetched, err := store.WorkflowDefinition("daily-digest")
	if err != nil {
		t.Fatalf("WorkflowDefinition() error = %v", err)
	}
	if fetched.InputShape.Properties["topic"].Type != "string" {
		t.Fatalf("fetched = %+v", fetched)
	}

	list, err := store.ListWorkflowDefinitions()
	if err != nil {
		t.Fatalf("ListWorkflowDefinitions() error = %v", err)
	}
	if len(list) != 1 || list[0].ID != "daily-digest" {
		t.Fatalf("list = %+v", list)
	}
}

func TestRunnerRejectsManualOpenPilotWithoutInputs(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{})

	_, err := runner.RunTrigger(t.Context(), "manual-open-pilot")
	if err != controlplane.ErrInvalid {
		t.Fatalf("RunTrigger() error = %v, want invalid input", err)
	}
	if runs := store.ListRuns(); len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
}

func TestRunnerFailsDevlogWithoutRegisteredExecutor(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{})

	run, err := runner.RunTrigger(t.Context(), "manual-devlog")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	if run.WorkflowID != "devlog" || run.TriggerID != "manual-devlog" || run.Status != controlplane.RunFailed || !strings.Contains(run.Summary, "not registered") {
		t.Fatalf("run = %+v", run)
	}
	if runs := store.ListRuns(); len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestRunnerDispatchesNativeChangemakerExecutor(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{
		"changemaker-daily-digest": controlplane.WorkflowExecutorFunc(func(_ context.Context, run controlplane.Run) (controlplane.WorkflowExecution, error) {
			if run.WorkflowID != "changemaker-daily-digest" {
				t.Fatalf("workflow = %s", run.WorkflowID)
			}
			return controlplane.WorkflowExecution{Summary: "dispatched test changemaker", Events: []controlplane.WorkflowExecutionEvent{{Type: "changemaker.accepted", Message: "accepted"}}}, nil
		}),
	})

	run, err := runner.RunTrigger(t.Context(), "daily-changemaker-native")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || run.Summary != "dispatched test changemaker" {
		t.Fatalf("run = %+v", run)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepSucceeded {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if detail.Events[4].Type != "changemaker.accepted" {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestStoreAddsArtifacts(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	artifact, err := store.AddArtifact(controlplane.CreateArtifactInput{RunID: run.ID, Name: "devlog.md", Kind: "markdown", URI: "data:text/markdown,hello"})
	if err != nil {
		t.Fatalf("AddArtifact() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].ID != artifact.ID {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
	if detail.Events[len(detail.Events)-1].Type != "artifact.created" {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestStoreRunStepLifecycle(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "collect history", Executor: "internal", Inputs: map[string]string{"scope": "recent"}})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepQueued || step.AttemptCount != 0 {
		t.Fatalf("created step = %+v", step)
	}
	step, err = store.StartRunStep(step.ID)
	if err != nil {
		t.Fatalf("StartRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepRunning || step.AttemptCount != 1 || step.StartedAt == nil {
		t.Fatalf("started step = %+v", step)
	}
	step, err = store.FinishRunStep(step.ID, controlplane.StepFailed, "", "boom", map[string]string{"request": "req-1"})
	if err != nil {
		t.Fatalf("FinishRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepFailed || step.Error != "boom" || step.CorrelationIDs["request"] != "req-1" {
		t.Fatalf("failed step = %+v", step)
	}
	step, err = store.RetryRunStep(step.ID)
	if err != nil {
		t.Fatalf("RetryRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepQueued || step.Error != "" || step.StartedAt != nil || step.FinishedAt != nil {
		t.Fatalf("retried step = %+v", step)
	}
	steps := store.ListRunSteps(run.ID)
	if len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("steps = %+v", steps)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].ID != step.ID {
		t.Fatalf("detail steps = %+v", detail.Steps)
	}
}

func TestStoreRunStepCancelValidation(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	step, err := store.CreateRunStep(controlplane.CreateRunStepInput{RunID: run.ID, Position: 1, Name: "generate", Executor: "model"})
	if err != nil {
		t.Fatalf("CreateRunStep() error = %v", err)
	}
	step, err = store.CancelRunStep(step.ID)
	if err != nil {
		t.Fatalf("CancelRunStep() error = %v", err)
	}
	if step.Status != controlplane.StepCanceled || step.FinishedAt == nil {
		t.Fatalf("canceled step = %+v", step)
	}
	if _, err := store.CancelRunStep(step.ID); err == nil {
		t.Fatal("CancelRunStep() succeeded for terminal step")
	}
}

func TestStoreClaimsQueuedRuns(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	if claimed.ID != run.ID || claimed.Status != controlplane.RunRunning || claimed.ClaimedBy != "worker-1" || claimed.AttemptCount != 1 || claimed.LeaseExpiresAt == nil {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := store.ClaimNextRun("worker-2", time.Minute); err == nil {
		t.Fatal("second claim unexpectedly succeeded")
	}
}

func TestStoreExtendsRunLease(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	claimed, err := store.ClaimNextRun("worker-1", time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	now = now.Add(30 * time.Second)
	extended, err := store.ExtendRunLease(run.ID, "worker-1", 2*time.Minute)
	if err != nil {
		t.Fatalf("ExtendRunLease() error = %v", err)
	}
	if !extended.LeaseExpiresAt.After(*claimed.LeaseExpiresAt) || extended.ClaimedBy != "worker-1" {
		t.Fatalf("extended = %+v claimed = %+v", extended, claimed)
	}
	if _, err := store.ExtendRunLease(run.ID, "worker-2", time.Minute); err == nil {
		t.Fatal("ExtendRunLease() succeeded for wrong worker")
	}
}

func TestStoreFinishClaimedRunRequiresOwner(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.ClaimNextRun("worker-1", time.Minute); err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	if _, err := store.FinishClaimedRun(run.ID, "worker-2", controlplane.RunSucceeded, "done"); err == nil {
		t.Fatal("FinishClaimedRun() succeeded for wrong worker")
	}
	finished, err := store.FinishClaimedRun(run.ID, "worker-1", controlplane.RunSucceeded, "done")
	if err != nil {
		t.Fatalf("FinishClaimedRun() error = %v", err)
	}
	if finished.Status != controlplane.RunSucceeded || finished.ClaimedBy != "" || finished.LeaseExpiresAt != nil {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestStoreRecoversExpiredLeases(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.ClaimNextRun("worker-1", time.Second); err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	requeued, failed, err := store.RecoverLeasedRuns(now.Add(2 * time.Second))
	if err != nil {
		t.Fatalf("RecoverLeasedRuns() error = %v", err)
	}
	if requeued != 0 || failed != 1 {
		t.Fatalf("requeued=%d failed=%d", requeued, failed)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != controlplane.RunFailed || detail.Run.Summary != "Worker lease expired" {
		t.Fatalf("run = %+v", detail.Run)
	}
}

func TestStoreRedactsSecretsFromPersistedModelContent(t *testing.T) {
	store := controlplane.NewStore(nil)
	req, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "secret-request", Model: "test", RequestBody: `{"messages":[{"role":"user","content":"use Bearer top-secret-token"}],"api_key":"sk-proj-abcdefghijklmnop"}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(req.RequestBody, "top-secret-token") || strings.Contains(req.RequestBody, "abcdefghijklmnop") || !strings.Contains(req.RequestBody, "[REDACTED]") {
		t.Fatalf("request body was not redacted: %s", req.RequestBody)
	}
	finished, err := store.FinishModelBrokerRequest(req.ID, controlplane.FinishModelBrokerRequestInput{ResponseBody: `{"content":"password=hunter2"}`, Error: "authorization: bearer another-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(finished.ResponseBody+finished.Error, "hunter2") || strings.Contains(finished.ResponseBody+finished.Error, "another-secret") {
		t.Fatalf("response/error was not redacted: %+v", finished)
	}
}

func TestStoreRejectsOversizedArtifacts(t *testing.T) {
	t.Setenv("SWITCHYARD_MAX_ARTIFACT_BYTES", "1024")
	store := controlplane.NewStore(nil)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddArtifact(controlplane.CreateArtifactInput{RunID: run.ID, Name: "large", Kind: "text", URI: "data:text/plain," + strings.Repeat("x", 1024)}); err != controlplane.ErrInvalid {
		t.Fatalf("AddArtifact() error = %v", err)
	}
}
