package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"
)

type captureModelExecutor struct {
	requests []ModelRequest
	response ModelResponse
	err      error
}

func (c *captureModelExecutor) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	_ = ctx
	c.requests = append(c.requests, request)
	if c.err != nil {
		return ModelResponse{}, c.err
	}
	return c.response, nil
}

type captureOCQExecutor struct {
	requests []OCQRequest
	response OCQResponse
	err      error
}

func (c *captureOCQExecutor) SendPrompt(ctx context.Context, request OCQRequest) (OCQResponse, error) {
	_ = ctx
	c.requests = append(c.requests, request)
	if c.err != nil {
		return OCQResponse{}, c.err
	}
	return c.response, nil
}

func newOpenPilotAIReviewTestEnv(store Store) *EnvWorkflowExecutors {
	return &EnvWorkflowExecutors{
		store:                 store,
		llamaLineBaseURL:      "http://mock-review-backend",
		openPilotModel:        "review-model",
		modelExecutor:         &captureModelExecutor{},
		ocqExecutor:           &captureOCQExecutor{response: OCQResponse{Content: "agent output"}},
		openPilotBaseBranch:   "main",
		openPilotBranchPrefix: "open-pilot/",
	}
}

func newOpenPilotIssueState(repo, issue string, phase OpenPilotPhase, attempts, maxAttempts int, resumeInputs map[string]string, gateStatus OpenPilotGateStatus) OpenPilotIssueState {
	return OpenPilotIssueState{
		Repo:              repo,
		Issue:             issue,
		Phase:             phase,
		RepairAttempts:    attempts,
		MaxRepairAttempts: maxAttempts,
		ResumeInputs:      resumeInputs,
		GateStatus:        gateStatus,
	}
}

func seedOpenPilotIssueState(t *testing.T, store Store, state OpenPilotIssueState) {
	t.Helper()
	if _, err := store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: state.Repo, Issue: state.Issue, Phase: OpenPilotPhaseRequested}); err != nil {
		t.Fatalf("seed requested state: %v", err)
	}
	if state.Phase != OpenPilotPhaseRequested {
		if _, err := store.UpsertOpenPilotIssueState(state); err != nil {
			t.Fatalf("seed issue state: %v", err)
		}
	}
}

func TestOpenPilotAIReviewUsesNativeModelBrokerAvailability(t *testing.T) {
	run := Run{Inputs: map[string]string{"resume.open-pilot.pr-plan.pull_request_url": "https://git/OWNER/REPO/pulls/7"}}
	step := PipelineStep{ID: "ai-review", Name: "open-pilot.ai-review", Executor: "model"}
	if !(openPilotExecutor{env: &EnvWorkflowExecutors{modelExecutor: &EnvModelBroker{llamaLineBaseURL: "http://llama-line"}}}).ShouldExecutePipelineStep(run, step, map[string]any{}) {
		t.Fatalf("ai-review should run when the native model broker has a configured model-broker backend")
	}
	if (openPilotExecutor{env: &EnvWorkflowExecutors{modelExecutor: &EnvModelBroker{}}}).ShouldExecutePipelineStep(run, step, map[string]any{}) {
		t.Fatalf("ai-review should skip when the native model broker has no configured backend")
	}
}

func TestOpenPilotAIReviewApprovesAfterPRCreation(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: implementation looks good", CorrelationIDs: map[string]string{"model_request_id": "req-1"}}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = model
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo":   "OWNER/REPO",
		"issue":  "123",
		"branch": "open-pilot/issue-123",
		"resume.open-pilot.pr-plan.pull_request_url": "https://git/OWNER/REPO/pulls/7",
		"resume.open-pilot.verify-command.summary":   "Command allowed by policy in dry-run mode",
		"resume.open-pilot.agent-work.summary":       "agent summary",
		"resume.open-pilot.agent-work.response":      "agent response",
	}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseAIReviewed || state.GateStatus["ai_review"] != "approved" {
		t.Fatalf("state = %+v", state)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].Messages[1].Content, "Pull request URL: https://git/OWNER/REPO/pulls/7") {
		t.Fatalf("model request = %+v", model.requests)
	}
}

