package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type inboxIssueReader struct {
	mu            sync.Mutex
	listed        map[string][]GiteaIssue
	assigned      map[string][]GiteaIssue
	errs          map[string]error
	lists         int
	assignedLists int
}

func (r *inboxIssueReader) ListIssues(_ context.Context, repo string, _ []string) ([]GiteaIssue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lists++
	if err := r.errs[repo]; err != nil {
		return nil, err
	}
	return append([]GiteaIssue(nil), r.listed[repo]...), nil
}

func (r *inboxIssueReader) ListAssignedIssues(_ context.Context, repo, _ string) ([]GiteaIssue, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.assignedLists++
	if err := r.errs[repo]; err != nil {
		return nil, err
	}
	return append([]GiteaIssue(nil), r.assigned[repo]...), nil
}

func (r *inboxIssueReader) FetchIssue(context.Context, string, string) (GiteaIssue, error) {
	return GiteaIssue{}, errors.New("inbox must not fetch individual issues")
}

type inboxFixture struct {
	store    *MemoryStore
	api      *API
	reader   *inboxIssueReader
	manifest OpenPilotRuntimeManifest
}

type discoveringInboxReader struct {
	*inboxIssueReader
	visible []string
	err     error
}

func (r *discoveringInboxReader) ListOpenPilotRepositories(context.Context) ([]string, error) {
	return r.visible, r.err
}

func TestOpenPilotWildcardInboxRetainsMissingRepositoryHistoryWithoutPollingIt(t *testing.T) {
	fixture := newInboxFixture(t)
	fixture.api.openPilotPollRepos = []string{"*"}
	_, run := fixture.dispatch(t, "owner/beta", "9")
	if run.ID == "" {
		t.Fatal("historical claim was not created")
	}
	fixture.reader.errs["owner/beta"] = errors.New("deleted repository must not be queried")
	reader := &discoveringInboxReader{inboxIssueReader: fixture.reader, visible: []string{"owner/alpha"}}
	fixture.api.SetOpenPilotIssueReader(reader)
	_, _, report := fixture.inbox(t, "")
	if report.Upstream != OpenPilotInboxUpstreamAvailable {
		t.Fatalf("upstream = %s, error = %s", report.Upstream, report.UpstreamError)
	}
	for _, repository := range report.Repositories {
		if repository.ID == "beta" && (repository.Polled || repository.Claims != 1 || repository.Upstream != OpenPilotInboxRepositoryNotQueried) {
			t.Fatalf("retained repository = %+v", repository)
		}
	}
	if _, ok := inboxIssue(report, "owner/beta#9"); !ok {
		t.Fatal("missing repository history was hidden")
	}
	reader.err = errors.New("discovery unavailable")
	_, _, report = fixture.inbox(t, "")
	if report.Upstream != OpenPilotInboxUpstreamUnavailable {
		t.Fatalf("failed discovery = %+v", report)
	}
	if _, ok := inboxIssue(report, "owner/beta#9"); !ok {
		t.Fatal("failed discovery hid durable history")
	}
}

// newInboxFixture builds an isolated-test two-repository runtime (owner/alpha
// with alias legacy/alpha, owner/beta with alias legacy/beta) whose admission
// gate admits registered repositories without the durable rollout gate.
func newInboxFixture(t *testing.T) inboxFixture {
	t.Helper()
	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "isolated-test")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "")
	t.Setenv("OPEN_PILOT_ASSIGNEE", "open-pilot")
	t.Setenv("OPEN_PILOT_REQUIRED_LABELS", "ready-for-agent,agent:queued")
	t.Setenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC", "true")
	t.Setenv("OPEN_PILOT_POLL_REPOS", "legacy/alpha")
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "allow")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "go,make")
	t.Setenv("GITEA_TOKEN", "inbox-fixture-token-must-not-appear")
	store := NewStore(nil)
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	if runner.openPilotGateError != nil {
		t.Fatal(runner.openPilotGateError)
	}
	api := NewAPI(store, runner)
	api.SetAsyncDispatch(true)
	reader := &inboxIssueReader{listed: map[string][]GiteaIssue{}, assigned: map[string][]GiteaIssue{}, errs: map[string]error{}}
	api.SetOpenPilotIssueReader(reader)
	return inboxFixture{store: store, api: api, reader: reader, manifest: manifest}
}

