package controlplane

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type scriptedCIGiteaReader struct {
	findCalls  []string
	checkCalls []GiteaPullRequest
	findPR     GiteaPullRequest
	findPRs    []GiteaPullRequest
	findIdx    int
	findFound  bool
	findErr    error
	checks     []GiteaCheckSummary
	checkErr   error
	checkIdx   int
}

func (s *scriptedCIGiteaReader) FindPullRequestByHead(_ context.Context, _ string, head string) (GiteaPullRequest, bool, error) {
	s.findCalls = append(s.findCalls, head)
	if s.findErr != nil {
		return GiteaPullRequest{}, false, s.findErr
	}
	if len(s.findPRs) > 0 {
		index := min(s.findIdx, len(s.findPRs)-1)
		s.findIdx++
		pr := completeCITestPullRequest(s.findPRs[index])
		return pr, s.findFound, nil
	}
	pr := s.findPR
	if pr.State == "" {
		pr.State = "open"
	}
	return completeCITestPullRequest(pr), s.findFound, nil
}

func completeCITestPullRequest(pr GiteaPullRequest) GiteaPullRequest {
	if pr.Head.Repo == nil {
		pr.Head.Repo = pullRequestRepoIdentity("OWNER/REPO")
	}
	if pr.Base.Repo == nil {
		pr.Base.Repo = pullRequestRepoIdentity("OWNER/REPO")
	}
	if pr.Base.Ref == "" {
		pr.Base.Ref = "main"
	}
	if pr.Base.SHA == "" {
		pr.Base.SHA = "base123"
	}
	return pr
}

func (s *scriptedCIGiteaReader) PullRequestChecks(_ context.Context, _ string, pr GiteaPullRequest) (GiteaCheckSummary, error) {
	s.checkCalls = append(s.checkCalls, pr)
	if s.checkErr != nil {
		return GiteaCheckSummary{}, s.checkErr
	}
	if len(s.checks) == 0 {
		return GiteaCheckSummary{}, nil
	}
	if s.checkIdx >= len(s.checks) {
		return s.checks[len(s.checks)-1], nil
	}
	out := s.checks[s.checkIdx]
	s.checkIdx++
	return out, nil
}

func newOpenPilotCICheckTestEnv(store Store) *EnvWorkflowExecutors {
	env := newOpenPilotAIReviewTestEnv(store)
	env.modelExecutor = &captureModelExecutor{response: ModelResponse{Content: "APPROVED: implementation looks good"}}
	env.openPilotCICheckMode = "require-pass"
	env.openPilotCICheckTimeout = 50 * time.Millisecond
	env.openPilotCICheckInterval = 1 * time.Millisecond
	env.openPilotCIRequiredChecks = []string{"verify", "lint", "test"}
	return env
}

func openPilotCITestState() OpenPilotIssueState {
	return OpenPilotIssueState{
		Repo:           "OWNER/REPO",
		Issue:          "123",
		Phase:          OpenPilotPhaseFailed,
		Branch:         "open-pilot/issue-123",
		Base:           "main",
		PullRequestURL: "https://git/OWNER/REPO/pulls/7",
		PullRequestID:  99,
		PullRequestNum: 7,
		ResumeInputs: map[string]string{
			"resume.open-pilot.branch-plan.branch":      "open-pilot/issue-123",
			"resume.open-pilot.branch-plan.base_branch": "main",
		},
	}
}

func TestOpenPilotCIChecksPassesAndLooksUpPRByHead(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved"}
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "success",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}},
			Checks:   []GiteaCheckRun{{Name: "lint", Status: "completed", Conclusion: "success"}, {Name: "test", Status: "completed", Conclusion: "success"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	env.openPilotCICheckMode = "require-pass"
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseCIPassed || state.GateStatus["ci_checks"] != "passed" {
		t.Fatalf("state = %+v", state)
	}
	if len(reader.findCalls) != 2 || reader.findCalls[0] != "open-pilot/issue-123" || len(reader.checkCalls) != 1 || reader.checkCalls[0].Head.SHA != "abc123" {
		t.Fatalf("reader calls = %+v %+v", reader.findCalls, reader.checkCalls)
	}
	if detail, err := store.RunDetail(run.ID); err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	} else if len(detail.Steps) != 2 || detail.Steps[1].Name != "open-pilot.ci-checks" || detail.Steps[1].Status != StepSucceeded || !strings.Contains(detail.Steps[1].OutputSummary, "CI checks passed") {
		t.Fatalf("detail = %+v", detail.Steps)
	}
}

