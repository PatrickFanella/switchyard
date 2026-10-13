package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type overseerClientFake struct {
	issues       map[string]OpenPilotQueueIssue
	dependencies map[string][]OpenPilotQueueIssue
	prs          map[string]GiteaPullRequest
	writes       []string
	err          error
	lostWrite    bool
	onWrite      func()
}

func queueFixture(repo string, number int, labels ...string) OpenPilotQueueIssue {
	return OpenPilotQueueIssue{Repo: repo, ID: number + 100, Number: number, Title: "Implement feature", Body: "Acceptance criteria\nVerification: make verify", State: "open", Labels: labels, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)}
}

func (c *overseerClientFake) ListOpenPilotRepositories(context.Context) ([]string, error) {
	if c.err != nil {
		return nil, c.err
	}
	return []string{"owner/repo", "other/repo"}, nil
}
func (c *overseerClientFake) ListQueueIssues(_ context.Context, label string, assigned bool) ([]OpenPilotQueueIssue, error) {
	if c.err != nil {
		return nil, c.err
	}
	out := []OpenPilotQueueIssue{}
	for _, issue := range c.issues {
		if issue.State != "open" {
			continue
		}
		if label == "__pull_requests__" {
			if issue.IsPR {
				out = append(out, issue)
			}
		} else if assigned {
			if openPilotIssueAssignedTo(GiteaIssue{Assignees: issue.Assignees}, "open-pilot") {
				out = append(out, issue)
			}
		} else if hasAllLabels(issue.Labels, []string{label}) && !issue.IsPR {
			out = append(out, issue)
		}
	}
	return out, nil
}
func (c *overseerClientFake) FetchQueueIssue(_ context.Context, repo string, n int) (OpenPilotQueueIssue, error) {
	if issue, ok := c.issues[overseerKey(repo, fmt.Sprint(n))]; ok {
		return issue, nil
	}
	return OpenPilotQueueIssue{}, ErrNotFound
}
func (c *overseerClientFake) FetchIssue(ctx context.Context, repo, number string) (GiteaIssue, error) {
	n := 0
	_, _ = fmt.Sscanf(number, "%d", &n)
	issue, err := c.FetchQueueIssue(ctx, repo, n)
	return GiteaIssue{ID: issue.ID, Number: issue.Number, Repository: repo, State: issue.State, Labels: issue.Labels, Assignees: issue.Assignees}, err
}
func (c *overseerClientFake) GetPullRequest(_ context.Context, repo string, n int) (GiteaPullRequest, error) {
	if pr, ok := c.prs[overseerKey(repo, fmt.Sprint(n))]; ok {
		return pr, nil
	}
	return GiteaPullRequest{}, ErrNotFound
}
func (c *overseerClientFake) QueueDependencies(_ context.Context, repo string, n int) ([]OpenPilotQueueIssue, error) {
	return c.dependencies[overseerKey(repo, fmt.Sprint(n))], nil
}
func (c *overseerClientFake) QueueAdmissionBlockers(context.Context, string) ([]string, error) {
	return nil, nil
}
func (c *overseerClientFake) AddQueueLabel(_ context.Context, repo string, n int) error {
	if c.onWrite != nil {
		c.onWrite()
	}
	key := overseerKey(repo, fmt.Sprint(n))
	c.writes = append(c.writes, key)
	if c.lostWrite {
		return errors.New("lost response")
	}
	issue := c.issues[key]
	issue.Labels = append(issue.Labels, "agent:queued")
	c.issues[key] = issue
	return nil
}