func (f inboxFixture) inbox(t *testing.T, query string) (int, string, OpenPilotInboxReport) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/inbox"+query, nil)
	response := httptest.NewRecorder()
	f.api.routes().ServeHTTP(response, request)
	var report OpenPilotInboxReport
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode inbox: %v: %s", err, response.Body.String())
		}
	}
	return response.Code, response.Body.String(), report
}

func (f inboxFixture) dispatch(t *testing.T, repo, issue string) (int, Run) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/workflows/open-pilot/run", strings.NewReader(`{"repo":"`+repo+`","issue":"`+issue+`"}`))
	response := httptest.NewRecorder()
	f.api.routes().ServeHTTP(response, request)
	var payload struct {
		Run Run `json:"run"`
	}
	if response.Code == http.StatusAccepted || response.Code == http.StatusCreated {
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
	}
	return response.Code, payload.Run
}

func inboxIssue(report OpenPilotInboxReport, key string) (OpenPilotInboxIssue, bool) {
	for _, issue := range report.Issues {
		if issue.Key == key {
			return issue, true
		}
	}
	return OpenPilotInboxIssue{}, false
}

func inboxCheck(issue OpenPilotInboxIssue, name string) OpenPilotReadinessCheck {
	for _, check := range issue.Checks {
		if check.Name == name {
			return check
		}
	}
	return OpenPilotReadinessCheck{}
}

func assertInboxRedacted(t *testing.T, body string, manifest OpenPilotRuntimeManifest) {
	t.Helper()
	for _, repository := range manifest.Repositories {
		for _, forbidden := range []string{repository.PasswordFile, repository.SourcePath, repository.WorktreeRoot, strings.Repeat("a", 40), strings.Repeat("b", 40)} {
			if forbidden != "" && strings.Contains(body, forbidden) {
				t.Fatalf("inbox leaked %q: %s", forbidden, body)
			}
		}
	}
	if strings.Contains(body, "inbox-fixture-token-must-not-appear") || strings.Contains(body, "hunter2") {
		t.Fatalf("inbox leaked a secret: %s", body)
	}
}

const inboxSpecBody = "## Acceptance Criteria\n- done\n\n## Test Command\n```bash\ngo test ./...\n```\n"

