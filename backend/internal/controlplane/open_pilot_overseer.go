package controlplane

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type OpenPilotOverseerConfig struct {
	Enabled         bool `json:"enabled"`
	QueueEnabled    bool `json:"queue_enabled"`
	RecoveryEnabled bool `json:"recovery_enabled"`
	MaxOpenLanes    int  `json:"max_open_lanes"`
	IntervalSeconds int  `json:"interval_seconds"`
}

type OpenPilotOverseerAction struct {
	ID                 string     `json:"id"`
	Kind               string     `json:"kind"`
	Repo               string     `json:"repo"`
	Issue              int        `json:"issue"`
	RunID              string     `json:"run_id,omitempty"`
	ResultingRunID     string     `json:"resulting_run_id,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
	Outcome            string     `json:"outcome"`
	ExpectedUpdatedAt  time.Time  `json:"expected_updated_at,omitempty"`
	ExpectedAttempt    int        `json:"expected_attempt,omitempty"`
	MergeCommitSHA     string     `json:"merge_commit_sha,omitempty"`
	RepositoryID       string     `json:"repository_id,omitempty"`
	ExecutorGeneration string     `json:"executor_generation,omitempty"`
}

type OpenPilotOverseerIncident struct {
	Key          string    `json:"key"`
	Repo         string    `json:"repo"`
	Issue        int       `json:"issue"`
	RunID        string    `json:"run_id,omitempty"`
	Kind         string    `json:"kind"`
	Message      string    `json:"message"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	Observations int       `json:"observations"`
	Fingerprint  string    `json:"fingerprint,omitempty"`
}

type OpenPilotOverseerLane struct {
	Repo    string   `json:"repo"`
	Issue   string   `json:"issue"`
	Reasons []string `json:"reasons"`
}

type OpenPilotOverseerCandidate struct {
	Repo     string   `json:"repo"`
	Issue    int      `json:"issue"`
	Title    string   `json:"title"`
	Blockers []string `json:"blockers"`
}

type OpenPilotOverseerReport struct {
	CheckedAt      time.Time                    `json:"checked_at"`
	Complete       bool                         `json:"complete"`
	Status         string                       `json:"status"`
	Error          string                       `json:"error,omitempty"`
	Lanes          []OpenPilotOverseerLane      `json:"lanes"`
	AvailableLanes int                          `json:"available_lanes"`
	Candidates     []OpenPilotOverseerCandidate `json:"candidates"`
	Incidents      []OpenPilotOverseerIncident  `json:"incidents"`
	Action         *OpenPilotOverseerAction     `json:"action,omitempty"`
}

type OpenPilotOverseerState struct {
	Revision    int64                                `json:"revision"`
	Config      OpenPilotOverseerConfig              `json:"config"`
	NextCheckAt time.Time                            `json:"next_check_at,omitempty"`
	ReadySeen   map[string]time.Time                 `json:"ready_seen"`
	Incidents   map[string]OpenPilotOverseerIncident `json:"incidents"`
	History     []OpenPilotOverseerAction            `json:"history"`
	Pending     *OpenPilotOverseerAction             `json:"pending,omitempty"`
	Report      OpenPilotOverseerReport              `json:"report"`
}

func defaultOverseerState() OpenPilotOverseerState {
	return OpenPilotOverseerState{Config: OpenPilotOverseerConfig{QueueEnabled: true, RecoveryEnabled: true, MaxOpenLanes: 3, IntervalSeconds: 900}, ReadySeen: map[string]time.Time{}, Incidents: map[string]OpenPilotOverseerIncident{}, History: []OpenPilotOverseerAction{}, Report: OpenPilotOverseerReport{Status: "disabled", Lanes: []OpenPilotOverseerLane{}, Candidates: []OpenPilotOverseerCandidate{}, Incidents: []OpenPilotOverseerIncident{}}}
}

type OpenPilotOverseer struct {
	store       Store
	runner      *Runner
	client      OpenPilotQueueClient
	persistence overseerPersistence
	now         func() time.Time
	guard       func(context.Context) error
}

func NewOpenPilotOverseer(store Store, runner *Runner, client OpenPilotQueueClient) *OpenPilotOverseer {
	return &OpenPilotOverseer{store: store, runner: runner, client: client, persistence: newOverseerPersistence(store), now: time.Now}
}

func (o *OpenPilotOverseer) State(ctx context.Context) (OpenPilotOverseerState, error) {
	return o.persistence.load(ctx)
}

func (o *OpenPilotOverseer) ReleaseReviewedPending(ctx context.Context, expected int64, id, reason, actor string) (OpenPilotOverseerState, error) {
	var result OpenPilotOverseerState
	if strings.TrimSpace(reason) == "" || len(reason) > 512 {
		return result, ErrInvalid
	}
	err := o.persistence.cycle(ctx, func(ctx context.Context) error {
		state, err := o.State(ctx)
		if err != nil {
			return err
		}
		if state.Revision != expected || state.Pending == nil || state.Pending.ID != id {
			return ErrConflict
		}
		report := state.Report
		o.finish(&state, &report, "reservation released after operator review by "+RedactForLog(actor)+": "+RedactForLog(reason))
		state.Report = report
		state.NextCheckAt = time.Time{}
		if err := guardOverseerCycle(ctx); err != nil {
			return err
		}
		if err := o.persistence.save(ctx, &state); err != nil {
			return err
		}
		result = state
		return nil
	})
	return result, err
}

