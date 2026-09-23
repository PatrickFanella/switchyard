package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubIssueFetcher struct {
	issue GiteaIssue
	err   error
	calls int
}

const liveIssue42AutomaticCIRepairAcceptance = "This issue is a two-phase production proof. Phase one is complete when the committed probe causes the required PR check to fail and Open Pilot records that concrete CI failure. After that observation, the accepted repair is to remove or neutralize the intentional CI-only failure probe while preserving repository conventions. Final acceptance requires the repaired branch to pass the required CI check, reuse and merge PR #43, and close this issue automatically. The failing probe is test scaffolding and must not remain in the merged branch."

func (f *stubIssueFetcher) FetchIssue(context.Context, string, string) (GiteaIssue, error) {
	f.calls++
	return f.issue, f.err
}

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

type captureOpenCodeExecutor struct {
	requests []OpenCodeRequest
	response OpenCodeResponse
	err      error
}

func (c *captureOpenCodeExecutor) SendPrompt(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error) {
	_ = ctx
	c.requests = append(c.requests, request)
	if c.err != nil {
		return OpenCodeResponse{}, c.err
	}
	return c.response, nil
}

func newOpenPilotAIReviewTestEnv(store Store) *EnvWorkflowExecutors {
	return &EnvWorkflowExecutors{
		store:                 store,
		llamaLineBaseURL:      "http://mock-review-backend",
		openPilotModel:        "review-model",
		modelExecutor:         &captureModelExecutor{},
		openCodeExecutor:      &captureOpenCodeExecutor{response: OpenCodeResponse{Content: "agent output"}},
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
	if !(openPilotExecutor{env: &EnvWorkflowExecutors{modelExecutor: &EnvModelBroker{openCodeBaseURL: "http://opencode"}}}).ShouldExecutePipelineStep(run, step, map[string]any{}) {
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

func TestOpenPilotAIReviewUsesBoundedRefreshedIssueAndCIRepairContext(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: the accepted repair removes the intentional CI-only failure probe before PR #43 merges"}}
	fetcher := &stubIssueFetcher{issue: GiteaIssue{ID: 1558, Number: 42, Repository: "subculture-collective/switchyard", State: "open", Labels: []string{"agent:queued", "ready-for-agent"}, Title: "Probe Open Pilot behavior after CI failure", Body: "## Automatic CI repair acceptance\n\n" + liveIssue42AutomaticCIRepairAcceptance + strings.Repeat("b", openPilotCIRepairMaxPromptBytes)}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor, env.issueFetcher = model, fetcher
	env.openPilotLabels, env.requireIssueSpec = []string{"ready-for-agent"}, true
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
		"repo": "subculture-collective/switchyard", "issue": "42", "resume.open-pilot.pr-plan.pull_request_url": "https://git.subcult.tv/subculture-collective/switchyard/pulls/43",
		openPilotCIRepairCountKey: "1", openPilotCIRepairFeedbackKey: "Required CI failed with intentional CI repair-loop probe; Open Pilot recorded failing_gate=ci_checks." + strings.Repeat("f", openPilotCIRepairMaxFeedbackBytes),
		openPilotCIRepairOriginalPromptKey: liveIssue42AutomaticCIRepairAcceptance, openPilotCIRepairParentRunIDKey: "must-not-appear",
		openPilotCIRepairRepoKey: "control-repo", openPilotCIRepairFailedHeadSHAKey: "control-sha",
	}})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model"}}}
	if _, err := (&Runner{store: store}).executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatal(err)
	}
	if fetcher.calls != 1 || len(model.requests) != 1 {
		t.Fatalf("fetch/model calls = %d/%d", fetcher.calls, len(model.requests))
	}
	prompt := model.requests[0].Messages[1].Content
	for _, want := range []string{"Current issue title", "Probe Open Pilot behavior after CI failure", liveIssue42AutomaticCIRepairAcceptance, "Automatic CI repair context", "Failed-check feedback", "Open Pilot recorded failing_gate=ci_checks", "current issue's ultimate outcome", "untrusted data"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %q", want, prompt)
		}
	}
	for _, forbidden := range []string{"must-not-appear", "control-repo", "control-sha", strings.Repeat("b", openPilotCIRepairMaxPromptBytes), strings.Repeat("f", openPilotCIRepairMaxFeedbackBytes)} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt contains unbounded/control value %q", forbidden)
		}
	}
	if model.requests[0].ToolChoice != "none" || !strings.Contains(model.requests[0].Messages[0].Content, "do not require preserving an implementation that caused the recorded CI failure") {
		t.Fatalf("review policy/tool choice = %+v", model.requests[0])
	}
}

func TestOpenPilotAIReviewPrefersCurrentRepairAttemptEvidence(t *testing.T) {
	inputs := map[string]string{
		"repo": "OWNER/REPO", "issue": "123",
		"resume.open-pilot.agent-work.summary":       "stale initial agent summary",
		"resume.open-pilot.agent-work.response":      "stale initial agent response",
		"resume.open-pilot.verify-command.summary":   "stale initial verification",
		"resume.open-pilot.commit-changes.sha":       strings.Repeat("a", 40),
		"resume.open-pilot.commit-changes.subject":   "stale initial commit",
		"resume.open-pilot.commit-changes.diffstat":  "stale.txt | 1 +",
		"resume.open-pilot.pr-plan.pull_request_url": "https://git.example/OWNER/REPO/pulls/7",
		openPilotCIRepairCountKey:                    "1",
		openPilotCIRepairFeedbackKey:                 "append the repair marker",
	}
	state := map[string]any{
		"open-pilot.agent-summary":  "current repair agent summary",
		"open-pilot.response":       "current repair agent response",
		"open-pilot.verify-summary": "current repair verification",
		"open-pilot.commit-result": OpenPilotCommitResult{
			SHA: strings.Repeat("b", 40), Subject: "current repair commit", DiffStat: "PROOF.md | 1 +",
		},
	}
	prompt := buildOpenPilotAIReviewPrompt("OWNER/REPO", "123", inputs, state)
	for _, want := range []string{"current repair agent summary", "current repair agent response", "current repair verification", strings.Repeat("b", 40), "current repair commit", "PROOF.md | 1 +"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing current evidence %q: %q", want, prompt)
		}
	}
	for _, stale := range []string{"stale initial agent summary", "stale initial agent response", "stale initial verification", strings.Repeat("a", 40), "stale initial commit", "stale.txt | 1 +"} {
		if strings.Contains(prompt, stale) {
			t.Fatalf("prompt retained stale evidence %q: %q", stale, prompt)
		}
	}
}