func overseerFixture(t *testing.T, issues ...OpenPilotQueueIssue) (*OpenPilotOverseer, *MemoryStore, *overseerClientFake, *time.Time) {
	t.Helper()
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("OPEN_PILOT_ASSIGNEE", "open-pilot")
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	store := NewStore(func() time.Time { return now })
	runner := NewRunner(store)
	client := &overseerClientFake{issues: map[string]OpenPilotQueueIssue{}, dependencies: map[string][]OpenPilotQueueIssue{}, prs: map[string]GiteaPullRequest{}}
	for _, issue := range issues {
		client.issues[overseerKey(issue.Repo, fmt.Sprint(issue.Number))] = issue
	}
	o := NewOpenPilotOverseer(store, runner, client)
	o.now = func() time.Time { return now }
	state, _ := o.State(context.Background())
	state.Config.Enabled = true
	if err := o.persistence.save(context.Background(), &state); err != nil {
		t.Fatal(err)
	}
	return o, store, client, &now
}

func TestOverseerQueuesOneIssueInPriorityMilestoneOrder(t *testing.T) {
	a := queueFixture("owner/repo", 1, "ready-for-agent", "priority: P1")
	b := queueFixture("owner/repo", 2, "ready-for-agent", "priority: P0")
	b.Milestone = "M2 - Later"
	c := queueFixture("other/repo", 3, "ready-for-agent", "priority: P0")
	c.Milestone = "M1 - Foundation"
	o, _, client, _ := overseerFixture(t, a, b, c)
	client.onWrite = func() {
		state, err := o.State(context.Background())
		if err != nil || state.Pending == nil {
			t.Error("write was not preceded by durable reservation")
		}
	}
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 1 || client.writes[0] != "other/repo#3" {
		t.Fatalf("writes = %v", client.writes)
	}
	state, _ := o.State(context.Background())
	if state.Pending != nil || len(state.History) != 1 || !state.Report.Complete {
		t.Fatalf("state = %+v", state)
	}
}

func TestOverseerDependenciesAndHumanBoundariesHoldCandidates(t *testing.T) {
	a := queueFixture("owner/repo", 1, "ready-for-agent", "priority: P0")
	a.Body += "\n## Blocked by\n- #8"
	b := queueFixture("other/repo", 2, "ready-for-agent")
	b.Body += "\nDo not auto-merge."
	dependency := queueFixture("owner/repo", 8)
	o, _, client, _ := overseerFixture(t, a, b, dependency)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("blocked work was queued")
	}
	state, _ := o.State(context.Background())
	if len(state.Report.Candidates) != 2 {
		t.Fatalf("candidates=%v", state.Report.Candidates)
	}
	for _, candidate := range state.Report.Candidates {
		if len(candidate.Blockers) == 0 {
			t.Fatal("missing hold reason")
		}
	}
}

func TestOverseerNativeDependenciesAndClosedPrerequisites(t *testing.T) {
	candidate := queueFixture("owner/repo", 1, "ready-for-agent")
	dependency := queueFixture("owner/repo", 8)
	o, _, client, now := overseerFixture(t, candidate, dependency)
	client.dependencies["owner/repo#1"] = []OpenPilotQueueIssue{dependency}
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("open native dependency ignored")
	}
	dependency.State = "closed"
	client.dependencies["owner/repo#1"] = []OpenPilotQueueIssue{dependency}
	*now = now.Add(15 * time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 1 {
		t.Fatal("closed prerequisite did not release issue")
	}
}

func TestOverseerCapDeduplicatesLabelsAssignmentAndOpenPR(t *testing.T) {
	queued := queueFixture("owner/repo", 1, "ready-for-agent", "agent:queued")
	queued.Assignees = []GiteaIssueAssignee{{Login: "open-pilot"}}
	pr := queueFixture("owner/repo", 5)
	pr.IsPR = true
	pr.Author = "open-pilot"
	o, _, client, _ := overseerFixture(t, queued, pr, queueFixture("other/repo", 2, "ready-for-agent"))
	client.prs["owner/repo#5"] = GiteaPullRequest{Number: 5, Head: GiteaPullRequestRef{Ref: "open-pilot/issue-1-fix"}}
	state, _ := o.State(context.Background())
	state.Config.MaxOpenLanes = 1
	_ = o.persistence.save(context.Background(), &state)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	state, _ = o.State(context.Background())
	if len(state.Report.Lanes) != 1 || len(client.writes) != 0 {
		t.Fatalf("lanes=%v writes=%v", state.Report.Lanes, client.writes)
	}
}