func (o *OpenPilotOverseer) Configure(ctx context.Context, expected int64, config OpenPilotOverseerConfig, actor string) (OpenPilotOverseerState, error) {
	if config.MaxOpenLanes < 1 || config.MaxOpenLanes > 100 || config.IntervalSeconds < 60 || config.IntervalSeconds > 3600 {
		return OpenPilotOverseerState{}, fmt.Errorf("%w: lane limit must be 1–100 and interval 60–3600 seconds", ErrInvalid)
	}
	state, err := o.State(ctx)
	if err != nil {
		return state, err
	}
	if state.Revision != expected {
		return state, ErrConflict
	}
	state.Config = config
	state.NextCheckAt = time.Time{}
	state.History = append(state.History, OpenPilotOverseerAction{ID: newID("ovr"), Kind: "configure", CreatedAt: o.now().UTC(), Outcome: "configured by " + RedactForLog(actor)})
	err = o.persistence.save(ctx, &state)
	return state, err
}

func (o *OpenPilotOverseer) Tick(ctx context.Context, force, observeOnly bool) error {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	return o.persistence.cycle(ctx, func(ctx context.Context) error {
		state, err := o.State(ctx)
		if err != nil {
			return err
		}
		now := o.now().UTC()
		if !force && (!state.Config.Enabled || state.NextCheckAt.After(now)) {
			return nil
		}
		if state.ReadySeen == nil {
			state.ReadySeen = map[string]time.Time{}
		}
		if state.Incidents == nil {
			state.Incidents = map[string]OpenPilotOverseerIncident{}
		}
		save := func() error {
			if err := guardOverseerCycle(ctx); err != nil {
				return err
			}
			return o.persistence.save(ctx, &state)
		}
		report := OpenPilotOverseerReport{CheckedAt: now, Status: "observing", Lanes: []OpenPilotOverseerLane{}, Candidates: []OpenPilotOverseerCandidate{}, Incidents: []OpenPilotOverseerIncident{}}
		err = o.inspect(ctx, &state, &report, save, observeOnly || !state.Config.Enabled)
		if err != nil {
			report.Status = "partial"
			report.Error = RedactForLog(err.Error())
			report.Complete = false
		}
		state.Report = report
		state.NextCheckAt = now.Add(time.Duration(state.Config.IntervalSeconds) * time.Second)
		if saveErr := save(); saveErr != nil {
			return saveErr
		}
		return err
	})
}

func overseerKey(repo, issue string) string { return strings.ToLower(repo) + "#" + issue }

func (o *OpenPilotOverseer) observe(state *OpenPilotOverseerState, report *OpenPilotOverseerReport, repo, issue, runID, kind, message string, fingerprints ...string) OpenPilotOverseerIncident {
	key := overseerKey(repo, issue) + "/" + kind
	previous, ok := state.Incidents[key]
	incident := OpenPilotOverseerIncident{Key: key, Repo: repo, RunID: runID, Kind: kind, Message: RedactForLog(message), FirstSeen: report.CheckedAt, LastSeen: report.CheckedAt, Observations: 1}
	if len(fingerprints) > 0 {
		incident.Fingerprint = fingerprints[0]
	}
	incident.Issue, _ = strconv.Atoi(issue)
	if ok && state.Report.Complete && previous.RunID == runID && previous.Message == incident.Message && previous.Fingerprint == incident.Fingerprint && previous.LastSeen.Equal(state.Report.CheckedAt) {
		incident.FirstSeen = previous.FirstSeen
		incident.Observations = previous.Observations + 1
	}
	state.Incidents[key] = incident
	report.Incidents = append(report.Incidents, incident)
	return incident
}

func overseerBudget(state *OpenPilotOverseerState, action OpenPilotOverseerAction, now time.Time) bool {
	if len(state.History) >= 10000 {
		return false
	} // Retain evidence; stop rather than silently prune it.
	hour, day := 0, 0
	for _, previous := range state.History {
		if previous.Kind == "configure" {
			continue
		}
		if previous.CreatedAt.After(now.Add(-time.Hour)) {
			hour++
		}
		sameOwner := strings.EqualFold(previous.Repo, action.Repo)
		if action.RepositoryID != "" && previous.RepositoryID != "" {
			sameOwner = action.RepositoryID == previous.RepositoryID && action.ExecutorGeneration == previous.ExecutorGeneration
		}
		if action.Kind != "queue_label" && previous.Kind != "queue_label" && sameOwner && previous.Issue == action.Issue && previous.CreatedAt.After(now.Add(-24*time.Hour)) {
			day++
		}
	}
	return hour < 4 && day < 2 && state.Pending == nil
}