func TestOpenPilotCIChecksPreservesFreshlyCheckedPRForMerge(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	seed := openPilotCITestState()
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved"}
	seedOpenPilotIssueState(t, store, seed)
	checked := completeCITestPullRequest(GiteaPullRequest{ID: 99, Number: 7, State: "open", HTMLURL: seed.PullRequestURL, Head: GiteaPullRequestRef{Ref: seed.Branch, SHA: "abc123"}})
	fresh := checked
	fresh.Base.Ref = "main"
	reader := &scriptedCIGiteaReader{findPRs: []GiteaPullRequest{checked, fresh}, findFound: true, checks: []GiteaCheckSummary{{State: "success", SHA: "abc123", Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}}, Checks: []GiteaCheckRun{{Name: "lint", Status: "completed", Conclusion: "success"}, {Name: "test", Status: "completed", Conclusion: "success"}}}}}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": seed.Repo, "issue": seed.Issue, "branch": seed.Branch}})
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"open-pilot.pull-request": checked, "open-pilot.gate-status": map[string]string{"ai_review": "approved"}}
	if _, err := (openPilotExecutor{env: env}).executeOpenPilotCIChecks(t.Context(), run, state); err != nil {
		t.Fatal(err)
	}
	if got, ok := state["open-pilot.ci-passed-pull-request"].(GiteaPullRequest); !ok || !reflect.DeepEqual(got, fresh) {
		t.Fatalf("CI-passed PR = %+v", state["open-pilot.ci-passed-pull-request"])
	}
	saved, err := store.OpenPilotIssueState(seed.Repo, seed.Issue)
	if err != nil || saved.ResumeInputs[openPilotCIPassedHeadSHAKey] != "abc123" {
		t.Fatalf("saved CI head = %+v, %v", saved.ResumeInputs, err)
	}
	recovered := map[string]any{}
	(openPilotExecutor{env: env}).hydrateOpenPilotPipelineStateFromIssueState(run, saved, recovered)
	if got, ok := recovered["open-pilot.ci-passed-pull-request"].(GiteaPullRequest); !ok || got.Head.SHA != "abc123" || got.Number != fresh.Number {
		t.Fatalf("recovered CI-passed PR = %+v", recovered["open-pilot.ci-passed-pull-request"])
	}
}