func TestOpenPilotAIReviewRequestsChangesAndStopsPipeline(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	model := &captureModelExecutor{response: ModelResponse{Content: "CHANGES_REQUESTED: tighten validation and update docs", CorrelationIDs: map[string]string{"model_request_id": "req-2"}}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = model
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo":   "OWNER/REPO",
		"issue":  "123",
		"branch": "open-pilot/issue-123",
		"resume.open-pilot.pr-plan.pull_request_url": "https://git/OWNER/REPO/pulls/7",
		"resume.open-pilot.verify-command.summary":   "Command allowed",
		"resume.open-pilot.agent-work.response":      "agent response",
		"resume.open-pilot.agent-work.summary":       "agent summary",
	}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "after-ai-review", Position: 9, Name: "open-pilot.after-ai-review", Executor: "internal"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected ai review rejection error")
	}
	if len(model.requests) != 1 {
		t.Fatalf("model requests = %+v", model.requests)
	}
	if detail, err := store.RunDetail(run.ID); err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	} else if len(detail.Steps) != 1 || detail.Steps[0].Name != "open-pilot.ai-review" || detail.Steps[0].Status != StepFailed {
		t.Fatalf("detail = %+v", detail)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseFailed || state.RepairAttempts != 1 || state.GateStatus["ai_review"] != "changes_requested" || state.ResumeInputs["ai_review.feedback"] == "" || state.ResumeInputs["retry_from_step"] != "open-pilot.agent-work" || state.FailingGate != "ai_review" || state.RecommendedNextAction != "fix_agent_commit" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotAIReviewPausesAtMaxAttempts(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seedOpenPilotIssueState(t, store, newOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhasePaused, 1, 2, nil, nil))
	model := &captureModelExecutor{response: ModelResponse{Content: "CHANGES_REQUESTED: still needs work"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = model
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo":   "OWNER/REPO",
		"issue":  "123",
		"branch": "open-pilot/issue-123",
		"resume.open-pilot.pr-plan.pull_request_url": "https://git/OWNER/REPO/pulls/7",
		"resume.open-pilot.verify-command.summary":   "Command allowed",
		"resume.open-pilot.agent-work.response":      "agent response",
	}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err == nil {
		t.Fatal("expected ai review rejection error")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhasePaused || state.RepairAttempts != 2 || state.GateStatus["ai_review"] != "changes_requested" || state.FailingGate != "ai_review" || state.RecommendedNextAction != "fix_agent_commit" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotResumePromptIncludesAiReviewFeedback(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seedOpenPilotIssueState(t, store, newOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhasePaused, 1, 2, map[string]string{
		"resume.open-pilot.branch-plan.branch":      "open-pilot/issue-123",
		"resume.open-pilot.branch-plan.base_branch": "main",
		"ai_review.feedback":                        "Please tighten validation and add tests",
	}, OpenPilotGateStatus{"ai_review": "changes_requested", "local_verify": "passed"}))
	ocq := &captureOCQExecutor{response: OCQResponse{Content: "agent output"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.ocqExecutor = ocq
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo":                     "OWNER/REPO",
		"issue":                    "123",
		"retry_from_step_position": "4",
		"retry_from_step":          "open-pilot.agent-work",
	}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "agent-work", Position: 4, Name: "open-pilot.agent-work", Executor: "opencode", Profile: "open-pilot-executor", Model: "${model}"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	if len(ocq.requests) != 1 || !strings.Contains(ocq.requests[0].Prompt, "Please tighten validation and add tests") {
		t.Fatalf("prompt = %q", ocq.requests)
	}
}

func TestOpenPilotAIReviewSkippedWithoutKnownPullRequest(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	env := newOpenPilotAIReviewTestEnv(store)
	executor := openPilotExecutor{env: env}
	run := Run{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}}
	step := PipelineStep{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model"}
	if executor.ShouldExecutePipelineStep(run, step, map[string]any{}) {
		t.Fatal("ai-review should be skipped without a known pull request")
	}
	if !executor.ShouldExecutePipelineStep(run, step, map[string]any{"open-pilot.pull-request": GiteaPullRequest{HTMLURL: "https://git/OWNER/REPO/pulls/7"}}) {
		t.Fatal("ai-review should run with a known pull request")
	}
}

func TestParseOpenPilotAIReviewResponseRequiresRationale(t *testing.T) {
	if verdict, feedback, err := parseOpenPilotAIReviewResponse("Approved: looks good"); err != nil || verdict != "approved" || feedback != "looks good" {
		t.Fatalf("parse lowercase approval = %q %q %v", verdict, feedback, err)
	}
	if _, _, err := parseOpenPilotAIReviewResponse("APPROVED:"); err == nil {
		t.Fatal("expected empty approval rationale to fail")
	}
	if _, _, err := parseOpenPilotAIReviewResponse("CHANGES_REQUESTED:"); err == nil {
		t.Fatal("expected empty changes feedback to fail")
	}
	if _, _, err := parseOpenPilotAIReviewResponse("APPROVED: ok\n\nextra paragraph"); err == nil {
		t.Fatal("expected multi-paragraph approval to fail")
	}
	if _, _, err := parseOpenPilotAIReviewResponse("APPROVED: ok\nCHANGES_REQUESTED: actually no"); err == nil {
		t.Fatal("expected multi-verdict output to fail")
	}
}

func TestOpenPilotAIReviewApprovalClearsRepairCursor(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := newOpenPilotIssueState("OWNER/REPO", "123", OpenPilotPhaseFailed, 1, 2, map[string]string{
		"retry_from_step":                            "open-pilot.agent-work",
		"retry_from_step_position":                   "4",
		"ai_review.feedback":                         "tighten validation",
		"resume.open-pilot.pr-plan.pull_request_url": "https://git/OWNER/REPO/pulls/7",
	}, OpenPilotGateStatus{"ai_review": "changes_requested"})
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seedOpenPilotIssueState(t, store, seed)
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: implementation now satisfies the review"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = model
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.GateStatus["ai_review"] != "approved" || state.RepairAttempts != 0 || state.ResumeInputs["ai_review.feedback"] != "" || state.ResumeInputs["retry_from_step"] != "" || state.ResumeInputs["retry_from_step_position"] != "" {
		t.Fatalf("state = %+v", state)
	}
}