func (o *OpenPilotOverseer) reserve(ctx context.Context, state *OpenPilotOverseerState, action OpenPilotOverseerAction, save func() error) error {
	if !overseerBudget(state, action, o.now().UTC()) {
		return fmt.Errorf("overseer intervention budget exhausted or outcome pending")
	}
	if err := guardOverseerCycle(ctx); err != nil {
		return err
	}
	if o.guard != nil {
		if err := o.guard(ctx); err != nil {
			return err
		}
	}
	action.ID = newID("ovr")
	action.CreatedAt = o.now().UTC()
	action.Outcome = "reserved"
	state.Pending = &action
	return save()
}

func (o *OpenPilotOverseer) finish(state *OpenPilotOverseerState, report *OpenPilotOverseerReport, outcome string) {
	action := *state.Pending
	now := o.now().UTC()
	action.CompletedAt = &now
	action.Outcome = outcome
	state.History = append(state.History, action)
	state.Pending = nil
	report.Action = &action
}

func (o *OpenPilotOverseer) resolvePending(ctx context.Context, state *OpenPilotOverseerState, report *OpenPilotOverseerReport, save func() error) error {
	action := state.Pending
	if action == nil {
		return nil
	}
	confirmed := false
	switch action.Kind {
	case "queue_label":
		issue, err := o.client.FetchQueueIssue(ctx, action.Repo, action.Issue)
		if err != nil {
			return err
		}
		confirmed = hasAllLabels(issue.Labels, []string{"agent:queued"})
	case "reconcile":
		current, err := o.store.WithContext(ctx).OpenPilotIssueState(action.Repo, strconv.Itoa(action.Issue))
		if err != nil {
			return err
		}
		confirmed = current.RunID == action.RunID && current.Phase == OpenPilotPhaseIssueClosed && current.MergeCommitSHA == action.MergeCommitSHA
	case "coding_repair":
		current, err := o.store.WithContext(ctx).OpenPilotIssueState(action.Repo, strconv.Itoa(action.Issue))
		if err != nil {
			return err
		}
		detail, err := o.store.RunDetailContext(ctx, current.RunID)
		if err != nil {
			return err
		}
		confirmed = current.RunID != action.RunID && detail.Run.Inputs[openPilotCodingRepairParentKey] == action.RunID
		if confirmed {
			action.ResultingRunID = current.RunID
		}
	case "requeue":
		detail, err := o.store.RunDetailContext(ctx, action.RunID)
		if err != nil {
			return err
		}
		confirmed = detail.Run.Status == RunQueued || detail.Run.AttemptCount > action.ExpectedAttempt
	}
	if !confirmed {
		return fmt.Errorf("uncertain %s outcome for %s#%d; reserved budget retained for operator review", action.Kind, action.Repo, action.Issue)
	}
	o.finish(state, report, "confirmed after interrupted cycle")
	return save()
}