func TestOpenPilotCIChecksRejectsPassedSnapshotWhenFreshPRChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*GiteaPullRequest)
	}{
		{name: "head", mutate: func(pr *GiteaPullRequest) { pr.Head.SHA = "new-head" }},
		{name: "identity", mutate: func(pr *GiteaPullRequest) { pr.Number++ }},
		{name: "closed", mutate: func(pr *GiteaPullRequest) { pr.State = "closed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			seed := openPilotCITestState()
			seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved"}
			seedOpenPilotIssueState(t, store, seed)
			checked := GiteaPullRequest{ID: 99, Number: 7, State: "open", HTMLURL: seed.PullRequestURL, Head: GiteaPullRequestRef{Ref: seed.Branch, SHA: "abc123"}}
			fresh := checked
			test.mutate(&fresh)
			reader := &scriptedCIGiteaReader{findPRs: []GiteaPullRequest{checked, fresh}, findFound: true, checks: []GiteaCheckSummary{{State: "success", SHA: "abc123"}}}
			env := newOpenPilotCICheckTestEnv(store)
			env.giteaReader = reader
			run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": seed.Repo, "issue": seed.Issue, "branch": seed.Branch}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = (openPilotExecutor{env: env}).executeOpenPilotCIChecks(t.Context(), run, map[string]any{"open-pilot.pull-request": checked})
			if err == nil {
				t.Fatal("expected stale passed snapshot rejection")
			}
			state, stateErr := store.OpenPilotIssueState(seed.Repo, seed.Issue)
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if state.Phase == OpenPilotPhaseCIPassed {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestOpenPilotCIChecksFailsOnRequiredCheckFailure(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seed.PullRequestID = 99
	seed.PullRequestNum = 7
	seed.ResumeInputs["resume.open-pilot.pr-plan.pull_request_url"] = seed.PullRequestURL
	seed.PullRequestNum = 7
	seed.PullRequestID = 99
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "failure",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}},
			Checks:   []GiteaCheckRun{{Name: "lint", Status: "completed", Conclusion: "failure"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected ci checks failure")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseFailed || state.GateStatus["ci_checks"] != "failed" || state.FailingGate != "ci_checks" || state.RecommendedNextAction != "fix_agent_commit" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksTimeoutsOnPendingChecks(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks:    []GiteaCheckSummary{{State: "pending", SHA: "abc123"}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	env.openPilotCICheckTimeout = 5 * time.Millisecond
	env.openPilotCICheckInterval = time.Millisecond
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil {
		t.Fatal("expected ci checks timeout")
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseFailed || state.GateStatus["ci_checks"] != "timeout" || state.FailingGate != "ci_checks" || state.RecommendedNextAction != "wait_ci" || state.OperatorSummary == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksFailsOnAggregateFailureWithUnmatchedRequiredNames(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved"}
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", Label: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "failure",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "template-check / verify (push)", Status: "failure"}, {Context: "template-check / verify (pull_request)", Status: "failure"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ci-checks", Position: 10, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil || !strings.Contains(err.Error(), "aggregate status failure") {
		t.Fatalf("expected aggregate failure, got %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseFailed || state.GateStatus["ci_checks"] != "failed" || state.RecommendedNextAction != "fix_agent_commit" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksPassesOnAggregateSuccessWithUnmatchedRequiredNames(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved"}
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", Label: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "success",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "template-check / verify (push)", Status: "success"}, {Context: "template-check / verify (pull_request)", Status: "success"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ci-checks", Position: 10, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseCIPassed || state.GateStatus["ci_checks"] != "passed" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksDisabledSkipsAndDoesNotPass(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seed.PullRequestID = 99
	seed.PullRequestNum = 7
	seed.ResumeInputs["resume.open-pilot.pr-plan.pull_request_url"] = seed.PullRequestURL
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seedOpenPilotIssueState(t, store, seed)
	env := newOpenPilotCICheckTestEnv(store)
	env.openPilotCICheckMode = "disabled"
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseAIReviewed || state.GateStatus["ci_checks"] != "skipped" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksDisabledDoesNotCallGitea(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{findErr: errors.New("GITEA_BASE_URL is not configured"), checkErr: errors.New("must not check")}
	env := newOpenPilotCICheckTestEnv(store)
	env.openPilotCICheckMode = "disabled"
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	if len(reader.findCalls) != 0 || len(reader.checkCalls) != 0 {
		t.Fatalf("disabled CI should not call Gitea: find=%+v checks=%+v", reader.findCalls, reader.checkCalls)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseAIReviewed || state.GateStatus["ci_checks"] != "skipped" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksRejectsUnknownMode(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seedOpenPilotIssueState(t, store, seed)
	env := newOpenPilotCICheckTestEnv(store)
	env.openPilotCICheckMode = "disable"
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "branch": "open-pilot/issue-123"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ai-review", Position: 8, Name: "open-pilot.ai-review", Executor: "model", Profile: "operator-reviewer", Model: "${model}"}, {ID: "ci-checks", Position: 9, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	_, err = runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env})
	if err == nil || !strings.Contains(err.Error(), "unsupported OPEN_PILOT_CI_CHECK_MODE") {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestOpenPilotCIChecksResumeUsesPersistedGateStatus(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.Phase = OpenPilotPhaseFailed
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "timeout"}
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seed.ResumeInputs["resume.open-pilot.pr-plan.pull_request_url"] = seed.PullRequestURL
	seedOpenPilotIssueState(t, store, seed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "success",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}, {Context: "lint", Status: "success"}, {Context: "test", Status: "success"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "retry_from_step": "open-pilot.ci-checks", "retry_from_step_position": "10"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ci-checks", Position: 10, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	if len(reader.findCalls) != 2 || len(reader.checkCalls) != 1 {
		t.Fatalf("expected CI gate to run on resume: find=%+v check=%+v", reader.findCalls, reader.checkCalls)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseCIPassed || state.GateStatus["ci_checks"] != "passed" {
		t.Fatalf("state = %+v", state)
	}
}

func TestOpenPilotCIChecksResumeFromAIReviewedUsesPersistedGateStatus(t *testing.T) {
	store := NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed := openPilotCITestState()
	seed.Phase = OpenPilotPhaseAIReviewed
	seed.GateStatus = OpenPilotGateStatus{"ai_review": "approved", "ci_checks": "skipped"}
	seed.PullRequestURL = "https://git/OWNER/REPO/pulls/7"
	seed.PullRequestNum = 7
	seed.ResumeInputs["resume.open-pilot.pr-plan.pull_request_url"] = seed.PullRequestURL
	seed.ResumeInputs["resume.open-pilot.pr-plan.pull_request_number"] = "7"
	seedOpenPilotStateAtPhase(t, store, seed, OpenPilotPhaseAIReviewed)
	reader := &scriptedCIGiteaReader{
		findPR:    GiteaPullRequest{ID: 99, Number: 7, HTMLURL: "https://git/OWNER/REPO/pulls/7", Head: GiteaPullRequestRef{Ref: "open-pilot/issue-123", SHA: "abc123"}},
		findFound: true,
		checks: []GiteaCheckSummary{{
			State:    "success",
			SHA:      "abc123",
			Statuses: []GiteaCommitStatus{{Context: "verify", Status: "success"}, {Context: "lint", Status: "success"}, {Context: "test", Status: "success"}},
		}},
	}
	env := newOpenPilotCICheckTestEnv(store)
	env.giteaReader = reader
	runner := &Runner{store: store}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "retry_from_step": "open-pilot.ci-checks", "retry_from_step_position": "10"}})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	pipeline := Pipeline{ID: "test", WorkflowID: string(WorkflowOpenPilot), Steps: []PipelineStep{{ID: "ci-checks", Position: 10, Name: "open-pilot.ci-checks", Executor: "gitea"}}}
	if _, err := runner.executePipeline(t.Context(), run, pipeline, openPilotExecutor{env: env}, openPilotExecutor{env: env}); err != nil {
		t.Fatalf("executePipeline() error = %v", err)
	}
	if len(reader.findCalls) != 2 || len(reader.checkCalls) != 1 {
		t.Fatalf("expected CI gate to run from ai_reviewed resume: find=%+v check=%+v", reader.findCalls, reader.checkCalls)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != OpenPilotPhaseCIPassed || state.GateStatus["ci_checks"] != "passed" {
		t.Fatalf("state = %+v", state)
	}
}