func TestOpenPilotInboxClassifiesIntakeCandidates(t *testing.T) {
	fixture := newInboxFixture(t)
	bot := []GiteaIssueAssignee{{Login: "Open-Pilot"}}
	both := []string{"ready-for-agent", "agent:queued"}
	fixture.reader.listed["owner/alpha"] = []GiteaIssue{
		{ID: 101, Number: 1, State: "open", Title: "Labeled and specified", Body: inboxSpecBody, Labels: both, HTMLURL: "https://gitea.test/owner/alpha/issues/1"},
		{ID: 103, Number: 3, State: "open", Title: "Awaiting queue label", Body: inboxSpecBody, Labels: []string{"ready-for-agent"}},
		{ID: 104, Number: 4, State: "open", Title: "Denied command", Body: "## Acceptance Criteria\n- x\n\n## Test Command\n```bash\nrm -rf /\n```\n", Labels: both},
		{ID: 105, Number: 5, State: "closed", Title: "Closed but labeled", Body: inboxSpecBody, Labels: both},
		{ID: 106, Number: 6, State: "open", Title: "No specification", Body: "please fix", Labels: both},
		{ID: 107, Number: 7, State: "open", Title: "Active run", Body: inboxSpecBody, Labels: both},
		{ID: 108, Number: 8, State: "open", Title: "Failed lifecycle", Body: inboxSpecBody, Labels: both},
	}
	fixture.reader.assigned["owner/alpha"] = []GiteaIssue{
		{ID: 101, Number: 1, State: "open", Title: "Labeled and specified", Body: inboxSpecBody, Labels: both, Assignees: bot},
		{ID: 102, Number: 2, State: "open", Title: "Assigned only", Body: inboxSpecBody, Assignees: bot},
	}

	// Claims created through the existing admission and claim path.
	code, activeRun := fixture.dispatch(t, "legacy/alpha", "7")
	if code != http.StatusAccepted || activeRun.ID == "" {
		t.Fatalf("dispatch #7 code=%d run=%+v", code, activeRun)
	}
	if code, _ := fixture.dispatch(t, "owner/alpha", "07"); code != http.StatusConflict {
		t.Fatalf("duplicate dispatch code=%d, want 409", code)
	}
	_, failedRun := fixture.dispatch(t, "owner/alpha", "8")
	if _, err := fixture.store.FinishRun(failedRun.ID, RunFailed, "agent crashed"); err != nil {
		t.Fatal(err)
	}
	failedState, err := fixture.store.OpenPilotIssueState("owner/alpha", "8")
	if err != nil {
		t.Fatal(err)
	}
	failedState.Phase, failedState.LastError, failedState.LastStepName = OpenPilotPhaseFailed, "agent crashed", "open-pilot.agent-work"
	failedState.PullRequestURL, failedState.PullRequestNum, failedState.PullRequestID = "https://gitea.test/owner/alpha/pulls/80", 80, 800
	if _, err := fixture.store.UpsertOpenPilotIssueState(failedState); err != nil {
		t.Fatal(err)
	}
	// A durable claim in beta that Gitea no longer lists.
	_, mergedRun := fixture.dispatch(t, "owner/beta", "9")
	if _, err := fixture.store.FinishRun(mergedRun.ID, RunSucceeded, "merged"); err != nil {
		t.Fatal(err)
	}

	code, body, report := fixture.inbox(t, "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	assertInboxRedacted(t, body, fixture.manifest)
	if report.ActivationMode != "isolated-test" || report.Upstream != OpenPilotInboxUpstreamAvailable || len(report.Blockers) != 0 || report.HasMore {
		t.Fatalf("report = %+v", report)
	}
	if fixture.reader.lists != 2 || fixture.reader.assignedLists != 2 {
		t.Fatalf("gitea lists=%d assigned=%d", fixture.reader.lists, fixture.reader.assignedLists)
	}
	if !report.Policy.RequireIssueSpec || report.Policy.Assignee != "open-pilot" || len(report.Policy.RequiredLabels) != 2 || len(report.Policy.PollRepositories) != 1 {
		t.Fatalf("policy = %+v", report.Policy)
	}
	repos := map[string]OpenPilotInboxRepository{}
	for _, repository := range report.Repositories {
		repos[repository.ID] = repository
	}
	if alpha := repos["alpha"]; !alpha.Admitted || !alpha.Polled || alpha.Upstream != OpenPilotInboxRepositoryListed || alpha.Listed != 8 || alpha.Claims != 2 {
		t.Fatalf("alpha = %+v", alpha)
	}
	if beta := repos["beta"]; !beta.Admitted || beta.Polled || beta.Upstream != OpenPilotInboxRepositoryListed || beta.Listed != 0 || beta.Claims != 1 {
		t.Fatalf("beta = %+v", beta)
	}
	if len(report.Issues) != 9 || report.Matched != 9 {
		keys := []string{}
		for _, issue := range report.Issues {
			keys = append(keys, issue.Key)
		}
		t.Fatalf("issues = %v", keys)
	}
	for index := 1; index < len(report.Issues); index++ {
		left, right := report.Issues[index-1], report.Issues[index]
		leftNumber, _ := strconv.Atoi(left.Issue)
		rightNumber, _ := strconv.Atoi(right.Issue)
		if left.Repo > right.Repo || (left.Repo == right.Repo && leftNumber >= rightNumber) {
			t.Fatalf("issues are not ordered: %s before %s", left.Key, right.Key)
		}
	}

	labeled, _ := inboxIssue(report, "owner/alpha#1")
	if !labeled.Eligible || labeled.Action != OpenPilotInboxActionDispatch || labeled.Pickup != "both" || !labeled.BotAssigned || !labeled.LabelsSatisfied || !labeled.SpecReady || labeled.TestCommand != "go test ./..." || labeled.Links.Issue == "" || labeled.Claim != nil {
		t.Fatalf("labeled = %+v", labeled)
	}
	if check := inboxCheck(labeled, "test_command"); check.Code != "command_allowed" || check.Status != OpenPilotReadinessPass {
		t.Fatalf("labeled command = %+v", check)
	}
	if check := inboxCheck(labeled, "issue_state"); check.Code != "issue_state_new" {
		t.Fatalf("labeled state = %+v", check)
	}
	assigned, _ := inboxIssue(report, "owner/alpha#2")
	if !assigned.Eligible || assigned.Pickup != "assigned" || assigned.LabelsSatisfied || len(assigned.MissingLabels) != 2 || assigned.Assignees[0] != "Open-Pilot" {
		t.Fatalf("assigned = %+v", assigned)
	}
	labelOnly, _ := inboxIssue(report, "owner/alpha#3")
	if labelOnly.Eligible || labelOnly.Pickup != "none" || len(labelOnly.ReasonCodes) != 1 || labelOnly.ReasonCodes[0] != OpenPilotReasonLabelsMissing || len(labelOnly.MissingLabels) != 1 || labelOnly.MissingLabels[0] != "agent:queued" || labelOnly.Action != OpenPilotInboxActionNone {
		t.Fatalf("label-only = %+v", labelOnly)
	}
	if check := inboxCheck(labelOnly, "pickup"); !strings.Contains(check.Message, "agent:queued") || !strings.Contains(check.Remediation, "open-pilot") {
		t.Fatalf("label-only pickup = %+v", check)
	}
	denied, _ := inboxIssue(report, "owner/alpha#4")
	if denied.Eligible || denied.ReasonCodes[0] != OpenPilotReasonCommandDenied || denied.TestCommand != "rm -rf /" {
		t.Fatalf("denied = %+v", denied)
	}
	closed, _ := inboxIssue(report, "owner/alpha#5")
	if closed.Eligible || closed.ReasonCodes[0] != OpenPilotReasonIssueNotOpen {
		t.Fatalf("closed = %+v", closed)
	}
	unspecified, _ := inboxIssue(report, "owner/alpha#6")
	if unspecified.Eligible || unspecified.SpecReady || !unspecified.SpecRequired || unspecified.ReasonCodes[0] != OpenPilotReasonIssueSpecMissing {
		t.Fatalf("unspecified = %+v", unspecified)
	}
	active, _ := inboxIssue(report, "owner/alpha#7")
	if active.Eligible || active.Claim == nil || !active.Claim.ActiveRun || active.Claim.RunID != activeRun.ID || active.Claim.RunStatus != string(RunQueued) || active.Claim.Phase != string(OpenPilotPhaseRequested) || active.Action != OpenPilotInboxActionNone || active.ReasonCodes[0] != OpenPilotReasonIssueRunActive || active.Links.Run != "/api/runs/"+activeRun.ID {
		t.Fatalf("active = %+v claim=%+v", active, active.Claim)
	}
	failed, _ := inboxIssue(report, "owner/alpha#8")
	if failed.Eligible || failed.Claim == nil || failed.Claim.ActiveRun || failed.Claim.Phase != string(OpenPilotPhaseFailed) || failed.Claim.LastError != "agent crashed" || failed.Claim.RunStatus != string(RunFailed) || failed.Claim.PullRequestNumber != 80 || failed.Links.PullRequest != "https://gitea.test/owner/alpha/pulls/80" || failed.Links.Run != "/api/runs/"+failedRun.ID || failed.Action != OpenPilotInboxActionResume || failed.ReasonCodes[0] != OpenPilotReasonIssueClaimed {
		t.Fatalf("failed = %+v claim=%+v", failed, failed.Claim)
	}
	if check := inboxCheck(failed, "issue_state"); !strings.Contains(check.Message, "agent crashed") || !strings.Contains(check.Remediation, "/api/open-pilot/state/resume") {
		t.Fatalf("failed state check = %+v", check)
	}
	stateOnly, ok := inboxIssue(report, "owner/beta#9")
	if !ok || stateOnly.Source != OpenPilotInboxSourceState || stateOnly.Eligible || stateOnly.Claim == nil || stateOnly.Claim.Phase != string(OpenPilotPhaseRequested) || inboxCheck(stateOnly, "upstream").Code != OpenPilotReasonUpstreamNotListed {
		t.Fatalf("state-only = %+v", stateOnly)
	}
}

func TestOpenPilotInboxDistinguishesUnavailableUpstreamFromEmptyQueue(t *testing.T) {
	fixture := newInboxFixture(t)
	if _, run := fixture.dispatch(t, "owner/alpha", "3"); run.ID == "" {
		t.Fatal("claim was not created")
	}

	// Empty queue: every admitted repository listed successfully with no candidates.
	code, _, report := fixture.inbox(t, "?eligibility=eligible")
	if code != http.StatusOK || report.Upstream != OpenPilotInboxUpstreamAvailable || len(report.Issues) != 0 || report.Matched != 0 || report.UpstreamError != "" || len(report.Checks) != 0 {
		t.Fatalf("empty queue report = %+v", report)
	}

	// Partial: alpha fails, beta lists. The alpha claim is still shown from the store.
	fixture.reader.errs["owner/alpha"] = errors.New("gitea issue list failed: 502 Bad Gateway token=hunter2")
	code, body, report := fixture.inbox(t, "")
	if code != http.StatusOK || report.Upstream != OpenPilotInboxUpstreamPartial || !strings.Contains(report.UpstreamError, "owner/alpha: gitea issue list failed: 502 Bad Gateway") {
		t.Fatalf("partial report = %+v", report)
	}
	assertInboxRedacted(t, body, fixture.manifest)
	if len(report.Checks) != 1 || report.Checks[0].Code != OpenPilotReasonUpstreamUnavailable || report.Checks[0].Status != OpenPilotReadinessWarn || len(report.Blockers) != 0 {
		t.Fatalf("partial checks = %+v", report.Checks)
	}
	for _, repository := range report.Repositories {
		switch repository.ID {
		case "alpha":
			if repository.Upstream != OpenPilotInboxRepositoryUnavailable || repository.UpstreamError == "" || repository.Claims != 1 {
				t.Fatalf("alpha = %+v", repository)
			}
		case "beta":
			if repository.Upstream != OpenPilotInboxRepositoryListed || repository.UpstreamError != "" {
				t.Fatalf("beta = %+v", repository)
			}
		}
	}
	claim, ok := inboxIssue(report, "owner/alpha#3")
	if !ok || claim.Source != OpenPilotInboxSourceState || inboxCheck(claim, "upstream").Code != OpenPilotReasonUpstreamUnavailable || claim.Claim == nil || !claim.Claim.ActiveRun {
		t.Fatalf("claim under unavailable upstream = %+v", claim)
	}

	// Unavailable: every admitted repository failed.
	fixture.reader.errs["owner/beta"] = errors.New("dial tcp: connection refused")
	if _, _, report := fixture.inbox(t, ""); report.Upstream != OpenPilotInboxUpstreamUnavailable || len(report.Issues) != 1 {
		t.Fatalf("unavailable report = %+v", report)
	}

	// No reader: nothing is queried and the report says so.
	fixture.api.SetOpenPilotIssueReader(nil)
	if _, _, report := fixture.inbox(t, ""); report.Upstream != OpenPilotInboxUpstreamNotQueried || report.UpstreamError == "" || len(report.Issues) != 1 {
		t.Fatalf("no-reader report = %+v", report)
	}
}

func TestOpenPilotInboxResolvesAdmissionBeforeGitea(t *testing.T) {
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "disabled")
	fixture := newInboxFixture(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "disabled")
	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	runner := NewRunner(fixture.store)
	if runner.openPilotGateError != nil || runner.openPilotGate.mode != "disabled" {
		t.Fatalf("gate = %+v err=%v", runner.openPilotGate, runner.openPilotGateError)
	}
	api := NewAPI(fixture.store, runner)
	api.SetOpenPilotIssueReader(fixture.reader)
	fixture.api, fixture.manifest = api, manifest
	fixture.reader.listed["owner/alpha"] = []GiteaIssue{{ID: 1, Number: 1, State: "open", Labels: []string{"ready-for-agent", "agent:queued"}}}
	code, body, report := fixture.inbox(t, "")
	if code != http.StatusOK || report.Upstream != OpenPilotInboxUpstreamNotQueried || len(report.Issues) != 0 || fixture.reader.lists != 0 || fixture.reader.assignedLists != 0 {
		t.Fatalf("disabled report = %+v lists=%d body=%s", report, fixture.reader.lists, body)
	}
	for _, repository := range report.Repositories {
		if repository.Admitted || repository.AdmissionCode != OpenPilotReasonActivationDisabled || repository.Upstream != OpenPilotInboxRepositoryNotQueried {
			t.Fatalf("repository = %+v", repository)
		}
	}

	// Gate unavailable: no registry, no Gitea calls, a single stable blocker.
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", "")
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	broken := NewAPI(fixture.store, NewRunner(fixture.store))
	broken.SetOpenPilotIssueReader(fixture.reader)
	fixture.api = broken
	if code, _, report := fixture.inbox(t, ""); code != http.StatusOK || len(report.Blockers) != 1 || report.Blockers[0] != OpenPilotReasonGateUnavailable || len(report.Repositories) != 0 || fixture.reader.lists != 0 {
		t.Fatalf("gate-unavailable report = %d %+v", code, report)
	}
}