func (o *OpenPilotOverseer) inspect(ctx context.Context, state *OpenPilotOverseerState, report *OpenPilotOverseerReport, save func() error, observeOnly bool) error {
	if o.client == nil || o.runner == nil {
		return fmt.Errorf("overseer dependencies unavailable")
	}
	if o.guard != nil {
		if err := o.guard(ctx); err != nil {
			return err
		}
	}
	if err := o.runner.openPilotGateError; err != nil {
		return err
	}
	rollout, err := o.store.OpenPilotRolloutStatus()
	if err != nil {
		return err
	}
	if rollout.Mode != "allow" && rollout.Mode != "legacy" {
		return fmt.Errorf("normal Open Pilot admission is paused by rollout %s", rollout.Mode)
	}
	if state.Pending != nil {
		return o.resolvePending(ctx, state, report, save)
	}
	repositories, err := o.client.ListOpenPilotRepositories(ctx)
	if err != nil {
		return err
	}
	visible := map[string]bool{}
	for _, repo := range repositories {
		visible[strings.ToLower(repo)] = true
	}
	ready, err := o.client.ListQueueIssues(ctx, "ready-for-agent", false)
	if err != nil {
		return err
	}
	queued, err := o.client.ListQueueIssues(ctx, "agent:queued", false)
	if err != nil {
		return err
	}
	assigned := []OpenPilotQueueIssue{}
	if o.assignee() != "" {
		assigned, err = o.client.ListQueueIssues(ctx, "", true)
		if err != nil {
			return err
		}
	}
	prs, err := o.client.ListQueueIssues(ctx, "__pull_requests__", false)
	if err != nil {
		return err
	}
	lister, ok := o.store.(interface {
		ListOpenPilotOverseerIssueStates(context.Context) ([]OpenPilotIssueState, error)
	})
	if !ok {
		return fmt.Errorf("store does not support bounded overseer lifecycle scans")
	}
	states, err := lister.ListOpenPilotOverseerIssueStates(ctx)
	if err != nil {
		return err
	}
	if len(states) > 10000 {
		return fmt.Errorf("overseer lifecycle scan exceeds 10000 states")
	}
	sort.Slice(states, func(i, j int) bool {
		return overseerKey(states[i].Repo, states[i].Issue) < overseerKey(states[j].Repo, states[j].Issue)
	})
	runs := map[string]Run{}
	cursor := ListRunsPageInput{Limit: 100}
	for page := 0; page < 200; page++ {
		result, err := o.store.ListRunsPageContext(ctx, cursor)
		if err != nil {
			return err
		}
		for _, run := range result.Runs {
			runs[run.ID] = run
		}
		if !result.HasMore {
			break
		}
		if len(result.Runs) == 0 || page == 199 {
			return fmt.Errorf("overseer run scan incomplete")
		}
		last := result.Runs[len(result.Runs)-1]
		cursor.BeforeCreated = &last.CreatedAt
		cursor.BeforeID = last.ID
	}
	lanes := map[string]*OpenPilotOverseerLane{}
	claims := map[string]OpenPilotIssueState{}
	addLane := func(repo, issue, reason string) {
		key := overseerKey(repo, issue)
		if lanes[key] == nil {
			lanes[key] = &OpenPilotOverseerLane{Repo: repo, Issue: issue, Reasons: []string{}}
		}
		lanes[key].Reasons = append(lanes[key].Reasons, reason)
	}
	for _, s := range states {
		claims[overseerKey(firstNonEmpty(s.CanonicalRepo, s.Repo), s.Issue)] = s
		claims[overseerKey(s.Repo, s.Issue)] = s
	}
	unpicked := false
	for _, issue := range append(queued, assigned...) {
		if issue.IsPR || issue.State != "open" {
			continue
		}
		number := strconv.Itoa(issue.Number)
		if hasAllLabels(issue.Labels, []string{"agent:queued"}) || openPilotIssueAssignedTo(GiteaIssue{Assignees: issue.Assignees}, o.assignee()) {
			addLane(issue.Repo, number, "queued label or runtime assignment")
			if _, ok := claims[overseerKey(issue.Repo, number)]; !ok {
				unpicked = true
			}
		}
	}
	for _, run := range runs {
		if run.WorkflowID == string(WorkflowOpenPilot) && (run.Status == RunQueued || run.Status == RunRunning) && run.Inputs["repo"] != "" && run.Inputs["issue"] != "" {
			addLane(run.Inputs["repo"], run.Inputs["issue"], "active run")
		}
	}
	for _, prIssue := range prs {
		pr, err := o.client.GetPullRequest(ctx, prIssue.Repo, prIssue.Number)
		if err != nil {
			return err
		}
		if strings.HasPrefix(pr.Head.Ref, "open-pilot/") || (o.assignee() != "" && strings.EqualFold(prIssue.Author, o.assignee())) {
			issue := "pr-" + strconv.Itoa(pr.Number)
			if match := overseerBranchIssue.FindStringSubmatch(pr.Head.Ref); len(match) > 1 {
				issue = match[1]
			}
			addLane(prIssue.Repo, issue, "open Open Pilot PR")
		}
	}
	type recovery struct {
		state    OpenPilotIssueState
		run      Run
		pr       *GiteaPullRequest
		issue    *GiteaIssue
		kind     string
		incident OpenPilotOverseerIncident
		feedback string
	}
	recoveries := []recovery{}
	for _, s := range states {
		if s.Phase == OpenPilotPhaseIssueClosed {
			continue
		}
		run, retained := runs[s.RunID]
		if retained && (run.Status == RunCanceled || run.Status == RunQueued || run.Status == RunRunning) {
			continue
		}
		if s.Phase != OpenPilotPhaseFailed && s.Phase != OpenPilotPhasePaused {
			continue
		}
		if !visible[strings.ToLower(s.Repo)] {
			continue
		}
		issue, err := o.client.FetchIssue(ctx, s.Repo, s.Issue)
		if err != nil {
			return err
		}
		if issue.State == "open" {
			addLane(s.Repo, s.Issue, "unresolved failed or paused lifecycle")
		}
		if !retained {
			o.observe(state, report, s.Repo, s.Issue, s.RunID, "historical_run_unavailable", "Retained lifecycle has no run detail; no recovery attempted")
			continue
		}
		var recordedPR *GiteaPullRequest
		if s.PullRequestNum > 0 {
			pr, err := o.client.GetPullRequest(ctx, s.Repo, s.PullRequestNum)
			if err != nil {
				return err
			}
			recordedPR = &pr
			if pr.Merged && issue.State == "closed" {
				incident := o.observe(state, report, s.Repo, s.Issue, s.RunID, "external_merge", "Recorded PR merged and issue closed outside lifecycle", overseerRecoveryFingerprint(s, run))
				recoveries = append(recoveries, recovery{state: s, run: run, pr: &pr, issue: &issue, kind: "reconcile", incident: incident})
				continue
			}
			if pr.Merged || pr.State != "open" {
				o.observe(state, report, s.Repo, s.Issue, s.RunID, "pr_requires_operator", "Recorded PR is merged or closed while lifecycle remains unresolved")
				continue
			}
		}
		if issue.State != "open" {
			continue
		}
		if s.Phase == OpenPilotPhaseFailed && s.LastStepName == "open-pilot.verify-command" && run.Status == RunFailed {
			detail, err := o.store.RunDetailContext(ctx, s.RunID)
			if err != nil {
				return err
			}
			kind, feedback := openPilotVerificationFailure(detail)
			count, err := openPilotCodingRepairCount(s.ResumeInputs)
			if err != nil {
				return err
			}
			if kind == "coding_repair" && count+s.RepairAttempts >= s.MaxRepairAttempts {
				kind, feedback = "repair_budget_exhausted", "Coding/review repair budget exhausted; operator review required"
			}
			incident := o.observe(state, report, s.Repo, s.Issue, s.RunID, kind, feedback, overseerRecoveryFingerprint(s, run))
			if kind == "coding_repair" && s.PullRequestNum == 0 && run.PipelineSnapshot != nil {
				recoveries = append(recoveries, recovery{state: s, run: run, kind: "coding_repair", incident: incident, feedback: feedback})
			}
			continue
		}
		incident := o.observe(state, report, s.Repo, s.Issue, s.RunID, "terminal_failure", firstNonEmpty(s.OperatorSummary, run.Summary), overseerRecoveryFingerprint(s, run))
		if s.Phase == OpenPilotPhaseFailed && (run.Status == RunFailed || run.Status == RunTimedOut) && run.AttemptCount > 0 && run.AttemptCount < run.MaxAttempts && s.RepairAttempts < s.MaxRepairAttempts && overseerTransient(run.Summary) {
			recoveries = append(recoveries, recovery{state: s, run: run, pr: recordedPR, kind: "requeue", incident: incident})
		}
	}
	for _, lane := range lanes {
		report.Lanes = append(report.Lanes, *lane)
	}
	sort.Slice(report.Lanes, func(i, j int) bool {
		return overseerKey(report.Lanes[i].Repo, report.Lanes[i].Issue) < overseerKey(report.Lanes[j].Repo, report.Lanes[j].Issue)
	})
	report.AvailableLanes = max(0, state.Config.MaxOpenLanes-len(lanes))
	if len(state.ReadySeen)+len(ready) > 20000 {
		return fmt.Errorf("ready observation history exceeds bound; operator retention review required")
	}
	for _, issue := range ready {
		key := overseerKey(issue.Repo, strconv.Itoa(issue.Number))
		if _, ok := state.ReadySeen[key]; !ok {
			state.ReadySeen[key] = report.CheckedAt
		}
	}
	sort.SliceStable(ready, func(i, j int) bool { return overseerIssueLess(ready[i], ready[j], state.ReadySeen) })
	var selected *OpenPilotQueueIssue
	for _, issue := range ready {
		candidate := OpenPilotOverseerCandidate{Repo: issue.Repo, Issue: issue.Number, Title: issue.Title, Blockers: []string{}}
		key := overseerKey(issue.Repo, strconv.Itoa(issue.Number))
		if !visible[strings.ToLower(issue.Repo)] || issue.State != "open" || issue.IsPR {
			candidate.Blockers = append(candidate.Blockers, "repository or issue is not eligible")
		}
		if _, claimed := claims[key]; claimed {
			candidate.Blockers = append(candidate.Blockers, "durable lifecycle exists; preserve prior cancellations and use recovery")
		}
		for _, lane := range lanes {
			if strings.EqualFold(lane.Repo, issue.Repo) {
				candidate.Blockers = append(candidate.Blockers, "repository lane occupied")
				break
			}
		}
		if len(lanes) >= state.Config.MaxOpenLanes {
			candidate.Blockers = append(candidate.Blockers, "global lane limit reached")
		}
		if unpicked {
			candidate.Blockers = append(candidate.Blockers, "waiting for previous admission pickup")
		}
		if len(candidate.Blockers) == 0 {
			blockers, err := o.client.QueueAdmissionBlockers(ctx, issue.Repo)
			if err != nil {
				return err
			}
			candidate.Blockers = append(candidate.Blockers, blockers...)
		}
		if len(candidate.Blockers) == 0 {
			blockers, err := o.queueBlockers(ctx, issue)
			if err != nil {
				return err
			}
			candidate.Blockers = append(candidate.Blockers, blockers...)
		}
		if len(candidate.Blockers) == 0 && selected == nil {
			copy := issue
			selected = &copy
		}
		report.Candidates = append(report.Candidates, candidate)
	}
	report.Complete = true
	report.Status = "healthy"
	if len(report.Incidents) > 0 {
		report.Status = "attention"
	}
	if observeOnly {
		return nil
	}
	if state.Config.RecoveryEnabled {
		for _, recovery := range recoveries {
			if recovery.incident.Observations < 2 || report.CheckedAt.Sub(recovery.incident.FirstSeen) < 15*time.Minute {
				continue
			}
			action := OpenPilotOverseerAction{Kind: recovery.kind, Repo: recovery.state.Repo, Issue: recovery.incident.Issue, RunID: recovery.run.ID, ExpectedUpdatedAt: recovery.state.UpdatedAt, ExpectedAttempt: recovery.run.AttemptCount, RepositoryID: recovery.state.RepositoryID, ExecutorGeneration: recovery.state.ExecutorGeneration}
			if recovery.pr != nil {
				action.MergeCommitSHA = recovery.pr.MergeCommitSHA
			}
			if action.Kind == "requeue" && overseerRepeatedRecovery(state, action, report.CheckedAt) {
				report.Status = "attention"
				report.Error = "A prior recovery failed again; operator review is required"
				continue
			}
			if action.Kind == "coding_repair" {
				progress, err := o.codingRepairProgress(ctx, state, action, report.CheckedAt)
				if err != nil {
					return err
				}
				if !progress {
					report.Error = "A coding hand-back failed again without verified progress; operator review is required"
					continue
				}
			}
			if !overseerBudget(state, action, report.CheckedAt) {
				continue
			}
			current, err := o.store.WithContext(ctx).OpenPilotIssueState(action.Repo, strconv.Itoa(action.Issue))
			if err != nil {
				return err
			}
			if current.RunID != action.RunID || !current.UpdatedAt.Equal(action.ExpectedUpdatedAt) {
				return ErrConflict
			}
			if o.runner.openPilotGate == nil {
				return ErrInvalid
			}
			target, err := o.runner.openPilotGate.ValidateState(current)
			if err != nil {
				return err
			}
			if err := validateOpenPilotRunStateTarget(recovery.run, current, target); err != nil {
				return err
			}
			if err := o.reserve(ctx, state, action, save); err != nil {
				return err
			}
			if err := guardOverseerCycle(ctx); err != nil {
				return err
			}
			if recovery.kind == "coding_repair" {
				issue, err := o.client.FetchIssue(ctx, action.Repo, strconv.Itoa(action.Issue))
				if err != nil {
					return err
				}
				if issue.State != "open" {
					return ErrConflict
				}
				detail, err := o.store.RunDetailContext(ctx, action.RunID)
				if err != nil {
					return err
				}
				kind, feedback := openPilotVerificationFailure(detail)
				if kind != "coding_repair" || feedback != recovery.feedback {
					return ErrConflict
				}
				prepared, err := o.runner.prepareOpenPilotStateRunInput(current, CreateRunInput{WorkflowID: string(WorkflowOpenPilot), Inputs: map[string]string{"repo": current.Repo, "issue": current.Issue}, PipelineSnapshot: detail.Run.PipelineSnapshot, OpenPilotCodingRepair: &OpenPilotCodingRepairRequest{ExpectedRunID: action.RunID, ExpectedUpdatedAt: action.ExpectedUpdatedAt, Feedback: feedback}})
				if err != nil {
					return err
				}
				_, child, err := o.store.WithContext(ctx).ClaimAndCreateOpenPilotResumeRun(current.Repo, current.Issue, prepared)
				if err != nil {
					return err
				}
				state.Pending.ResultingRunID = child.ID
				o.finish(state, report, "returned to Open Pilot coding with verification feedback; subsequent progress requires observation")
				return save()
			} else if recovery.kind == "reconcile" {
				pr, err := o.client.GetPullRequest(ctx, action.Repo, current.PullRequestNum)
				if err != nil {
					return err
				}
				issue, err := o.client.FetchIssue(ctx, action.Repo, strconv.Itoa(action.Issue))
				if err != nil {
					return err
				}
				if _, err := o.store.WithContext(ctx).ReconcileOpenPilotExternalMerge(current, pr, issue); err != nil {
					return err
				}
			} else {
				issue, err := o.client.FetchIssue(ctx, action.Repo, strconv.Itoa(action.Issue))
				if err != nil {
					return err
				}
				if issue.State != "open" {
					return ErrConflict
				}
				if recovery.pr != nil {
					pr, err := o.client.GetPullRequest(ctx, action.Repo, current.PullRequestNum)
					if err != nil {
						return err
					}
					if pr.ID != recovery.pr.ID || pr.Number != current.PullRequestNum || pr.Head.SHA != recovery.pr.Head.SHA || pr.Head.Ref != current.Branch || pr.Merged || pr.State != "open" {
						return ErrConflict
					}
				}
				detail, err := o.store.RunDetailContext(ctx, action.RunID)
				if err != nil {
					return err
				}
				if detail.Run.AttemptCount != action.ExpectedAttempt || !overseerTransient(detail.Run.Summary) {
					return ErrConflict
				}
				requeuer, ok := o.store.(interface {
					RequeueOpenPilotOverseerRun(context.Context, OpenPilotIssueState, int, time.Time) (Run, error)
				})
				if !ok {
					return fmt.Errorf("store does not support fenced overseer requeue")
				}
				if _, err := requeuer.RequeueOpenPilotOverseerRun(ctx, current, action.ExpectedAttempt, report.CheckedAt); err != nil {
					return err
				}
			}
			o.finish(state, report, "recovery applied; subsequent progress requires another observation")
			return save()
		}
	}
	if !state.Config.QueueEnabled || selected == nil {
		return nil
	}
	if env, ok := o.runner.executors.(*EnvWorkflowExecutors); ok && env.openPilotWriteMode != "allow" {
		report.Status = "policy_wait"
		report.Error = "OPEN_PILOT_GITEA_WRITE_MODE must allow label admission"
		return nil
	}
	action := OpenPilotOverseerAction{Kind: "queue_label", Repo: selected.Repo, Issue: selected.Number, ExpectedUpdatedAt: selected.UpdatedAt}
	if !overseerBudget(state, action, report.CheckedAt) {
		report.Status = "budget_wait"
		return nil
	}
	// Re-scan capacity/order immediately before reserving. The preview never writes.
	preview := cloneOverseerState(*state)
	freshReport := OpenPilotOverseerReport{CheckedAt: report.CheckedAt, Lanes: []OpenPilotOverseerLane{}, Candidates: []OpenPilotOverseerCandidate{}, Incidents: []OpenPilotOverseerIncident{}}
	if err := o.inspect(ctx, &preview, &freshReport, func() error { return nil }, true); err != nil {
		return err
	}
	first := OpenPilotOverseerCandidate{}
	for _, candidate := range freshReport.Candidates {
		if len(candidate.Blockers) == 0 {
			first = candidate
			break
		}
	}
	if first.Repo != selected.Repo || first.Issue != selected.Number || freshReport.AvailableLanes < 1 {
		return ErrConflict
	}
	fresh, err := o.client.FetchQueueIssue(ctx, selected.Repo, selected.Number)
	if err != nil {
		return err
	}
	if fresh.ID != selected.ID || !fresh.UpdatedAt.Equal(selected.UpdatedAt) || fresh.State != "open" || !hasAllLabels(fresh.Labels, []string{"ready-for-agent"}) {
		return ErrConflict
	}
	if err := o.reserve(ctx, state, action, save); err != nil {
		return err
	}
	if err := guardOverseerCycle(ctx); err != nil {
		return err
	}
	if err := o.client.AddQueueLabel(ctx, action.Repo, action.Issue); err != nil {
		return err
	}
	readback, err := o.client.FetchQueueIssue(ctx, action.Repo, action.Issue)
	if err != nil {
		return err
	}
	if !hasAllLabels(readback.Labels, append(fresh.Labels, "agent:queued")) {
		return fmt.Errorf("queue label readback incomplete; reservation retained")
	}
	report.Lanes = append(report.Lanes, OpenPilotOverseerLane{Repo: action.Repo, Issue: strconv.Itoa(action.Issue), Reasons: []string{"queue label verified; awaiting native pickup"}})
	report.AvailableLanes = max(0, report.AvailableLanes-1)
	o.finish(state, report, "agent:queued label verified; waiting for native poll pickup")
	return save()
}