func TestOpenPilotAIReviewIssueRefreshFailureUsesValidationPolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		triggerID string
		baseURL   string
		wantErr   bool
	}{
		{name: "gitea poll fails closed", triggerID: "gitea-open-pilot-poll", wantErr: true},
		{name: "configured gitea fails closed", baseURL: "https://git.example", wantErr: true},
		{name: "non-gitea run preserves optional fetch", wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITEA_BASE_URL", test.baseURL)
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: current evidence satisfies the issue"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.modelExecutor = model
			env.issueFetcher = &stubIssueFetcher{err: errors.New("issue unavailable" + strings.Repeat("x", openPilotCIRepairMaxFeedbackBytes))}
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), TriggerID: test.triggerID, Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "pull_request_url": "https://git/pulls/7"}})
			if err != nil {
				t.Fatal(err)
			}
			pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model"}}}
			_, err = (&Runner{store: store}).executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %t", err, test.wantErr)
			}
			if test.wantErr && len(model.requests) != 0 {
				t.Fatalf("model called after issue fetch failure: %+v", model.requests)
			}
			if test.wantErr {
				state, stateErr := store.OpenPilotIssueState("OWNER/REPO", "123")
				if stateErr != nil || state.Phase != OpenPilotPhaseFailed || state.LastStepName != "open-pilot.ai-review" || len(state.LastError) != openPilotCIRepairMaxFeedbackBytes {
					t.Fatalf("state = %+v, error = %v", state, stateErr)
				}
			}
		})
	}
}

func TestOpenPilotAIReviewRejectsInvalidRefreshedIssue(t *testing.T) {
	valid := GiteaIssue{
		ID: 1558, Number: 42, Repository: "subculture-collective/switchyard", State: "open",
		Labels: []string{"agent:queued", "ready-for-agent"}, Title: "Probe Open Pilot behavior after CI failure",
		Body: "## Automatic CI repair acceptance\n\n" + liveIssue42AutomaticCIRepairAcceptance,
	}
	for _, test := range []struct {
		name    string
		mutate  func(*GiteaIssue)
		wantErr string
	}{
		{name: "closed", mutate: func(issue *GiteaIssue) { issue.State = "closed" }, wantErr: "not open"},
		{name: "missing required label", mutate: func(issue *GiteaIssue) { issue.Labels = []string{"agent:queued"} }, wantErr: "missing required labels"},
		{name: "invalid spec", mutate: func(issue *GiteaIssue) { issue.Body = "Two phase cleanup." }, wantErr: "acceptance criteria and test instructions"},
		{name: "mismatched number", mutate: func(issue *GiteaIssue) { issue.Number = 41 }, wantErr: "identity does not match"},
		{name: "mismatched repository", mutate: func(issue *GiteaIssue) { issue.Repository = "other/switchyard" }, wantErr: "identity does not match"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := valid
			test.mutate(&current)
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: unreachable"}}
			env := newOpenPilotAIReviewTestEnv(store)
			env.modelExecutor, env.issueFetcher = model, &stubIssueFetcher{issue: current}
			env.openPilotLabels, env.requireIssueSpec = []string{"ready-for-agent"}, true
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{
				"repo": "subculture-collective/switchyard", "issue": "42", "pull_request_url": "https://git.subcult.tv/subculture-collective/switchyard/pulls/43",
			}})
			if err != nil {
				t.Fatal(err)
			}
			pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model"}}}
			_, err = (&Runner{store: store}).executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
			if len(model.requests) != 0 {
				t.Fatalf("model called after refreshed issue rejection: %+v", model.requests)
			}
			state, stateErr := store.OpenPilotIssueState("subculture-collective/switchyard", "42")
			if stateErr != nil || state.Phase != OpenPilotPhaseFailed || state.LastStepName != "open-pilot.ai-review" || !strings.Contains(state.LastError, test.wantErr) || len(state.LastError) > openPilotCIRepairMaxFeedbackBytes {
				t.Fatalf("state = %+v, error = %v", state, stateErr)
			}
		})
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
		"resume.open-pilot.branch-plan.branch":        "open-pilot/issue-123",
		"resume.open-pilot.branch-plan.base_branch":   "main",
		"resume.open-pilot.branch-plan.worktree_path": "/tmp/worktree",
		"resume.open-pilot.branch-plan.executed":      "true",
		"ai_review.feedback":                          "Please tighten validation and add tests",
	}, OpenPilotGateStatus{"ai_review": "changes_requested", "local_verify": "passed"}))
	ocq := &captureOpenCodeExecutor{response: OpenCodeResponse{Content: "agent output"}}
	env := newOpenPilotAIReviewTestEnv(store)
	env.openCodeExecutor = ocq
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
		t.Fatalf("prompt = %v", ocq.requests)
	}
	if ocq.requests[0].WorkDir != "/tmp/worktree" {
		t.Fatalf("workdir = %q", ocq.requests[0].WorkDir)
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