func TestOpenPilotInboxProofModeAdmitsOnlyProofTargets(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	reader := &inboxIssueReader{listed: map[string][]GiteaIssue{"owner/alpha": {
		{ID: 1, Number: 1, State: "open", Title: "proof target", Body: inboxSpecBody, Labels: []string{"ready-for-agent", "agent:queued"}},
		{ID: 2, Number: 2, State: "open", Title: "not a proof target", Body: inboxSpecBody, Labels: []string{"ready-for-agent", "agent:queued"}},
	}}, assigned: map[string][]GiteaIssue{}, errs: map[string]error{}}
	fixture.api.SetOpenPilotIssueReader(reader)
	request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/inbox?repo=legacy/alpha", nil)
	response := httptest.NewRecorder()
	fixture.api.routes().ServeHTTP(response, request)
	var report OpenPilotInboxReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil || response.Code != http.StatusOK {
		t.Fatalf("code=%d err=%v body=%s", response.Code, err, response.Body.String())
	}
	assertReadinessRedacted(t, response.Body.String(), fixture.manifest)
	if report.ActivationMode != "durable" || len(report.Repositories) != 1 || !report.Repositories[0].Admitted || len(report.Issues) != 2 {
		t.Fatalf("report = %+v", report)
	}
	admitted, _ := inboxIssue(report, "owner/alpha#1")
	excluded, _ := inboxIssue(report, "owner/alpha#2")
	if !admitted.Eligible || excluded.Eligible || excluded.ReasonCodes[0] != OpenPilotReasonRolloutProofExcluded {
		t.Fatalf("admitted=%+v excluded=%+v", admitted, excluded)
	}
}