func (o *OpenPilotOverseer) assignee() string {
	if env, ok := o.runner.executors.(*EnvWorkflowExecutors); ok {
		return env.openPilotAssignee
	}
	return "open-pilot"
}

var overseerBranchIssue = regexp.MustCompile(`^open-pilot/issue-(\d+)(?:-|$)`)
var overseerPriority = regexp.MustCompile(`(?i)^priority\s*:\s*p(\d+)$`)
var overseerMilestone = regexp.MustCompile(`(?i)\b(?:M|Phase\s*)(\d+)\b`)
var overseerHeading = regexp.MustCompile(`^#{1,6}\s+`)
var overseerDependencyLead = regexp.MustCompile(`(?i)^(?:[-*]\s*)?(?:depends on|blocked by|dependencies|prerequisites)\s*:?\s*`)
var overseerReference = regexp.MustCompile(`https://[^\s)]+/([^/\s]+/[^/\s]+)/issues/(\d+)|([\w.-]+/[\w.-]+)#(\d+)|(?:^|[^\w/])#(\d+)`)
var overseerHuman = regexp.MustCompile(`(?i)\b(?:do not|don't|no)\s+auto[- ]merge\b|\bhuman (?:approval|acceptance)\b`)

func overseerIssueLess(a, b OpenPilotQueueIssue, seen map[string]time.Time) bool {
	priority := func(issue OpenPilotQueueIssue) int {
		p := 999
		for _, label := range issue.Labels {
			if match := overseerPriority.FindStringSubmatch(label); len(match) > 1 {
				value, _ := strconv.Atoi(match[1])
				p = min(p, value)
			}
		}
		return p
	}
	phase := func(issue OpenPilotQueueIssue) int {
		if match := overseerMilestone.FindStringSubmatch(issue.Milestone); len(match) > 1 {
			value, _ := strconv.Atoi(match[1])
			return value
		}
		return 999
	}
	if priority(a) != priority(b) {
		return priority(a) < priority(b)
	}
	if phase(a) != phase(b) {
		return phase(a) < phase(b)
	}
	if !a.DueAt.Equal(b.DueAt) {
		if a.DueAt.IsZero() {
			return false
		}
		if b.DueAt.IsZero() {
			return true
		}
		return a.DueAt.Before(b.DueAt)
	}
	x, y := seen[overseerKey(a.Repo, strconv.Itoa(a.Number))], seen[overseerKey(b.Repo, strconv.Itoa(b.Number))]
	if !x.Equal(y) {
		return x.Before(y)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	if a.Repo != b.Repo {
		return a.Repo < b.Repo
	}
	return a.Number < b.Number
}

func (o *OpenPilotOverseer) queueBlockers(ctx context.Context, issue OpenPilotQueueIssue) ([]string, error) {
	blockers := []string{}
	if !openPilotIssueSpecReady(issue.Body) {
		blockers = append(blockers, "acceptance criteria or verification missing")
	}
	if overseerHuman.MatchString(issue.Body) {
		blockers = append(blockers, "explicit human-approval or no-auto-merge boundary")
	}
	dependencies, err := o.client.QueueDependencies(ctx, issue.Repo, issue.Number)
	if err != nil {
		return nil, err
	}
	for _, dependency := range dependencies {
		if dependency.State != "closed" {
			blockers = append(blockers, fmt.Sprintf("blocked by %s#%d", dependency.Repo, dependency.Number))
		}
	}
	section := false
	for _, line := range strings.Split(issue.Body, "\n") {
		trimmed := strings.TrimSpace(line)
		if overseerHeading.MatchString(trimmed) {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			section = heading == "blocked by" || heading == "dependencies" || heading == "prerequisites" || heading == "depends on"
			continue
		}
		inline := overseerDependencyLead.MatchString(trimmed)
		if inline {
			trimmed = overseerDependencyLead.ReplaceAllString(trimmed, "")
		}
		if (!section && !inline) || trimmed == "" {
			continue
		}
		matches := overseerReference.FindAllStringSubmatch(trimmed, -1)
		if len(matches) == 0 {
			plain := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "-* ")))
			if plain != "none" && plain != "n/a" && plain != "no dependencies" {
				blockers = append(blockers, "ambiguous prerequisite section")
			}
			continue
		}
		for _, match := range matches {
			repo, number := issue.Repo, match[5]
			if match[1] != "" {
				repo, number = match[1], match[2]
				if env, ok := o.client.(*EnvGiteaClient); ok {
					reference, _ := url.Parse(strings.TrimSpace(match[0]))
					base, _ := url.Parse(env.baseURL)
					if reference == nil || base == nil || reference.Host != base.Host {
						blockers = append(blockers, "foreign-host prerequisite needs operator review")
						continue
					}
				}
			}
			if match[3] != "" {
				repo, number = match[3], match[4]
			}
			index, _ := strconv.Atoi(number)
			dependency, err := o.client.FetchQueueIssue(ctx, repo, index)
			if err != nil {
				return nil, err
			}
			if dependency.State != "closed" {
				blockers = append(blockers, fmt.Sprintf("blocked by %s#%d", repo, index))
			}
		}
	}
	return blockers, nil
}