func TestOverseerDoesNotReviveCanceledClaims(t *testing.T) {
	o, store, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	store.openPilotIssueStates[openPilotIssueStateKey("owner/repo", "1")] = OpenPilotIssueState{Repo: "owner/repo", Issue: "1", Phase: OpenPilotPhaseRequested, RunID: "canceled"}
	store.runs["canceled"] = Run{ID: "canceled", Status: RunCanceled, WorkflowID: string(WorkflowOpenPilot)}
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("canceled issue revived")
	}
}

func TestOverseerWaitsForDurablePickupBeforeNextAdmission(t *testing.T) {
	o, _, client, now := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"), queueFixture("other/repo", 2, "ready-for-agent"))
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(15 * time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 1 {
		t.Fatalf("second issue admitted before pickup: %v", client.writes)
	}
}

func TestOverseerPreviewAndDisabledGateNeverWrite(t *testing.T) {
	o, _, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	if err := o.Tick(context.Background(), true, true); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("preview wrote")
	}
	state, _ := o.State(context.Background())
	state.Config.Enabled = false
	_ = o.persistence.save(context.Background(), &state)
	if err := o.Tick(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("disabled controller wrote")
	}
}

func TestOverseerHonorsExistingGiteaWritePolicy(t *testing.T) {
	o, _, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	o.runner.executors.(*EnvWorkflowExecutors).openPilotWriteMode = "disabled"
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	if len(client.writes) != 0 {
		t.Fatal("write policy bypassed")
	}
}

func TestOverseerPartialScanAndLostWriteRetainEvidence(t *testing.T) {
	o, _, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	client.err = errors.New("upstream unavailable")
	if o.Tick(context.Background(), true, false) == nil {
		t.Fatal("expected partial scan")
	}
	if len(client.writes) != 0 {
		t.Fatal("partial scan wrote")
	}
	client.err = nil
	client.lostWrite = true
	if o.Tick(context.Background(), true, false) == nil {
		t.Fatal("expected uncertain write")
	}
	state, _ := o.State(context.Background())
	if state.Pending == nil {
		t.Fatal("uncertain outcome reservation lost")
	}
	client.lostWrite = false
	if o.Tick(context.Background(), true, false) == nil {
		t.Fatal("uncertain outcome was blindly retried")
	}
	if len(client.writes) != 1 {
		t.Fatal("uncertain write duplicated")
	}
}