func TestOpenPilotInboxPaginatesAndFilters(t *testing.T) {
	fixture := newInboxFixture(t)
	both := []string{"ready-for-agent", "agent:queued"}
	for number := 1; number <= 7; number++ {
		issue := GiteaIssue{ID: 100 + number, Number: number, State: "open", Title: "alpha issue " + strconv.Itoa(number), Body: inboxSpecBody, Labels: both}
		if number%2 == 0 {
			issue.Labels = []string{"ready-for-agent"}
		}
		fixture.reader.listed["owner/alpha"] = append(fixture.reader.listed["owner/alpha"], issue)
	}
	fixture.reader.listed["owner/beta"] = []GiteaIssue{{ID: 201, Number: 11, State: "open", Title: "beta needle", Body: inboxSpecBody, Labels: both, Assignees: []GiteaIssueAssignee{{Login: "open-pilot"}}}}
	if _, run := fixture.dispatch(t, "owner/alpha", "7"); run.ID == "" {
		t.Fatal("claim was not created")
	}

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		query := "?limit=3"
		if cursor != "" {
			query += "&cursor=" + cursor
		}
		code, body, report := fixture.inbox(t, query)
		if code != http.StatusOK || report.Filter.Limit != 3 || report.Matched != 8 || len(report.Issues) == 0 || len(report.Issues) > 3 {
			t.Fatalf("page %d: code=%d report=%+v body=%s", pages, code, report, body)
		}
		pages++
		for _, issue := range report.Issues {
			seen[issue.Key]++
		}
		if !report.HasMore {
			if report.NextCursor != "" {
				t.Fatalf("final page carries a cursor: %+v", report)
			}
			break
		}
		if report.NextCursor == "" || report.NextCursor == cursor {
			t.Fatalf("page %d has_more without a fresh cursor: %+v", pages, report)
		}
		cursor = report.NextCursor
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 3 || len(seen) != 8 {
		t.Fatalf("pages=%d seen=%v", pages, seen)
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("%s listed %d times", key, count)
		}
	}

	if _, _, report := fixture.inbox(t, "?eligibility=eligible"); report.Matched != 4 {
		t.Fatalf("eligible matched=%d", report.Matched)
	}
	if _, _, report := fixture.inbox(t, "?eligibility=ineligible"); report.Matched != 3 {
		t.Fatalf("ineligible matched=%d", report.Matched)
	}
	if _, _, report := fixture.inbox(t, "?eligibility=claimed&phase=requested"); report.Matched != 1 || report.Issues[0].Key != "owner/alpha#7" {
		t.Fatalf("claimed report=%+v", report)
	}
	if _, _, report := fixture.inbox(t, "?phase=failed"); report.Matched != 0 {
		t.Fatalf("failed phase matched=%d", report.Matched)
	}
	if _, _, report := fixture.inbox(t, "?pickup=assigned"); report.Matched != 1 || report.Issues[0].Key != "owner/beta#11" {
		t.Fatalf("assigned pickup report=%+v", report)
	}
	if _, _, report := fixture.inbox(t, "?pickup=none"); report.Matched != 3 {
		t.Fatalf("no pickup matched=%d", report.Matched)
	}
	if _, _, report := fixture.inbox(t, "?q=needle"); report.Matched != 1 || report.Issues[0].Repo != "owner/beta" {
		t.Fatalf("query report=%+v", report)
	}
	if _, _, report := fixture.inbox(t, "?repo=legacy/beta"); len(report.Repositories) != 1 || report.Repositories[0].ID != "beta" || report.Matched != 1 {
		t.Fatalf("repo filter report=%+v", report)
	}
	if code, _, report := fixture.inbox(t, "?repo=owner/gamma"); code != http.StatusOK || len(report.Blockers) != 1 || report.Blockers[0] != OpenPilotReasonRepositoryUnregistered || len(report.Issues) != 0 || len(report.Repositories) != 0 {
		t.Fatalf("unregistered repo report=%d %+v", code, report)
	}
	for _, query := range []string{"?limit=0", "?limit=101", "?limit=abc", "?eligibility=bogus", "?phase=bogus", "?pickup=bogus", "?cursor=not-a-cursor", "?cursor=" + encodeOpenPilotInboxCursor("owner/alpha", "abc"), "?q=" + strings.Repeat("x", 129)} {
		if code, _, _ := fixture.inbox(t, query); code != http.StatusBadRequest {
			t.Fatalf("%s: code=%d, want 400", query, code)
		}
	}
	if code, _, report := fixture.inbox(t, "?cursor="+encodeOpenPilotInboxCursor("zzz/last", "1")); code != http.StatusOK || len(report.Issues) != 0 || report.HasMore {
		t.Fatalf("past-the-end cursor report=%d %+v", code, report)
	}
}

