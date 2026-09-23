package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewDiffFixture(t *testing.T, content string) (OpenPilotWorktreePlan, GiteaPullRequest) {
	t.Helper()
	_, _, plan := preparedCleanupWorktree(t)
	base := strings.TrimSpace(worktreeTestGitOutput(t, plan.WorktreePath, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(plan.WorktreePath, "PROOF.md"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runWorktreeTestGit(t, plan.WorktreePath, "add", "PROOF.md")
	runWorktreeTestGit(t, plan.WorktreePath, "commit", "-m", "actual edit")
	head := strings.TrimSpace(worktreeTestGitOutput(t, plan.WorktreePath, "rev-parse", "HEAD"))
	return plan, GiteaPullRequest{Head: GiteaPullRequestRef{SHA: head}, Base: GiteaPullRequestRef{SHA: base}}
}
func TestOpenPilotReviewUsesActualCommittedPatchDespiteAgentSummary(t *testing.T) {
	plan, pr := reviewDiffFixture(t, "history\n\nmisplaced fixture\n\nlast historical entry\n")
	patch, err := openPilotCommittedReviewDiff(t.Context(), plan, pr)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"open-pilot.ai-review.diff": patch, "open-pilot.agent-summary": "I appended the fixture at the end"}
	prompt := buildOpenPilotAIReviewPrompt("owner/repo", "1", nil, state)
	for _, want := range []string{pr.Head.SHA, pr.Base.SHA, "+misplaced fixture", "+last historical entry"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("review omitted %q: %s", want, prompt)
		}
	}
	artifact := buildOpenPilotAIReviewArtifact("owner/repo", "1", nil, state, "changes_requested", "wrong placement", "CHANGES_REQUESTED: wrong placement")
	if !strings.Contains(artifact, patch) {
		t.Fatal("review artifact omitted committed evidence")
	}
}
func TestOpenPilotReviewRejectsMovedHeadAndDirtyWorktree(t *testing.T) {
	plan, pr := reviewDiffFixture(t, "fixture\n")
	moved := pr
	moved.Head.SHA = pr.Base.SHA
	if _, err := openPilotCommittedReviewDiff(t.Context(), plan, moved); err == nil || !strings.Contains(err.Error(), "HEAD") {
		t.Fatalf("moved head error=%v", err)
	}
	if err := os.WriteFile(filepath.Join(plan.WorktreePath, "PROOF.md"), []byte("unverified change"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openPilotCommittedReviewDiff(t.Context(), plan, pr); err == nil || !strings.Contains(err.Error(), "changed after commit") {
		t.Fatalf("dirty error=%v", err)
	}
}
func TestOpenPilotReviewRejectsIncompleteOrOpaquePatch(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"oversized", strings.Repeat("large line\n", 10000), "64 KiB"},
		{"binary", "binary\x00data", "binary changes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, pr := reviewDiffFixture(t, tc.content)
			if _, err := openPilotCommittedReviewDiff(t.Context(), plan, pr); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOpenPilotAIReviewStepSendsAndRetainsCommittedPatch(t *testing.T) {
	plan, pr := reviewDiffFixture(t, "actual committed content\n")
	pr.ID = 1
	pr.Number = 1
	pr.State = "open"
	pr.HTMLURL = "https://git/owner/repo/pulls/1"
	pr.Head.Ref = plan.Branch
	pr.Base.Ref = plan.BaseBranch
	pr.Head.Repo = pullRequestRepoIdentity(plan.Repo)
	pr.Base.Repo = pullRequestRepoIdentity(plan.Repo)
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": plan.Repo, "issue": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	seedOpenPilotStateAtPhase(t, store, OpenPilotIssueState{Repo: plan.Repo, Issue: "1", RunID: run.ID, Branch: plan.Branch, Base: plan.BaseBranch, WorktreePath: plan.WorktreePath, ResumeInputs: map[string]string{"resume.open-pilot.commit-changes.sha": pr.Head.SHA}}, OpenPilotPhasePRCreated)
	env := newOpenPilotAIReviewTestEnv(store)
	env.giteaReader = &scriptedCIGiteaReader{findPR: pr, findFound: true}
	model := &captureModelExecutor{response: ModelResponse{Content: "APPROVED: patch matches"}}
	env.modelExecutor = model
	state := map[string]any{"open-pilot.branch-plan": plan, "open-pilot.pull-request": pr}
	out, err := (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "ai-review", Name: "open-pilot.ai-review"}, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].Messages[1].Content, "+actual committed content") {
		t.Fatalf("review request=%+v", model.requests)
	}
	found := false
	for _, a := range out.Artifacts {
		if a.Name == "open-pilot-ai-review.md" && strings.Contains(a.URI, "actual+committed+content") {
			found = true
		}
	}
	if !found {
		t.Fatalf("review patch artifact missing: %+v", out.Artifacts)
	}
}
