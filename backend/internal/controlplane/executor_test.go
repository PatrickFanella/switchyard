package controlplane_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestEnvWorkflowExecutorsDispatchesOpenPilotCommand(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "args.txt")
	scriptPath := filepath.Join(dir, "open-pilot-test")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" > \"" + logPath + "\"\nprintf '{\"ok\":true}'\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("OPEN_PILOT_COMMAND", scriptPath)

	executor := controlplane.NewEnvWorkflowExecutors().ExecutorFor("open-pilot")
	execution, err := executor.Execute(t.Context(), controlplane.Run{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "PatrickFanella/switchyard", "issue": "42"}})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	if string(args) != "run --repo PatrickFanella/switchyard --issue 42 --json\n" {
		t.Fatalf("args = %q", string(args))
	}
	if execution.Summary != "Dispatched Open Pilot issue 42" || execution.Events[1].Type != "open-pilot.accepted" {
		t.Fatalf("execution = %+v", execution)
	}
}

func TestRunnerRunsOpenPilotPromptPipelineWithoutOCQ(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("GITEA_BASE_URL", "")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunPaused || run.Summary != "Prepared Open Pilot prompt for issue 123" {
		t.Fatalf("run = %+v", run)
	}
	if len(detail.Steps) != 4 || detail.Steps[1].Name != "open-pilot.branch-plan" || detail.Steps[3].Executor != "opencode" || detail.Steps[3].OutputSummary != "OpenCode executor unavailable; prompt prepared for operator handoff" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if len(detail.Artifacts) != 2 || detail.Artifacts[1].Name != "open-pilot-prompt.md" {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != controlplane.OpenPilotPhasePaused || state.LastStepName != "open-pilot.agent-work" || !strings.Contains(state.LastError, "not configured") || state.ResumeInputs["resume.open-pilot.prompt"] == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestRunnerFetchesOpenPilotIssueWhenGiteaConfigured(t *testing.T) {
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repos/OWNER/REPO/issues/123" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 4 || detail.Steps[0].OutputSummary != "Fetched issue: Fix switchyard (open)" || detail.Steps[1].OutputSummary != "Planned branch open-pilot/issue-123-fix-switchyard" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	foundIssueArtifact := false
	for _, artifact := range detail.Artifacts {
		if artifact.Name == "open-pilot-issue.json" && artifact.Kind == "json" {
			foundIssueArtifact = true
		}
	}
	if !foundIssueArtifact {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerRejectsOpenPilotIssueMissingRequiredLabels(t *testing.T) {
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"}]}`))
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunFailed || !strings.Contains(run.Summary, "agent:queued") {
		t.Fatalf("run = %+v", run)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != controlplane.StepFailed {
		t.Fatalf("steps = %+v", detail.Steps)
	}
}

func TestRunnerRunsOpenPilotPromptPipelineWithOCQ(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	var requestBody map[string]any
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("X-OCQ-Request-ID", "req-1")
		w.Header().Set("X-OCQ-Session", "ses-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_MODEL", "opencode-open-pilot")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Summary != "Ran Open Pilot issue 123 through OCQ" || len(detail.Steps) != 7 {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	if detail.Steps[3].Model != "opencode-open-pilot" || detail.Steps[3].CorrelationIDs["ocq_request_id"] != "req-1" || detail.Steps[3].CorrelationIDs["ocq_session_id"] != "ses-1" {
		t.Fatalf("agent step = %+v", detail.Steps[3])
	}
	if detail.Steps[3].CorrelationIDs["worktree_path"] != detail.Steps[1].CorrelationIDs["worktree_path"] {
		t.Fatalf("agent step correlations = %+v branch = %+v", detail.Steps[3].CorrelationIDs, detail.Steps[1].CorrelationIDs)
	}
	if requestBody["directory"] != detail.Steps[1].CorrelationIDs["worktree_path"] || requestBody["directory"] == "" {
		t.Fatalf("request body = %+v worktree = %q", requestBody, detail.Steps[1].CorrelationIDs["worktree_path"])
	}
	found := map[string]bool{}
	for _, artifact := range detail.Artifacts {
		found[artifact.Name] = true
	}
	for _, name := range []string{"open-pilot-response.md", "open-pilot-push-plan.json", "open-pilot-pr-plan.json", "open-pilot-worktree-cleanup.json"} {
		if !found[name] {
			t.Fatalf("artifacts = %+v", detail.Artifacts)
		}
	}
}

func TestRunnerPausesOpenPilotAgentWorkWithoutExecutedWorktree(t *testing.T) {
	for _, mode := range []string{"disabled", "dry-run"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			repoPath := initTestGitRepo(t)
			worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
			var ocqCalls int32
			ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&ocqCalls, 1)
				t.Fatalf("OCQ should not be called when worktree mode is %s", mode)
			}))
			t.Cleanup(ocq.Close)
			t.Setenv("OCQ_BASE_URL", ocq.URL)
			t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
			t.Setenv("OPEN_PILOT_WORKTREE_MODE", mode)
			t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
			t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

			store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
			if err := controlplane.SeedDefaults(store); err != nil {
				t.Fatalf("SeedDefaults() error = %v", err)
			}
			runner := controlplane.NewRunner(store)
			run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
			if err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			detail, err := store.RunDetail(run.ID)
			if err != nil {
				t.Fatalf("RunDetail() error = %v", err)
			}
			if atomic.LoadInt32(&ocqCalls) != 0 {
				t.Fatalf("ocqCalls = %d", ocqCalls)
			}
			if run.Summary != "Open Pilot paused: isolated worktree required" || run.Status != controlplane.RunPaused {
				t.Fatalf("run = %+v", run)
			}
			if len(detail.Steps) != 4 || detail.Steps[3].OutputSummary != "Open Pilot paused: isolated worktree required" {
				t.Fatalf("steps = %+v", detail.Steps)
			}
			state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
			if err != nil {
				t.Fatalf("OpenPilotIssueState() error = %v", err)
			}
			if state.Phase != controlplane.OpenPilotPhasePaused || state.LastStepName != "open-pilot.agent-work" || state.LastError != "open-pilot agent-work requires OPEN_PILOT_WORKTREE_MODE=allow to create an isolated worktree" {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestRunnerRejectsOpenPilotMismatchedRepoPathOrigin(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	runGitForTest(t, repoPath, "remote", "set-url", "origin", "https://git.example.test/OWNER/OTHER.git")
	var ocqCalls int32
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&ocqCalls, 1)
		t.Fatalf("OCQ should not be called for mismatched repo path")
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if atomic.LoadInt32(&ocqCalls) != 0 {
		t.Fatalf("ocqCalls = %d", ocqCalls)
	}
	if run.Status != controlplane.RunFailed || !strings.Contains(run.Summary, "does not match requested repo") {
		t.Fatalf("run = %+v", run)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != controlplane.OpenPilotPhaseFailed || state.LastStepName != "open-pilot.branch-plan" || !strings.Contains(state.LastError, "OWNER/OTHER") {
		t.Fatalf("state = %+v", state)
	}
}

func TestRunnerRunsOpenPilotDryRunVerifyCommand(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "status,diff")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 8 || detail.Steps[4].Name != "open-pilot.verify-command" || detail.Steps[4].Status != controlplane.StepSucceeded || detail.Steps[5].Name != "open-pilot.commit-changes" || detail.Steps[6].Name != "open-pilot.push-branch" || detail.Steps[7].Name != "open-pilot.pr-plan" {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	if detail.Steps[4].CorrelationIDs["policy_mode"] != "dry-run" {
		t.Fatalf("verify step = %+v", detail.Steps[4])
	}
	if detail.Steps[4].Inputs["diff_check"] != "git diff --check" || !strings.Contains(detail.Steps[4].OutputSummary, "git diff --check passed") {
		t.Fatalf("expected central diff check evidence: %+v", detail.Steps[4])
	}
	if detail.Steps[1].Inputs["branch"] == "" || detail.Steps[4].Inputs["command"] != "make verify" || detail.Steps[6].Inputs["mode"] == "" || detail.Steps[7].Inputs["head"] == "" {
		t.Fatalf("dynamic inputs not persisted: %+v", detail.Steps)
	}
}

func TestRunnerDoesNotPersistOpenPilotPushedPhaseWhenPushIsDryRunOrDisabled(t *testing.T) {
	for _, pushMode := range []string{"dry-run", "disabled"} {
		pushMode := pushMode
		t.Run(pushMode, func(t *testing.T) {
			state := runOpenPilotNoPersistTest(t, pushMode, "disabled")
			if state.Phase == controlplane.OpenPilotPhasePushed {
				t.Fatalf("state = %+v", state)
			}
			if state.Phase != controlplane.OpenPilotPhaseVerified {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func TestRunnerDoesNotPersistOpenPilotPRCreatedPhaseWhenPRIsDryRunOrDisabled(t *testing.T) {
	for _, writeMode := range []string{"dry-run", "disabled"} {
		writeMode := writeMode
		t.Run(writeMode, func(t *testing.T) {
			state := runOpenPilotNoPersistTest(t, "disabled", writeMode)
			if state.Phase == controlplane.OpenPilotPhasePRCreated {
				t.Fatalf("state = %+v", state)
			}
			if state.Phase != controlplane.OpenPilotPhaseVerified {
				t.Fatalf("state = %+v", state)
			}
		})
	}
}

func runOpenPilotNoPersistTest(t *testing.T, pushMode, writeMode string) controlplane.OpenPilotIssueState {
	t.Helper()
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "status,diff")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", pushMode)
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", writeMode)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v", run)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	return state
}

func TestRunnerRehydratesOpenPilotResumeStateForVerifyStep(t *testing.T) {
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "diff")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{
		"repo":                     "OWNER/REPO",
		"issue":                    "123",
		"test_command":             "make verify",
		"retry_from_step_position": "5",
		"retry_from_step":          "open-pilot.verify-command",
		"resume.open-pilot.validate-issue.summary":         "Fetched issue: Fix switchyard (open)",
		"resume.open-pilot.branch-plan.repo":               "OWNER/REPO",
		"resume.open-pilot.branch-plan.base_branch":        "main",
		"resume.open-pilot.branch-plan.branch":             "open-pilot/issue-123-fix-switchyard",
		"resume.open-pilot.branch-plan.mode":               "dry-run",
		"resume.open-pilot.branch-plan.dry_run":            "true",
		"resume.open-pilot.agent-work.summary":             "implementation summary",
		"resume.open-pilot.agent-work.correlation.request": "ocq-1",
	}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 4 {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	if detail.Steps[0].Name != "open-pilot.verify-command" || detail.Steps[1].Name != "open-pilot.commit-changes" || detail.Steps[2].Name != "open-pilot.push-branch" || detail.Steps[3].Name != "open-pilot.pr-plan" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if detail.Steps[2].Inputs["branch"] != "open-pilot/issue-123-fix-switchyard" || detail.Steps[3].Inputs["head"] != "open-pilot/issue-123-fix-switchyard" {
		t.Fatalf("rehydrated inputs missing: %+v", detail.Steps)
	}
}

func TestRunnerReusesStoredOpenPilotPromptFromResumeInputs(t *testing.T) {
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "diff")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	const prompt = "stored prompt with acceptance instructions"
	_, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{
		"repo":                     "OWNER/REPO",
		"issue":                    "123",
		"test_command":             "make verify",
		"retry_from_step_position": "5",
		"retry_from_step":          "open-pilot.verify-command",
		"resume.open-pilot.validate-issue.summary":  "Fetched issue: Fix switchyard (open)",
		"resume.open-pilot.prompt":                  prompt,
		"resume.open-pilot.branch-plan.repo":        "OWNER/REPO",
		"resume.open-pilot.branch-plan.base_branch": "main",
		"resume.open-pilot.branch-plan.branch":      "open-pilot/issue-123-fix-switchyard",
		"resume.open-pilot.branch-plan.mode":        "dry-run",
		"resume.open-pilot.branch-plan.dry_run":     "true",
		"resume.open-pilot.agent-work.summary":      "implementation summary",
	}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if got := state.ResumeInputs["resume.open-pilot.prompt"]; got != prompt {
		t.Fatalf("resume prompt = %q, want %q", got, prompt)
	}
}

func TestRunnerPersistsOpenPilotStateThroughAgentWork(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "disabled")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "disabled")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || state.Phase != controlplane.OpenPilotPhaseAgentCompleted {
		t.Fatalf("run = %+v state = %+v", run, state)
	}
	if state.Branch == "" || state.ResumeInputs["resume.open-pilot.branch-plan.branch"] == "" || state.ResumeInputs["resume.open-pilot.prompt"] == "" || state.ResumeInputs["resume.open-pilot.agent-work.response"] != "implementation summary" {
		t.Fatalf("state resume inputs missing: %+v", state)
	}
}

func TestRunnerPersistsOpenPilotPullRequestFields(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	runGitForTest(t, repoPath, "push", "test-origin", "main:refs/heads/open-pilot/issue-123-fix-switchyard")
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/issues/123" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":99,"number":7,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/7"}`))
		default:
			t.Fatalf("unexpected PR request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "disabled")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || state.Phase != controlplane.OpenPilotPhasePRCreated {
		t.Fatalf("run = %+v state = %+v", run, state)
	}
	if state.PullRequestURL != "https://git/OWNER/REPO/pulls/7" || state.PullRequestID != 99 || state.PullRequestNum != 7 {
		t.Fatalf("PR fields not persisted: %+v", state)
	}
	if state.ResumeInputs["resume.open-pilot.pr-plan.pull_request_url"] != "https://git/OWNER/REPO/pulls/7" {
		t.Fatalf("PR resume inputs not persisted: %+v", state.ResumeInputs)
	}
}

func TestRunnerPersistsOpenPilotPullRequestAfterVerifyWhenPushSkipped(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	runGitForTest(t, repoPath, "push", "test-origin", "main:refs/heads/open-pilot/issue-123-fix-switchyard")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/issues/123" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":99,"number":7,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/7"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "disabled")
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "diff")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || state.Phase != controlplane.OpenPilotPhasePRCreated || state.PullRequestURL != "https://git/OWNER/REPO/pulls/7" {
		t.Fatalf("run = %+v state = %+v", run, state)
	}
}

func TestRunnerPersistsOpenPilotFailureState(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "deny")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if run.Status != controlplane.RunFailed || state.Phase != controlplane.OpenPilotPhaseFailed || state.LastStepName != "open-pilot.verify-command" || !strings.Contains(state.LastError, "deny") || state.FailingGate != "local_verify" || state.RecommendedNextAction != "rerun_verify" || state.OperatorSummary == "" {
		t.Fatalf("run = %+v state = %+v", run, state)
	}
	if state.Branch == "" || state.Base == "" || state.ResumeInputs["resume.open-pilot.prompt"] == "" {
		t.Fatalf("expected branch/base/prompt to persist: %+v", state)
	}
}

func TestRunnerFailsOpenPilotWhenConfiguredGiteaFetchFails(t *testing.T) {
	t.Setenv("GITEA_BASE_URL", "http://127.0.0.1:1")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if run.Status != controlplane.RunFailed || state.Phase != controlplane.OpenPilotPhaseFailed || state.LastStepName != "open-pilot.validate-issue" || state.FailingGate != "issue_validation" {
		t.Fatalf("run = %+v state = %+v", run, state)
	}
}

func TestRunnerPrefersOpenPilotDBStateForHydration(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", controlplane.OpenPilotPhaseRequested); err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{
		Repo:              "OWNER/REPO",
		Issue:             "123",
		Phase:             controlplane.OpenPilotPhaseFailed,
		RunID:             "",
		LastStepName:      "open-pilot.verify-command",
		Branch:            "db-branch",
		Base:              "main",
		ResumeInputs:      map[string]string{"resume.open-pilot.validate-issue.summary": "db summary", "resume.open-pilot.prompt": "db prompt", "resume.open-pilot.agent-work.response": "db response", "resume.open-pilot.branch-plan.branch": "db-branch", "resume.open-pilot.branch-plan.base_branch": "main", "resume.open-pilot.push-branch.branch": "db-branch", "resume.open-pilot.push-branch.remote": "origin", "resume.open-pilot.push-branch.mode": "disabled"},
		GateStatus:        controlplane.OpenPilotGateStatus{"local_verify": "passed"},
		MaxRepairAttempts: 2,
	}); err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "dry-run")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "make,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "diff")
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{
		"repo":                                  "OWNER/REPO",
		"issue":                                 "123",
		"test_command":                          "make verify",
		"retry_from_step_position":              "5",
		"retry_from_step":                       "open-pilot.verify-command",
		"resume.open-pilot.branch-plan.branch":  "conflict-branch",
		"resume.open-pilot.prompt":              "conflict prompt",
		"resume.open-pilot.agent-work.response": "conflict response",
		"resume.open-pilot.push-branch.branch":  "conflict-branch",
	}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 4 || detail.Steps[2].Inputs["branch"] != "db-branch" || detail.Steps[3].Inputs["head"] != "db-branch" {
		t.Fatalf("hydrated steps = %+v", detail.Steps)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.ResumeInputs["resume.open-pilot.prompt"] != "db prompt" || state.ResumeInputs["resume.open-pilot.agent-work.response"] != "db response" {
		t.Fatalf("DB resume inputs were not preferred: %+v", state.ResumeInputs)
	}
}

func TestRunnerReusesKnownOpenPilotPullRequestOnResume(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	if _, err := store.TransitionOpenPilotIssueState("OWNER/REPO", "123", controlplane.OpenPilotPhaseRequested); err != nil {
		t.Fatalf("TransitionOpenPilotIssueState() error = %v", err)
	}
	if _, err := store.UpsertOpenPilotIssueState(controlplane.OpenPilotIssueState{
		Repo:              "OWNER/REPO",
		Issue:             "123",
		Phase:             controlplane.OpenPilotPhaseFailed,
		RunID:             "",
		LastStepName:      "open-pilot.pr-plan",
		Branch:            "open-pilot/issue-123",
		Base:              "main",
		PullRequestURL:    "https://git/OWNER/REPO/pulls/7",
		PullRequestID:     99,
		PullRequestNum:    7,
		ResumeInputs:      map[string]string{"resume.open-pilot.validate-issue.summary": "db summary", "resume.open-pilot.agent-work.response": "db response", "resume.open-pilot.branch-plan.branch": "open-pilot/issue-123", "resume.open-pilot.branch-plan.base_branch": "main"},
		MaxRepairAttempts: 2,
	}); err != nil {
		t.Fatalf("UpsertOpenPilotIssueState() error = %v", err)
	}
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls" && r.Method == http.MethodPost {
			t.Fatalf("CreatePullRequest should not be called when DB state already records PR")
		}
		_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")

	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "retry_from_step_position": "8", "retry_from_step": "open-pilot.pr-plan"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 1 || detail.Steps[0].OutputSummary != "Using existing pull request" || detail.Steps[0].CorrelationIDs["pull_request"] != "https://git/OWNER/REPO/pulls/7" {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.Phase != controlplane.OpenPilotPhasePRCreated || state.PullRequestURL != "https://git/OWNER/REPO/pulls/7" {
		t.Fatalf("state = %+v", state)
	}
}

func TestRunnerCreatesOpenPilotWorktreeAndRunsVerifyThere(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "allow")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "status,diff")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "git status"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 8 {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	worktreePath := detail.Steps[1].CorrelationIDs["worktree_path"]
	if worktreePath == "" || !strings.Contains(detail.Steps[1].OutputSummary, "Created worktree") {
		t.Fatalf("branch step = %+v", detail.Steps[1])
	}
	if _, err := os.Stat(filepath.Join(worktreePath, ".git")); err != nil {
		t.Fatalf("worktree .git missing: %v", err)
	}
	if detail.Steps[4].CorrelationIDs["worktree_path"] != worktreePath || detail.Steps[4].Status != controlplane.StepSucceeded {
		t.Fatalf("verify step = %+v", detail.Steps[4])
	}
	foundStatus := false
	for _, artifact := range detail.Artifacts {
		if artifact.Name == "open-pilot-git-status.txt" && strings.Contains(artifact.URI, "clean") {
			foundStatus = true
		}
	}
	if !foundStatus {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerSkipsOpenPilotWorktreeCleanupWithoutDurableHandoff(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_WORKTREE_CLEANUP", "on-success")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 7 {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	worktreePath := detail.Steps[1].CorrelationIDs["worktree_path"]
	if worktreePath == "" {
		t.Fatalf("branch step = %+v", detail.Steps[1])
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree should be kept for unpublished branch, stat err = %v", err)
	}
	cleanupArtifact := false
	for _, artifact := range detail.Artifacts {
		if artifact.Name == "open-pilot-worktree-cleanup.json" && strings.Contains(artifact.URI, "branch+not+pushed+or+published") {
			cleanupArtifact = true
		}
	}
	if !cleanupArtifact || detail.Steps[len(detail.Steps)-1].CorrelationIDs["worktree_cleanup"] != "skipped" || detail.Steps[len(detail.Steps)-1].CorrelationIDs["worktree_cleanup_reason"] != "branch not pushed or published" {
		t.Fatalf("cleanup artifact/step missing: artifacts=%+v step=%+v", detail.Artifacts, detail.Steps[len(detail.Steps)-1])
	}
}

func TestRunnerRejectsOpenPilotPullRequestForUnpushedWorktreeBranch(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	remotePath := addLocalBareTestRemote(t, repoPath)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	last := detail.Steps[len(detail.Steps)-1]
	if run.Status != controlplane.RunFailed || last.Name != "open-pilot.pr-plan" || last.Status != controlplane.StepFailed || !strings.Contains(last.Error, "requires pushed worktree branch") {
		t.Fatalf("run = %+v last = %+v", run, last)
	}
}

func TestRunnerCreatesOpenPilotPullRequestForExistingRemoteHead(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	runGitForTest(t, repoPath, "push", "test-origin", "main:refs/heads/open-pilot/issue-123")
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/issues/123" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"","body":"Acceptance criteria","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
		case r.URL.Path == "/api/v1/repos/OWNER/REPO/pulls" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"id":99,"number":7,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/7"}`))
		default:
			t.Fatalf("unexpected PR request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	pushStep := detail.Steps[len(detail.Steps)-2]
	prStep := detail.Steps[len(detail.Steps)-1]
	if pushStep.Name != "open-pilot.push-branch" || pushStep.Inputs["remote_head_exists"] != "true" || pushStep.CorrelationIDs["remote_head_exists"] != "true" {
		t.Fatalf("push step = %+v", pushStep)
	}
	if prStep.Name != "open-pilot.pr-plan" || prStep.OutputSummary != "Created pull request" || prStep.CorrelationIDs["pull_request"] == "" {
		t.Fatalf("pr step = %+v", prStep)
	}
}

func TestRunnerRejectsOpenPilotPushWithoutCommittedChanges(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	remotePath := addLocalBareTestRemote(t, repoPath)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	last := detail.Steps[len(detail.Steps)-1]
	if run.Status != controlplane.RunFailed || last.Name != "open-pilot.push-branch" || last.Status != controlplane.StepFailed || last.CorrelationIDs["commits_ahead"] != "0" {
		t.Fatalf("run = %+v last = %+v", run, last)
	}
	foundAhead := false
	for _, artifact := range detail.Artifacts {
		if artifact.Name == "open-pilot-git-ahead.txt" && strings.Contains(artifact.URI, "0%0A") {
			foundAhead = true
		}
	}
	if !foundAhead {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerAutoCommitsVerifiedOpenPilotWorktreeChanges(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode ocq: %v", err)
		}
		workdir, _ := body["directory"].(string)
		if workdir == "" {
			t.Fatal("missing OCQ directory")
		}
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		if err := os.WriteFile(filepath.Join(workdir, "generated.txt"), []byte("generated\n"), 0o644); err != nil {
			t.Fatalf("write generated: %v", err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"created generated.txt"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "allow")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "status,diff")
	t.Setenv("OPEN_PILOT_AUTO_COMMIT_MODE", "allow")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "allow")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "git status"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	commitStep := detail.Steps[5]
	pushStep := detail.Steps[6]
	if commitStep.Name != "open-pilot.commit-changes" || commitStep.Status != controlplane.StepSucceeded || commitStep.Inputs["executed"] != "true" || commitStep.CorrelationIDs["commit_sha"] == "" {
		t.Fatalf("commit step = %+v", commitStep)
	}
	if pushStep.Name != "open-pilot.push-branch" || pushStep.Status != controlplane.StepSucceeded || pushStep.CorrelationIDs["commits_ahead"] != "1" {
		t.Fatalf("push step = %+v", pushStep)
	}
	state, err := store.OpenPilotIssueState("OWNER/REPO", "123")
	if err != nil {
		t.Fatalf("OpenPilotIssueState() error = %v", err)
	}
	if state.ResumeInputs["resume.open-pilot.commit-changes.sha"] == "" || state.ResumeInputs["resume.open-pilot.commit-changes.executed"] != "true" || state.GateStatus["local_verify"] != "passed" {
		t.Fatalf("state = %+v", state)
	}
}

func TestRunnerRejectsOpenPilotPushWithDirtyWorktree(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	remotePath := addLocalBareTestRemote(t, repoPath)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "allow")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "touch,git")
	t.Setenv("OPEN_PILOT_ALLOWED_GIT_VERBS", "diff")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "touch dirty.txt"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	last := detail.Steps[len(detail.Steps)-1]
	if run.Status != controlplane.RunFailed || last.Name != "open-pilot.push-branch" || last.Status != controlplane.StepFailed || !strings.Contains(last.Error, "requires a clean worktree") {
		t.Fatalf("run = %+v last = %+v", run, last)
	}
	foundDirtyStatus := false
	for _, artifact := range detail.Artifacts {
		if artifact.Name == "open-pilot-git-status.txt" && strings.Contains(artifact.URI, "dirty.txt") {
			foundDirtyStatus = true
		}
	}
	if !foundDirtyStatus {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerCreatesOpenPilotPullRequestWhenWriteAllowed(t *testing.T) {
	t.Setenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE", "true")
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	runGitForTest(t, repoPath, "push", "test-origin", "main:refs/heads/open-pilot/issue-123-fix-switchyard")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runGitForTest(t, repoPath, "remote", "set-url", "origin", remotePath)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	gitea := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/OWNER/REPO/issues/123":
			_, _ = w.Write([]byte(`{"id":10,"number":123,"title":"Fix switchyard","body":"Acceptance criteria\n\nTest: make verify","state":"open","labels":[{"name":"ready-for-agent"},{"name":"agent:queued"}]}`))
		case "/api/v1/repos/OWNER/REPO/pulls":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s", r.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode pr: %v", err)
			}
			if body["base"] != "main" || body["head"] != "open-pilot/issue-123-fix-switchyard" || body["draft"] != true {
				t.Fatalf("pr body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":99,"number":7,"title":"Open Pilot: issue 123","state":"open","html_url":"https://git/OWNER/REPO/pulls/7"}`))
		default:
			t.Fatalf("path = %s", r.URL.Path)
		}
	}))
	t.Cleanup(gitea.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("GITEA_BASE_URL", gitea.URL)
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "disabled")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC", "true")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || len(detail.Steps) != 7 || detail.Steps[6].OutputSummary != "Created pull request" || detail.Steps[6].CorrelationIDs["pull_request"] == "" {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
}

func TestRunnerFailsOpenPilotDeniedVerifyCommand(t *testing.T) {
	repoPath := initTestGitRepo(t)
	worktreeRoot := filepath.Join(t.TempDir(), "worktrees")
	ocq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"implementation summary"}}]}`))
	}))
	t.Cleanup(ocq.Close)
	t.Setenv("OCQ_BASE_URL", ocq.URL)
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_REPO_PATH", repoPath)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", worktreeRoot)
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "deny")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "open-pilot", Inputs: map[string]string{"repo": "OWNER/REPO", "issue": "123", "test_command": "make verify"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunFailed || !strings.Contains(run.Summary, "deny") || len(detail.Steps) != 5 || detail.Steps[4].Status != controlplane.StepFailed {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
}

func TestRunnerGeneratesDevlogArtifact(t *testing.T) {
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	seed, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "open-pilot"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.FinishRun(seed.ID, controlplane.RunSucceeded, "seed run complete"); err != nil {
		t.Fatalf("FinishRun() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunTrigger(t.Context(), "manual-devlog")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || run.Summary != "Generated Devlog markdown artifact" {
		t.Fatalf("run = %+v", run)
	}
	if len(detail.Steps) != 3 {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	wantNames := []string{"devlog.collect-history", "devlog.generate-markdown", "devlog.attach-artifact"}
	for i, want := range wantNames {
		if detail.Steps[i].Name != want || detail.Steps[i].Status != controlplane.StepSucceeded {
			t.Fatalf("step[%d] = %+v", i, detail.Steps[i])
		}
	}
	if detail.Steps[1].Executor != "model" || detail.Steps[1].OutputSummary != "Used deterministic Markdown fallback" {
		t.Fatalf("generate step = %+v", detail.Steps[1])
	}
	if len(detail.Artifacts) != 1 || detail.Artifacts[0].Kind != "markdown" || detail.Artifacts[0].Name != "devlog.md" {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for worktree test")
	}
	dir := t.TempDir()
	runGitForTest(t, dir, "init")
	runGitForTest(t, dir, "config", "user.email", "switchyard@example.test")
	runGitForTest(t, dir, "config", "user.name", "Switchyard Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitForTest(t, dir, "add", "README.md")
	runGitForTest(t, dir, "commit", "-m", "initial")
	runGitForTest(t, dir, "branch", "-M", "main")
	runGitForTest(t, dir, "remote", "add", "origin", "https://git.example.test/OWNER/REPO.git")
	return dir
}

func runGitForTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
}

func addLocalBareTestRemote(t *testing.T, repoPath string) string {
	t.Helper()
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	runGitForTest(t, t.TempDir(), "init", "--bare", remotePath)
	runGitForTest(t, repoPath, "remote", "add", "test-origin", remotePath)
	runGitForTest(t, repoPath, "push", "test-origin", "main")
	return remotePath
}

func TestRunnerGeneratesDevlogWithModel(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if body.Model != "devlog-test-model" {
			t.Fatalf("model = %s", body.Model)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# Generated by model"}}]}`))
	}))
	t.Cleanup(modelServer.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", modelServer.URL)
	t.Setenv("DEVLOG_MODEL", "devlog-test-model")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunTrigger(t.Context(), "manual-devlog")
	if err != nil {
		t.Fatalf("RunTrigger() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 3 || detail.Steps[1].Model != "devlog-test-model" || detail.Steps[1].OutputSummary != "Generated Markdown with model" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if len(detail.Artifacts) != 1 || !strings.Contains(detail.Artifacts[0].URI, "Generated+by+model") {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}

func TestRunnerAllowsDevlogModelOverride(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode model request: %v", err)
		}
		if body.Model != "override-model" {
			t.Fatalf("model = %s", body.Model)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# Override"}}]}`))
	}))
	t.Cleanup(modelServer.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", modelServer.URL)
	t.Setenv("DEVLOG_MODEL", "default-model")
	t.Setenv("DEVLOG_ALLOWED_MODELS", "default-model,override-model")

	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "devlog", Inputs: map[string]string{"model": "override-model"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if len(detail.Steps) != 3 || detail.Steps[1].Model != "override-model" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
}

func TestRunnerExecutesNativeChangemakerTemplate(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"# Digest"}}]}`))
	}))
	t.Cleanup(modelServer.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", modelServer.URL)
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store)
	run, err := runner.RunOnce(t.Context(), controlplane.CreateRunInput{WorkflowID: "changemaker-daily-digest", Inputs: map[string]string{"topic": "daily", "prompt": "Write a digest"}})
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if run.Status != controlplane.RunSucceeded || run.Summary != "Changemaker draft generated by model step" {
		t.Fatalf("run = %+v", run)
	}
	if len(detail.Steps) != 3 || detail.Steps[0].Name != "changemaker.prepare" || detail.Steps[1].Name != "changemaker.generate" || detail.Steps[2].Name != "changemaker.attach-draft" {
		t.Fatalf("steps = %+v", detail.Steps)
	}
	if len(detail.Artifacts) != 3 {
		t.Fatalf("artifacts = %+v", detail.Artifacts)
	}
}