func TestOpenPilotInboxIsControlListenerOnlyAndSecretFree(t *testing.T) {
	fixture := newInboxFixture(t)
	fixture.reader.listed["owner/alpha"] = []GiteaIssue{{ID: 1, Number: 1, State: "open", Title: "issue", Body: inboxSpecBody, Labels: []string{"ready-for-agent", "agent:queued"}}}
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	for _, test := range []struct {
		name    string
		handler http.Handler
		token   string
		want    int
	}{
		{name: "control unauthenticated", handler: fixture.api.ControlPlaneHandler(), want: http.StatusUnauthorized},
		{name: "control read token", handler: fixture.api.ControlPlaneHandler(), token: "read-token", want: http.StatusOK},
		{name: "control admin token", handler: fixture.api.ControlPlaneHandler(), token: "admin-token", want: http.StatusOK},
		{name: "model listener", handler: fixture.api.ModelIngressHandler(), token: "admin-token", want: http.StatusNotFound},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/inbox", nil)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s: status = %d, want %d: %s", test.name, response.Code, test.want, response.Body.String())
		}
		if test.want == http.StatusNotFound && !strings.Contains(response.Body.String(), "route not available on this listener") {
			t.Fatalf("%s: model listener served the inbox: %s", test.name, response.Body.String())
		}
		if test.want == http.StatusOK {
			assertInboxRedacted(t, response.Body.String(), fixture.manifest)
			if strings.Contains(response.Body.String(), "read-token") || strings.Contains(response.Body.String(), "admin-token") {
				t.Fatalf("%s: inbox echoed a control token", test.name)
			}
		}
	}
	// The inbox is read-only: listing never creates claims or runs.
	if len(fixture.store.ListRuns()) != 0 || len(fixture.store.ListOpenPilotIssueStates()) != 0 {
		t.Fatal("inbox listing mutated runs or lifecycle state")
	}
}