func TestOverseerConfigurationPersistsAcrossControllerReplacementAndConflicts(t *testing.T) {
	o, store, client, _ := overseerFixture(t)
	state, _ := o.State(context.Background())
	config := state.Config
	config.MaxOpenLanes = 5
	updated, err := o.Configure(context.Background(), state.Revision, config, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.Configure(context.Background(), state.Revision, config, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale config accepted")
	}
	replacement := NewOpenPilotOverseer(store, o.runner, client)
	read, _ := replacement.State(context.Background())
	if read.Revision != updated.Revision || read.Config.MaxOpenLanes != 5 {
		t.Fatal("configuration lost")
	}
}

func TestOverseerListenerAndAuthorizationBoundary(t *testing.T) {
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "reader")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	defer api.Shutdown(context.Background())
	request := func(handler http.Handler, method, path, token, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if code := request(api.ControlPlaneHandler(), "GET", "/api/open-pilot/overseer", "reader", ""); code != 200 {
		t.Fatal(code)
	}
	body := `{"expected_revision":0,"config":{"enabled":false,"queue_enabled":true,"recovery_enabled":true,"max_open_lanes":3,"interval_seconds":900}}`
	if code := request(api.ControlPlaneHandler(), "PUT", "/api/open-pilot/overseer", "reader", body); code != 403 {
		t.Fatal(code)
	}
	if code := request(api.ControlPlaneHandler(), "PUT", "/api/open-pilot/overseer", "admin", body); code != 200 {
		t.Fatal(code)
	}
	if code := request(api.ControlPlaneHandler(), "PUT", "/api/open-pilot/overseer", "admin", body); code != 409 {
		t.Fatal(code)
	}
	if code := request(api.ModelIngressHandler(), "GET", "/api/open-pilot/overseer", "admin", ""); code != 404 {
		t.Fatal(code)
	}
}

func TestOverseerRecoveryRequiresConsecutiveCompleteObservations(t *testing.T) {
	o, store, client, now := overseerFixture(t, queueFixture("owner/repo", 1))
	state, pr, _ := externalMergeFixture(t, store, "owner/repo")
	issue := client.issues["owner/repo#1"]
	issue.State = "closed"
	client.issues["owner/repo#1"] = issue
	client.prs["owner/repo#7"] = pr
	o.runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	after, _ := store.OpenPilotIssueState(state.Repo, state.Issue)
	if after.Phase == OpenPilotPhaseIssueClosed {
		t.Fatal("first observation recovered work")
	}
	*now = now.Add(15 * time.Minute)
	client.err = errors.New("upstream unavailable")
	if err := o.Tick(context.Background(), true, false); err == nil {
		t.Fatal("partial observation accepted")
	}
	client.err = nil
	*now = now.Add(15 * time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	after, _ = store.OpenPilotIssueState(state.Repo, state.Issue)
	if after.Phase == OpenPilotPhaseIssueClosed {
		t.Fatal("recovered across failed observation")
	}
	*now = now.Add(15 * time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	after, _ = store.OpenPilotIssueState(state.Repo, state.Issue)
	if after.Phase != OpenPilotPhaseIssueClosed || after.GateStatus["ai_review"] != "changes_requested" {
		t.Fatalf("reconciliation lost history: %+v", after)
	}
	receipt, _ := o.State(context.Background())
	if len(receipt.History) != 1 || receipt.Pending != nil || receipt.Report.Action.Kind != "reconcile" {
		t.Fatalf("receipt=%+v", receipt)
	}
}

func assertOverseerFencedRequeue(t *testing.T, store Store, repo string) {
	t.Helper()
	state, prior := sessionRecoveryState(t, store, repo)
	state.Phase = OpenPilotPhaseFailed
	state.MaxRepairAttempts = 2
	state, err := store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(prior.ID, RunFailed, "temporary network failure"); err != nil {
		t.Fatal(err)
	}
	// Initial fixture has not consumed an attempt; preserve that count, never reset it.
	detail, _ := store.RunDetail(prior.ID)
	requeuer := store.(interface {
		RequeueOpenPilotOverseerRun(context.Context, OpenPilotIssueState, int, time.Time) (Run, error)
	})
	stale := state
	stale.UpdatedAt = stale.UpdatedAt.Add(time.Second)
	if _, err := requeuer.RequeueOpenPilotOverseerRun(context.Background(), stale, detail.Run.AttemptCount, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal("stale state accepted", err)
	}
	if _, err := requeuer.RequeueOpenPilotOverseerRun(context.Background(), state, detail.Run.AttemptCount+1, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal("stale attempt accepted", err)
	}
	queued, err := requeuer.RequeueOpenPilotOverseerRun(context.Background(), state, detail.Run.AttemptCount, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != RunQueued || queued.AttemptCount != detail.Run.AttemptCount || queued.MaxAttempts != detail.Run.MaxAttempts || queued.ID != prior.ID {
		t.Fatalf("budgets/identity changed: %+v", queued)
	}
	if _, err := requeuer.RequeueOpenPilotOverseerRun(context.Background(), state, detail.Run.AttemptCount, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate recovery accepted", err)
	}
	if _, err := store.FinishRun(prior.ID, RunCanceled, "fixture complete"); err != nil {
		t.Fatal(err)
	}
}

func TestOverseerFencedRequeue(t *testing.T) {
	assertOverseerFencedRequeue(t, NewStore(nil), "owner/requeue")
}

func TestOverseerRecoveryBudgetFollowsGenerationAndLineage(t *testing.T) {
	now := time.Now().UTC()
	state := defaultOverseerState()
	action := OpenPilotOverseerAction{Kind: "requeue", Repo: "owner/repo", Issue: 1, RepositoryID: "repo-1", ExecutorGeneration: "gen-1", RunID: "child"}
	for i := 0; i < 2; i++ {
		old := action
		old.RunID = fmt.Sprint(i)
		old.CreatedAt = now.Add(-2 * time.Hour)
		state.History = append(state.History, old)
	}
	if overseerBudget(&state, action, now) {
		t.Fatal("child run obtained a fresh recovery budget")
	}
	action.ExecutorGeneration = "gen-2"
	if !overseerBudget(&state, action, now) {
		t.Fatal("new executor generation was not reassessed")
	}
	for i := 0; i < 4; i++ {
		state.History = append(state.History, OpenPilotOverseerAction{Kind: "queue_label", CreatedAt: now.Add(-time.Minute)})
	}
	if overseerBudget(&state, action, now) {
		t.Fatal("fleet hourly limit ignored")
	}
}

func TestOverseerIntentReleaseRetainsBudgetAndRequiresReviewedRevision(t *testing.T) {
	o, _, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	client.lostWrite = true
	if err := o.Tick(context.Background(), true, false); err == nil {
		t.Fatal("expected uncertain outcome")
	}
	state, _ := o.State(context.Background())
	if state.Pending == nil {
		t.Fatal("lost reservation")
	}
	if _, err := o.ReleaseReviewedPending(context.Background(), state.Revision-1, state.Pending.ID, "provider reviewed", "admin"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale review accepted", err)
	}
	if _, err := o.ReleaseReviewedPending(context.Background(), state.Revision, state.Pending.ID, "", "admin"); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty review accepted", err)
	}
	released, err := o.ReleaseReviewedPending(context.Background(), state.Revision, state.Pending.ID, "provider confirmed no label was applied", "admin")
	if err != nil || released.Pending != nil || len(released.History) != 1 {
		t.Fatal("review did not preserve consumed action", err)
	}
}

func TestOverseerPauseRecheckedBeforeReservation(t *testing.T) {
	o, _, client, _ := overseerFixture(t, queueFixture("owner/repo", 1, "ready-for-agent"))
	checks := 0
	o.guard = func(context.Context) error {
		checks++
		if checks >= 3 {
			return errors.New("operator paused worker")
		}
		return nil
	}
	if err := o.Tick(context.Background(), true, false); err == nil {
		t.Fatal("new operator pause ignored")
	}
	state, _ := o.State(context.Background())
	if len(client.writes) != 0 || state.Pending != nil {
		t.Fatal("acted after pause")
	}
}

type blockingOverseerClient struct {
	*overseerClientFake
	entered chan struct{}
}

func (c *blockingOverseerClient) ListOpenPilotRepositories(ctx context.Context) ([]string, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestSchedulerOverseerDoesNotBlockDispatchAndStopsWithScheduler(t *testing.T) {
	o, store, client, _ := overseerFixture(t)
	blocked := &blockingOverseerClient{overseerClientFake: client, entered: make(chan struct{}, 1)}
	o.client = blocked
	scheduler := NewScheduler(store, o.runner, time.Hour, nil)
	scheduler.SetOpenPilotOverseer(o)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler.Start(ctx)
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("native overseer was not started")
	}
	returned := make(chan error, 1)
	go func() { returned <- scheduler.Tick(ctx) }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("overseer blocked ordinary scheduler dispatch")
	}
	cancel()
	waitCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := scheduler.Wait(waitCtx); err != nil {
		t.Fatal("scheduler did not drain overseer", err)
	}
}

func TestOverseerRepeatRecoveryRequiresOperatorAcrossChildRuns(t *testing.T) {
	state := defaultOverseerState()
	now := time.Now().UTC()
	state.History = []OpenPilotOverseerAction{{Kind: "requeue", Repo: "owner/repo", Issue: 1, RepositoryID: "repo-1", ExecutorGeneration: "gen-1", RunID: "parent", CreatedAt: now.Add(-time.Hour)}}
	action := OpenPilotOverseerAction{Kind: "requeue", Repo: "owner/repo", Issue: 1, RepositoryID: "repo-1", ExecutorGeneration: "gen-1", RunID: "child"}
	if !overseerRepeatedRecovery(&state, action, now) {
		t.Fatal("child run bypassed repeated-failure escalation")
	}
	action.ExecutorGeneration = "gen-2"
	if overseerRepeatedRecovery(&state, action, now) {
		t.Fatal("new generation inherited stale incident")
	}
}

func TestOverseerTransientRecoveryPreservesAttemptBudgetAndEscalatesRepeat(t *testing.T) {
	o, store, _, now := overseerFixture(t, queueFixture("owner/repo", 1))
	state, prior := sessionRecoveryState(t, store, "owner/repo")
	state.Phase = OpenPilotPhaseFailed
	state.MaxRepairAttempts = 2
	var err error
	state, err = store.UpsertOpenPilotIssueState(state)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	run := store.runs[prior.ID]
	run.Status = RunFailed
	run.AttemptCount = 1
	run.MaxAttempts = 3
	run.Summary = "temporary network failure"
	store.runs[prior.ID] = run
	store.mu.Unlock()
	o.runner.SetOpenPilotAdmissionGate(openPilotAdmissionGateForTarget(t, *trustedOpenPilotInput(state.Repo, state.Issue, CreateRunInput{}).OpenPilotTarget))
	for _, advance := range []time.Duration{0, 14 * time.Minute} {
		*now = now.Add(advance)
		if err := o.Tick(context.Background(), true, false); err != nil {
			t.Fatal(err)
		}
		detail, _ := store.RunDetail(prior.ID)
		if detail.Run.Status != RunFailed {
			t.Fatal("recovered before 15 minutes")
		}
	}
	*now = now.Add(time.Minute)
	if err := o.Tick(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	detail, _ := store.RunDetail(prior.ID)
	if detail.Run.Status != RunQueued || detail.Run.AttemptCount != 1 || detail.Run.MaxAttempts != 3 {
		t.Fatalf("recovery=%+v", detail.Run)
	}
	store.mu.Lock()
	run = store.runs[prior.ID]
	run.Status = RunFailed
	run.AttemptCount = 2
	run.Summary = "temporary network failure"
	store.runs[prior.ID] = run
	store.mu.Unlock()
	for i := 0; i < 2; i++ {
		*now = now.Add(15 * time.Minute)
		if err := o.Tick(context.Background(), true, false); err != nil {
			t.Fatal(err)
		}
	}
	detail, _ = store.RunDetail(prior.ID)
	receipt, _ := o.State(context.Background())
	if detail.Run.Status != RunFailed || len(receipt.History) != 1 || receipt.Report.Error != "A prior recovery failed again; operator review is required" {
		t.Fatalf("repeat not escalated: %+v", receipt.Report)
	}
}

func TestOverseerDependencyReferencesAreNotMistakenForMarkdownHeadings(t *testing.T) {
	for _, body := range []string{"\n## Dependencies\n#8", "\nDepends on #8", "\n- Prerequisites: #8"} {
		t.Run(body, func(t *testing.T) {
			candidate := queueFixture("owner/repo", 1, "ready-for-agent")
			candidate.Body += body
			o, _, client, _ := overseerFixture(t, candidate, queueFixture("owner/repo", 8))
			if err := o.Tick(context.Background(), true, false); err != nil {
				t.Fatal(err)
			}
			if len(client.writes) != 0 {
				t.Fatal("open dependency was ignored")
			}
		})
	}
}