func overseerTransient(summary string) bool {
	text := strings.ToLower(summary)
	return strings.Contains(text, "deadline exceeded") || strings.Contains(text, "connection refused") || strings.Contains(text, "temporary network")
}

func overseerRecoveryFingerprint(state OpenPilotIssueState, run Run) string {
	return fmt.Sprintf("%s/%s/%s/%s/%d", state.RepositoryID, state.ExecutorGeneration, state.UpdatedAt.UTC().Format(time.RFC3339Nano), run.Status, run.AttemptCount)
}

func overseerRepeatedRecovery(state *OpenPilotOverseerState, action OpenPilotOverseerAction, now time.Time) bool {
	for _, previous := range state.History {
		sameOwner := strings.EqualFold(previous.Repo, action.Repo)
		if action.RepositoryID != "" && previous.RepositoryID != "" {
			sameOwner = previous.RepositoryID == action.RepositoryID && previous.ExecutorGeneration == action.ExecutorGeneration
		}
		if previous.Kind == "requeue" && sameOwner && previous.Issue == action.Issue && previous.CreatedAt.After(now.Add(-24*time.Hour)) {
			return true
		}
	}
	return false
}

func (o *OpenPilotOverseer) codingRepairProgress(ctx context.Context, state *OpenPilotOverseerState, action OpenPilotOverseerAction, now time.Time) (bool, error) {
	for _, previous := range state.History {
		sameOwner := strings.EqualFold(previous.Repo, action.Repo)
		if previous.RepositoryID != "" && action.RepositoryID != "" {
			sameOwner = previous.RepositoryID == action.RepositoryID
		}
		if previous.Kind != "coding_repair" || !sameOwner || previous.Issue != action.Issue || previous.ExecutorGeneration != action.ExecutorGeneration || !previous.CreatedAt.After(now.Add(-24*time.Hour)) {
			continue
		}
		if previous.ResultingRunID == "" {
			return false, nil
		}
		detail, err := o.store.RunDetailContext(ctx, previous.ResultingRunID)
		if err != nil {
			return false, err
		}
		progress := false
		for _, step := range detail.Steps {
			if step.Name == "open-pilot.verify-command" && step.Status == StepSucceeded {
				progress = true
			}
		}
		if !progress {
			return false, nil
		}
	}
	return true, nil
}
