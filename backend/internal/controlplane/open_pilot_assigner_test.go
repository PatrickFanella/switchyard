package controlplane

import (
	"context"
	"testing"
	"time"
)

type fakeOpenPilotIssueReader struct {
	issue   GiteaIssue
	lists   int
	fetches int
}

func (f *fakeOpenPilotIssueReader) ListIssues(context.Context, string, []string) ([]GiteaIssue, error) {
	f.lists++
	return []GiteaIssue{f.issue}, nil
}
func (f *fakeOpenPilotIssueReader) FetchIssue(context.Context, string, string) (GiteaIssue, error) {
	f.fetches++
	return f.issue, nil
}

func TestOpenPilotAssignerVerifiesAndClaimsIssueOnce(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	reader := &fakeOpenPilotIssueReader{issue: GiteaIssue{ID: 10, Number: 7, State: "open", Body: "## Test Command\n```bash\nnpm run data:verify\n```", Labels: []string{"ready-for-agent", "agent:queued"}}}
	assigner := &OpenPilotAssigner{store: store, runner: runner, issues: reader, repos: []string{"owner/repo"}, labels: []string{"ready-for-agent", "agent:queued"}, now: func() time.Time { return time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC) }}
	assigned, err := assigner.Poll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if assigned != 1 {
		t.Fatalf("assigned=%d", assigned)
	}
	assigned, err = assigner.Poll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if assigned != 0 {
		t.Fatalf("second assigned=%d", assigned)
	}
	runs := store.ListRuns()
	if len(runs) != 1 {
		t.Fatalf("runs=%d", len(runs))
	}
	if runs[0].Inputs["test_command"] != "npm run data:verify" || runs[0].Inputs["issue_id"] != "10" || runs[0].Inputs["issue_verified_at"] == "" {
		t.Fatalf("inputs=%v", runs[0].Inputs)
	}
}

func TestOpenPilotAssignerRejectsMissingRequiredLabel(t *testing.T) {
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	reader := &fakeOpenPilotIssueReader{issue: GiteaIssue{ID: 10, Number: 7, State: "open", Labels: []string{"ready-for-agent"}}}
	assigner := &OpenPilotAssigner{store: store, runner: NewRunner(store), issues: reader, repos: []string{"owner/repo"}, labels: []string{"ready-for-agent", "agent:queued"}, now: time.Now}
	assigned, err := assigner.Poll(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if assigned != 0 || len(store.ListRuns()) != 0 {
		t.Fatalf("assigned=%d runs=%d", assigned, len(store.ListRuns()))
	}
}

func TestOpenPilotAssignerResolvesGateBeforeGitea(t *testing.T) {
	store := NewStore(nil)
	runner := NewRunner(store)
	runner.SetOpenPilotAdmissionGate(testOpenPilotAdmissionGate(t))
	reader := &fakeOpenPilotIssueReader{}
	assigner := &OpenPilotAssigner{store: store, runner: runner, issues: reader, repos: []string{"other/repo"}, labels: []string{"ready-for-agent"}, now: time.Now}
	if assigned, err := assigner.Poll(t.Context()); err == nil || assigned != 0 {
		t.Fatalf("assigned=%d error=%v", assigned, err)
	}
	if reader.lists != 0 || reader.fetches != 0 {
		t.Fatalf("Gitea calls lists=%d fetches=%d", reader.lists, reader.fetches)
	}
}
