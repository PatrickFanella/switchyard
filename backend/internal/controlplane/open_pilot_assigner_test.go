package controlplane

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

type fakeOpenPilotIssueReader struct {
	issue         GiteaIssue
	lists         int
	fetches       int
	assignedLists int
	listedIssue   *GiteaIssue
}

func (f *fakeOpenPilotIssueReader) ListIssues(context.Context, string, []string) ([]GiteaIssue, error) {
	f.lists++
	if f.listedIssue != nil {
		return []GiteaIssue{*f.listedIssue}, nil
	}
	return []GiteaIssue{f.issue}, nil
}
func (f *fakeOpenPilotIssueReader) ListAssignedIssues(ctx context.Context, repo, assignee string) ([]GiteaIssue, error) {
	f.assignedLists++
	if assignee != "open-pilot" {
		return nil, nil
	}
	if f.listedIssue != nil {
		return []GiteaIssue{*f.listedIssue}, nil
	}
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

func TestOpenPilotAssignerAssignmentEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, configured, current, state string
		labels                           []string
		want                             int
	}{
		{name: "assigned without labels", configured: "open-pilot", current: "OPEN-PILOT", state: "open", want: 1},
		{name: "different user", configured: "open-pilot", current: "someone-else", state: "open"},
		{name: "assignment removed after listing", configured: "open-pilot", state: "open"},
		{name: "closed assigned issue", configured: "open-pilot", current: "open-pilot", state: "closed"},
		{name: "assignment disabled", current: "open-pilot", state: "open"},
		{name: "label path still works", configured: "open-pilot", state: "open", labels: []string{"ready-for-agent", "agent:queued"}, want: 1},
		{name: "both paths claimed once", configured: "open-pilot", current: "open-pilot", state: "open", labels: []string{"ready-for-agent", "agent:queued"}, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			listed := GiteaIssue{ID: 10, Number: 7, State: "open", Assignees: []GiteaIssueAssignee{{Login: "open-pilot"}}}
			reader := &fakeOpenPilotIssueReader{listedIssue: &listed, issue: GiteaIssue{ID: 10, Number: 7, State: tc.state, Labels: tc.labels, Assignees: []GiteaIssueAssignee{{Login: tc.current}}}}
			assigner := &OpenPilotAssigner{store: store, runner: NewRunner(store), issues: reader, repos: []string{"owner/repo"}, labels: []string{"ready-for-agent", "agent:queued"}, assignee: tc.configured, now: time.Now}
			got, err := assigner.Poll(t.Context())
			if err != nil || got != tc.want || len(store.ListRuns()) != tc.want {
				t.Fatalf("assigned=%d runs=%d err=%v", got, len(store.ListRuns()), err)
			}
			if reader.fetches != 1 {
				t.Fatalf("duplicate candidate fetched %d times", reader.fetches)
			}
			if tc.configured == "" && reader.assignedLists != 0 {
				t.Fatal("assignment query while disabled")
			}
			got, err = assigner.Poll(t.Context())
			if err != nil || got != 0 {
				t.Fatalf("second poll assigned=%d err=%v", got, err)
			}
		})
	}
}

func TestOpenPilotExecutionRechecksAssignmentAndSpec(t *testing.T) {
	env := &EnvWorkflowExecutors{openPilotLabels: []string{"ready-for-agent", "agent:queued"}, openPilotAssignee: "open-pilot", requireIssueSpec: true}
	issue := GiteaIssue{ID: 10, Number: 7, State: "open", Body: "Acceptance criteria and test instructions", Assignees: []GiteaIssueAssignee{{Login: "open-pilot"}}}
	if err := env.validateOpenPilotIssue("owner/repo", "7", issue); err != nil {
		t.Fatal(err)
	}
	issue.Assignees = nil
	if err := env.validateOpenPilotIssue("owner/repo", "7", issue); err == nil {
		t.Fatal("removed assignment accepted")
	}
	issue.Assignees = []GiteaIssueAssignee{{Login: "open-pilot"}}
	issue.Body = ""
	if err := env.validateOpenPilotIssue("owner/repo", "7", issue); err == nil {
		t.Fatal("assignment bypassed issue spec")
	}
	issue.Body = "Acceptance criteria and test instructions"
	if err := env.validateOpenPilotIssue("owner/repo", "8", issue); err == nil {
		t.Fatal("assignment bypassed issue identity")
	}
	issue.State = "closed"
	if err := env.validateOpenPilotIssue("owner/repo", "7", issue); err == nil {
		t.Fatal("assignment bypassed issue state")
	}
}

type assignmentIssueFetcher struct {
	issue GiteaIssue
	calls int
	err   error
}

func (f *assignmentIssueFetcher) FetchIssue(context.Context, string, string) (GiteaIssue, error) {
	return f.issue, nil
}
func (f *assignmentIssueFetcher) AssignIssue(context.Context, string, GiteaIssue, string) error {
	f.calls++
	return f.err
}

func TestOpenPilotSelfAssignmentHonorsWriteModeAndSurfacesFailure(t *testing.T) {
	for _, mode := range []string{"disabled", "dry-run", "allow"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("assignment forbidden")
			writer := &assignmentIssueFetcher{err: failure}
			env := &EnvWorkflowExecutors{issueFetcher: writer, openPilotAssignee: "open-pilot", openPilotWriteMode: mode}
			err := env.assignOpenPilotIssue(t.Context(), "owner/repo", GiteaIssue{Number: 7})
			if mode == "allow" {
				if !errors.Is(err, failure) || writer.calls != 1 {
					t.Fatalf("err=%v calls=%d", err, writer.calls)
				}
			} else if err != nil || writer.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, writer.calls)
			}
			writer.calls = 0
			err = env.assignOpenPilotIssue(t.Context(), "owner/repo", GiteaIssue{Number: 7, Assignees: []GiteaIssueAssignee{{Login: "open-pilot"}}})
			if err != nil || writer.calls != 0 {
				t.Fatalf("reassigned owned issue: err=%v calls=%d", err, writer.calls)
			}
		})
	}
}

func TestOpenPilotValidationAssignsOnlyAfterEligibilityAndPersistsWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		labels    []string
		writeErr  error
		wantCalls int
		wantPhase OpenPilotPhase
	}{
		{"eligible", []string{"ready-for-agent", "agent:queued"}, nil, 1, OpenPilotPhaseValidated},
		{"not queued", []string{"ready-for-agent"}, nil, 0, OpenPilotPhaseFailed},
		{"assignment fails", []string{"ready-for-agent", "agent:queued"}, errors.New("assignment forbidden"), 1, OpenPilotPhaseFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(nil)
			writer := &assignmentIssueFetcher{issue: GiteaIssue{ID: 10, Number: 7, State: "open", Labels: tc.labels, Body: "Acceptance criteria and test instructions"}, err: tc.writeErr}
			env := &EnvWorkflowExecutors{store: store, issueFetcher: writer, openPilotLabels: []string{"ready-for-agent", "agent:queued"}, openPilotAssignee: "open-pilot", openPilotWriteMode: "allow", requireIssueSpec: true}
			run := Run{ID: "run-test", TriggerID: "gitea-open-pilot-poll", Inputs: map[string]string{"repo": "owner/repo", "issue": "7", "issue_id": "10"}}
			_, err := (openPilotExecutor{env: env}).ExecutePipelineStep(t.Context(), run, PipelineStep{ID: "validate-issue", Name: "validate-issue"}, map[string]any{})
			if (err != nil) != (tc.wantPhase == OpenPilotPhaseFailed) || writer.calls != tc.wantCalls {
				t.Fatalf("err=%v assignment calls=%d", err, writer.calls)
			}
			state, stateErr := store.OpenPilotIssueState("owner/repo", "7")
			if stateErr != nil || state.Phase != tc.wantPhase {
				t.Fatalf("state=%+v err=%v", state, stateErr)
			}
			if tc.writeErr != nil && state.LastError != "assignment forbidden" {
				t.Fatalf("lost assignment failure: %+v", state)
			}
		})
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

func TestOpenPilotSelfAssignmentRejectsStaleAttempt(t *testing.T) {
	store := NewStore(nil)
	writer := &assignmentIssueFetcher{}
	env := &EnvWorkflowExecutors{store: store, issueFetcher: writer, openPilotAssignee: "open-pilot", openPilotWriteMode: "allow"}
	ctx := withRunAttemptFence(t.Context(), RunAttemptFence{RunID: "expired", WorkerID: "old-worker", Attempt: 1})
	ctx = context.WithValue(ctx, runAttemptStoreContextKey{}, store.WithContext(ctx))
	err := env.assignOpenPilotIssue(ctx, "owner/repo", GiteaIssue{Number: 7})
	if !errors.Is(err, ErrStaleRunAttempt) || writer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, writer.calls)
	}
}

func TestOpenPilotAssignerPollsOnlyExactProofTargets(t *testing.T) {
	for _, number := range []int{7, 99} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			store := NewStore(nil)
			if err := SeedDefaults(store); err != nil {
				t.Fatal(err)
			}
			manifest, _ := testMultiRepositoryRuntimeManifest(t)
			targets := testOpenPilotProofTargets(manifest)
			targets[0].Issue = "7"
			expires := time.Now().Add(time.Hour)
			_, err := store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: manifest.RegistryFingerprint, ProofTargets: targets, ExpiresAt: &expires})
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewOpenPilotRepositoryRegistry(manifest)
			if err != nil {
				t.Fatal(err)
			}
			gate := &OpenPilotAdmissionGate{registry: registry, mode: "durable", rollout: store}
			runner := NewRunner(store)
			runner.SetOpenPilotAdmissionGate(gate)
			reader := &fakeOpenPilotIssueReader{issue: GiteaIssue{ID: 10, Number: number, State: "open", Labels: []string{"ready-for-agent", "agent:queued"}}}
			assigner := &OpenPilotAssigner{store: store, runner: runner, issues: reader, repos: []string{targets[0].RepositorySlug}, labels: []string{"ready-for-agent", "agent:queued"}, now: time.Now}
			got, err := assigner.Poll(t.Context())
			want := 0
			if number == 7 {
				want = 1
			}
			if err != nil || got != want || reader.fetches != want {
				t.Fatalf("got=%d fetches=%d err=%v", got, reader.fetches, err)
			}
		})
	}
}
