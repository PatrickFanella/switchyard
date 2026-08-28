package controlplane

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	openPilotCIRepairCountKey          = "resume.open-pilot.ci-repair.count"
	openPilotCIRepairParentRunIDKey    = "resume.open-pilot.ci-repair.parent_run_id"
	openPilotCIRepairFeedbackKey       = "resume.open-pilot.ci-repair.feedback"
	openPilotCIRepairOriginalPromptKey = "resume.open-pilot.ci-repair.original_prompt"
	openPilotCIRepairRepoKey           = "resume.open-pilot.ci-repair.repo"
	openPilotCIRepairPRIDKey           = "resume.open-pilot.ci-repair.pull_request_id"
	openPilotCIRepairPRNumberKey       = "resume.open-pilot.ci-repair.pull_request_number"
	openPilotCIRepairHeadBranchKey     = "resume.open-pilot.ci-repair.head_branch"
	openPilotCIRepairFailedHeadSHAKey  = "resume.open-pilot.ci-repair.failed_head_sha"
	openPilotCIRepairExhaustedRunIDKey = "resume.open-pilot.ci-repair.exhausted_parent_run_id"
	openPilotCIRepairMaxAttempts       = 2
	openPilotCIRepairMaxFeedbackBytes  = 4096
	openPilotCIRepairMaxPromptBytes    = 8192
)

func (s *MemoryStore) HandleOpenPilotCIFailure(request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error) {
	if err := validateOpenPilotCIRepairRequest(request); err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	artifactInput, err := openPilotCIRepairArtifactInput(request)
	if err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.openPilotIssueStates[openPilotIssueStateKey(request.Repo, request.Issue)]
	if !ok {
		return OpenPilotCIRepairResult{}, ErrNotFound
	}
	if state.RunID != request.ParentRunID {
		child, ok := s.runs[state.RunID]
		if !ok || child.Inputs[openPilotCIRepairParentRunIDKey] != request.ParentRunID {
			return OpenPilotCIRepairResult{}, ErrConflict
		}
		parent, ok := s.runs[request.ParentRunID]
		if !ok {
			return OpenPilotCIRepairResult{}, ErrNotFound
		}
		attempt, err := openPilotCIRepairCount(child.Inputs)
		if err != nil {
			return OpenPilotCIRepairResult{}, err
		}
		childCopy := cloneRunForOpenPilotRepair(child)
		return OpenPilotCIRepairResult{State: cloneOpenPilotIssueState(state), ParentRun: cloneRunForOpenPilotRepair(parent), ChildRun: &childCopy, Attempt: attempt, Queued: true, Replayed: true}, nil
	}

	parent, ok := s.runs[request.ParentRunID]
	if !ok {
		return OpenPilotCIRepairResult{}, ErrNotFound
	}
	if replay, ok := exhaustedOpenPilotCIRepairReplay(state, parent, request); ok {
		return replay, nil
	}
	now := s.now().UTC()
	if parent.Status != RunRunning || parent.ClaimedBy != request.ExpectedWorkerID || parent.AttemptCount != request.ExpectedAttemptCount || parent.LeaseExpiresAt == nil || !parent.LeaseExpiresAt.After(now) {
		return OpenPilotCIRepairResult{}, ErrConflict
	}
	count, err := openPilotCIRepairCount(state.ResumeInputs)
	if err != nil {
		return OpenPilotCIRepairResult{}, err
	}
	queued := count < openPilotCIRepairMaxAttempts
	var child Run
	var childInputs map[string]string
	if queued {
		child, childInputs, err = buildOpenPilotCIRepairChild(state, parent, request, count+1, now)
		if err != nil {
			return OpenPilotCIRepairResult{}, err
		}
	}
	summary := openPilotCIRepairSummary(count+1, queued)
	terminalizeOpenPilotCIRepairParent(&parent, now, summary)
	s.runs[parent.ID] = parent
	for id, step := range s.steps {
		if step.RunID == parent.ID && step.Status == StepRunning {
			step.Status, step.Error, step.FinishedAt = StepFailed, summary, &now
			s.steps[id] = step
			s.events[parent.ID] = append(s.events[parent.ID], RunEvent{ID: newID("evt"), RunID: parent.ID, Type: "step.failed", Message: summary, CreatedAt: now})
		}
	}
	s.events[parent.ID] = append(s.events[parent.ID], RunEvent{ID: newID("evt"), RunID: parent.ID, Type: "open-pilot.ci-repair.handled", Message: summary, CreatedAt: now}, RunEvent{ID: newID("evt"), RunID: parent.ID, Type: "run.failed", Message: summary, CreatedAt: now})
	if err := s.addOpenPilotCIRepairArtifactLocked(artifactInput, now); err != nil {
		return OpenPilotCIRepairResult{}, err
	}

	if !queued {
		setOpenPilotCIRepairDiagnostics(&state, parent.ID, request.Feedback, count, false, now)
		state.ResumeInputs[openPilotCIRepairExhaustedRunIDKey] = parent.ID
		s.openPilotIssueStates[openPilotIssueStateKey(request.Repo, request.Issue)] = state
		return OpenPilotCIRepairResult{State: cloneOpenPilotIssueState(state), ParentRun: cloneRunForOpenPilotRepair(parent), Attempt: count, Exhausted: true}, nil
	}
	s.runs[child.ID] = child
	s.events[child.ID] = append(s.events[child.ID], RunEvent{ID: newID("evt"), RunID: child.ID, Type: "run.created", Message: summary, CreatedAt: now})
	setOpenPilotCIRepairDiagnostics(&state, child.ID, request.Feedback, count+1, true, now)
	state.ResumeInputs = cloneMap(childInputs)
	s.openPilotIssueStates[openPilotIssueStateKey(request.Repo, request.Issue)] = state
	childCopy := cloneRunForOpenPilotRepair(child)
	return OpenPilotCIRepairResult{State: cloneOpenPilotIssueState(state), ParentRun: cloneRunForOpenPilotRepair(parent), ChildRun: &childCopy, Attempt: count + 1, Queued: true}, nil
}

func exhaustedOpenPilotCIRepairReplay(state OpenPilotIssueState, parent Run, request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, bool) {
	count, err := openPilotCIRepairCount(state.ResumeInputs)
	if err != nil || count != openPilotCIRepairMaxAttempts || state.ResumeInputs[openPilotCIRepairExhaustedRunIDKey] != request.ParentRunID || state.RunID != request.ParentRunID || state.Phase != OpenPilotPhaseFailed || parent.Status != RunFailed || parent.Summary != openPilotCIRepairSummary(openPilotCIRepairMaxAttempts+1, false) || parent.MaxAttempts != parent.AttemptCount {
		return OpenPilotCIRepairResult{}, false
	}
	return OpenPilotCIRepairResult{State: cloneOpenPilotIssueState(state), ParentRun: cloneRunForOpenPilotRepair(parent), Attempt: count, Exhausted: true, Replayed: true}, true
}

func openPilotCIRepairArtifactInput(request OpenPilotCIRepairRequest) (CreateArtifactInput, error) {
	payload, err := json.Marshal(struct {
		Repo              string `json:"repo"`
		Issue             string `json:"issue"`
		PullRequestID     int    `json:"pull_request_id"`
		PullRequestNumber int    `json:"pull_request_number"`
		HeadBranch        string `json:"head_branch"`
		FailedHeadSHA     string `json:"failed_head_sha"`
		Feedback          string `json:"feedback"`
	}{request.Repo, request.Issue, request.PullRequestID, request.PullRequestNumber, request.HeadBranch, request.FailedHeadSHA, boundedOpenPilotCIRepairText(request.Feedback, openPilotCIRepairMaxFeedbackBytes)})
	if err != nil {
		return CreateArtifactInput{}, err
	}
	input := CreateArtifactInput{RunID: request.ParentRunID, Name: "open-pilot-ci-checks.json", Kind: "json", URI: "data:application/json;charset=utf-8," + url.QueryEscape(string(payload))}
	if err := validateArtifactInput(input); err != nil {
		return CreateArtifactInput{}, err
	}
	return input, nil
}

func (s *MemoryStore) addOpenPilotCIRepairArtifactLocked(input CreateArtifactInput, now time.Time) error {
	metadata, err := artifactMetadata(input.URI, now)
	if err != nil {
		return err
	}
	artifact := Artifact{ID: newID("art"), RunID: input.RunID, Name: input.Name, Kind: input.Kind, URI: input.URI, StorageClass: metadata.StorageClass, ContentType: metadata.ContentType, ByteSize: metadata.ByteSize, SHA256: metadata.SHA256, RetentionUntil: metadata.RetentionUntil, CreatedAt: now}
	s.artifacts[input.RunID] = append(s.artifacts[input.RunID], artifact)
	s.artifactsCreated++
	s.artifactBytesCreated += int64(artifact.ByteSize)
	date := now.Format(time.DateOnly)
	bucket := s.artifactDaily[date]
	bucket.Date, bucket.Created, bucket.CreatedBytes = date, bucket.Created+1, bucket.CreatedBytes+int64(artifact.ByteSize)
	s.artifactDaily[date] = bucket
	s.events[input.RunID] = append(s.events[input.RunID], RunEvent{ID: newID("evt"), RunID: input.RunID, Type: "artifact.created", Message: input.Name + " (" + input.Kind + ")", CreatedAt: now})
	return nil
}

func (s *MemoryStore) ReconcileOpenPilotCIRepairTerminal(runID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return false, ErrNotFound
	}
	if run.Inputs[openPilotCIRepairParentRunIDKey] == "" || (run.Status != RunFailed && run.Status != RunTimedOut && run.Status != RunCanceled) {
		return false, nil
	}
	for key, state := range s.openPilotIssueStates {
		if state.RunID != runID || !openPilotCIRepairTerminalPhase(state.Phase) {
			continue
		}
		setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		return true, nil
	}
	return false, nil
}

func (s *MemoryStore) ReconcileOpenPilotMergeTerminal(runID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return false, ErrNotFound
	}
	if run.WorkflowID != string(WorkflowOpenPilot) || (run.Status != RunFailed && run.Status != RunTimedOut) || run.AttemptCount < run.MaxAttempts {
		return false, nil
	}
	for key, state := range s.openPilotIssueStates {
		if state.RunID != runID || state.Phase != OpenPilotPhaseMergeReady {
			continue
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		return true, nil
	}
	return false, nil
}

func (s *MemoryStore) ReconcileOpenPilotMergeTerminals(limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reconciled := 0
	for key, state := range s.openPilotIssueStates {
		if reconciled >= limit || state.Phase != OpenPilotPhaseMergeReady || state.RunID == "" {
			continue
		}
		run, ok := s.runs[state.RunID]
		if !ok || run.WorkflowID != string(WorkflowOpenPilot) || (run.Status != RunFailed && run.Status != RunTimedOut) || run.AttemptCount < run.MaxAttempts {
			continue
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		reconciled++
	}
	return reconciled, nil
}

func (s *MemoryStore) ReconcileOpenPilotCIRepairTerminals(limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reconciled := 0
	for key, state := range s.openPilotIssueStates {
		if reconciled >= limit || !openPilotCIRepairTerminalPhase(state.Phase) || state.RunID == "" {
			continue
		}
		run, ok := s.runs[state.RunID]
		if !ok || run.Inputs[openPilotCIRepairParentRunIDKey] == "" || (run.Status != RunFailed && run.Status != RunTimedOut && run.Status != RunCanceled) {
			continue
		}
		setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		reconciled++
	}
	return reconciled, nil
}

func openPilotCIRepairTerminalPhase(phase OpenPilotPhase) bool {
	switch phase {
	case OpenPilotPhaseRequested, OpenPilotPhaseValidated, OpenPilotPhaseBranchPrepared, OpenPilotPhaseAgentCompleted, OpenPilotPhaseVerified, OpenPilotPhaseCommitted, OpenPilotPhasePushed, OpenPilotPhasePRCreated, OpenPilotPhaseAIReviewed, OpenPilotPhaseCIPassed, OpenPilotPhaseMergeReady:
		return true
	default:
		return false
	}
}

func setOpenPilotCIRepairTerminalDiagnostic(state *OpenPilotIssueState, diagnostic string, now time.Time) {
	diagnostic = boundedOpenPilotCIRepairText(diagnostic, openPilotCIRepairMaxFeedbackBytes)
	state.Phase = OpenPilotPhaseFailed
	state.LastError = diagnostic
	state.OperatorSummary = "Automatic CI repair run failed: " + diagnostic
	state.RecommendedNextAction = "manual_review"
	state.UpdatedAt = now
}

func setOpenPilotMergeTerminalDiagnostic(state *OpenPilotIssueState, diagnostic string, now time.Time) {
	diagnostic = boundedOpenPilotCIRepairText(firstNonEmpty(diagnostic, state.LastError, "merge retry attempts exhausted"), openPilotCIRepairMaxFeedbackBytes)
	state.Phase = OpenPilotPhaseFailed
	state.LastError = diagnostic
	state.FailingGate = "merge"
	state.OperatorSummary = boundedOpenPilotCIRepairText(summarizeOpenPilotFailure("Merge retries exhausted", diagnostic), openPilotCIRepairMaxFeedbackBytes)
	state.RecommendedNextAction = "manual_review"
	if state.GateStatus == nil {
		state.GateStatus = OpenPilotGateStatus{}
	}
	state.GateStatus["merge"] = "failed"
	state.UpdatedAt = now
}

func validateOpenPilotCIRepairRequest(request OpenPilotCIRepairRequest) error {
	if strings.TrimSpace(request.Repo) == "" || strings.TrimSpace(request.Issue) == "" || strings.TrimSpace(request.ParentRunID) == "" || strings.TrimSpace(request.ExpectedWorkerID) == "" || request.ExpectedAttemptCount < 1 || request.PullRequestID < 1 || request.PullRequestNumber < 1 || strings.TrimSpace(request.HeadBranch) == "" || strings.TrimSpace(request.FailedHeadSHA) == "" {
		return ErrInvalid
	}
	return nil
}

func openPilotCIRepairCount(inputs map[string]string) (int, error) {
	value := strings.TrimSpace(inputs[openPilotCIRepairCountKey])
	if value == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(value)
	if err != nil || count < 0 || count > openPilotCIRepairMaxAttempts {
		return 0, ErrInvalid
	}
	return count, nil
}

func buildOpenPilotCIRepairChild(state OpenPilotIssueState, parent Run, request OpenPilotCIRepairRequest, attempt int, now time.Time) (Run, map[string]string, error) {
	if parent.WorkflowID != string(WorkflowOpenPilot) || parent.PipelineSnapshot == nil {
		return Run{}, nil, ErrInvalid
	}
	if state.Repo != request.Repo || state.PullRequestID != request.PullRequestID || state.PullRequestNum != request.PullRequestNumber || state.Branch != request.HeadBranch {
		return Run{}, nil, ErrConflict
	}
	position := 0
	for _, step := range parent.PipelineSnapshot.Steps {
		if step.Name == "open-pilot.agent-work" {
			position = step.Position
			break
		}
	}
	if position < 1 {
		return Run{}, nil, ErrInvalid
	}
	inputs := cloneMap(parent.Inputs)
	if inputs == nil {
		inputs = map[string]string{}
	}
	for key, value := range state.ResumeInputs {
		inputs[key] = value
	}
	originalPrompt := firstNonEmpty(inputs[openPilotCIRepairOriginalPromptKey], inputs["resume.open-pilot.prompt"])
	preparedPrompt := boundedOpenPilotCIRepairText(request.ChildRun.Inputs["resume.open-pilot.prompt"], openPilotCIRepairMaxPromptBytes)
	if preparedPrompt == "" {
		preparedPrompt = boundedOpenPilotCIRepairText(request.Feedback, openPilotCIRepairMaxPromptBytes)
	}
	inputs[openPilotCIRepairOriginalPromptKey] = originalPrompt
	inputs["resume.open-pilot.prompt"] = preparedPrompt
	inputs[openPilotCIRepairFeedbackKey] = boundedOpenPilotCIRepairText(request.Feedback, openPilotCIRepairMaxFeedbackBytes)
	inputs[openPilotCIRepairParentRunIDKey] = parent.ID
	inputs[openPilotCIRepairCountKey] = strconv.Itoa(attempt)
	inputs[openPilotCIRepairRepoKey] = request.Repo
	inputs[openPilotCIRepairPRIDKey] = strconv.Itoa(request.PullRequestID)
	inputs[openPilotCIRepairPRNumberKey] = strconv.Itoa(request.PullRequestNumber)
	inputs[openPilotCIRepairHeadBranchKey] = request.HeadBranch
	inputs[openPilotCIRepairFailedHeadSHAKey] = request.FailedHeadSHA
	inputs["retry_from_step"] = "open-pilot.agent-work"
	inputs["retry_from_step_position"] = strconv.Itoa(position)
	inputs["repo"], inputs["issue"] = state.Repo, state.Issue
	setOpenPilotCIRepairProtectedInputs(inputs, state, parent)

	snapshot := clonePipeline(*parent.PipelineSnapshot)
	child := Run{ID: newID("run"), WorkflowID: parent.WorkflowID, TriggerID: parent.TriggerID, Status: RunQueued, Inputs: cloneMap(inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: 1, PipelineID: snapshot.ID, PipelineVersion: pipelineVersion(snapshot), PipelineSnapshot: &snapshot}
	return child, inputs, nil
}

func setOpenPilotCIRepairProtectedInputs(inputs map[string]string, state OpenPilotIssueState, parent Run) {
	set := func(key, value string) {
		if value != "" {
			inputs[key] = value
		}
	}
	set("resume.open-pilot.branch-plan.repo", state.Repo)
	set("resume.open-pilot.branch-plan.branch", state.Branch)
	set("resume.open-pilot.branch-plan.correlation.branch", state.Branch)
	set("resume.open-pilot.branch-plan.base_branch", state.Base)
	set("resume.open-pilot.branch-plan.worktree_path", state.WorktreePath)
	set("resume.open-pilot.branch-plan.correlation.worktree_path", state.WorktreePath)
	set("resume.open-pilot.pr-plan.pull_request_url", state.PullRequestURL)
	set("resume.open-pilot.pr-plan.correlation.pull_request", state.PullRequestURL)
	if state.PullRequestID > 0 {
		inputs["resume.open-pilot.pr-plan.pull_request_id"] = strconv.Itoa(state.PullRequestID)
	}
	if state.PullRequestNum > 0 {
		inputs["resume.open-pilot.pr-plan.pull_request_number"] = strconv.Itoa(state.PullRequestNum)
	}
	for _, key := range []string{"opencode_session_id", "ocq_session_id"} {
		if value := strings.TrimSpace(state.ResumeInputs[key]); value != "" {
			inputs[key] = value
		} else if value := strings.TrimSpace(parent.Inputs[key]); value != "" {
			inputs[key] = value
		}
	}
}

func terminalizeOpenPilotCIRepairParent(parent *Run, now time.Time, summary string) {
	parent.Status, parent.FinishedAt, parent.Summary = RunFailed, &now, summary
	parent.MaxAttempts = parent.AttemptCount
	parent.ClaimedAt, parent.LeaseExpiresAt, parent.ClaimedBy = nil, nil, ""
}

func setOpenPilotCIRepairDiagnostics(state *OpenPilotIssueState, runID, feedback string, attempt int, queued bool, now time.Time) {
	state.RunID, state.LastStepName, state.UpdatedAt = runID, "open-pilot.ci-checks", now
	state.LastError = boundedOpenPilotCIRepairText(feedback, openPilotCIRepairMaxFeedbackBytes)
	state.FailingGate = "ci_checks"
	if state.GateStatus == nil {
		state.GateStatus = OpenPilotGateStatus{}
	}
	state.GateStatus["ci_checks"] = "failed"
	if queued {
		state.Phase, state.OperatorSummary, state.RecommendedNextAction = OpenPilotPhaseBranchPrepared, "Required CI checks failed; automatic repair queued ("+strconv.Itoa(attempt)+"/2)", "automatic_ci_repair"
	} else {
		state.Phase, state.OperatorSummary, state.RecommendedNextAction = OpenPilotPhaseFailed, "Required CI checks still failing; automatic repair exhausted (2/2)", "manual_review"
	}
}

func openPilotCIRepairSummary(attempt int, queued bool) string {
	if queued {
		return "Required CI checks failed; Open Pilot CI repair queued (" + strconv.Itoa(attempt) + "/2)"
	}
	return "Required CI checks failed; Open Pilot CI repair exhausted (2/2)"
}

func boundedOpenPilotCIRepairText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		value = value[:max]
	}
	return value
}

func cloneRunForOpenPilotRepair(run Run) Run {
	run.Inputs = cloneMap(run.Inputs)
	if run.PipelineSnapshot != nil {
		snapshot := clonePipeline(*run.PipelineSnapshot)
		run.PipelineSnapshot = &snapshot
	}
	return run
}

func (s *MemoryStore) OpenPilotIssueState(repo, issue string) (OpenPilotIssueState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, ok := s.openPilotIssueStates[openPilotIssueStateKey(repo, issue)]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(input.Repo, input.Issue)
	var existing *OpenPilotIssueState
	if saved, ok := s.openPilotIssueStates[key]; ok {
		existing = &saved
	}
	state, err := validateOpenPilotIssueStateForUpsert(input, existing)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	if existing == nil {
		if state.ID == "" {
			state.ID = newID("opi")
		}
		if state.CreatedAt.IsZero() {
			state.CreatedAt = now
		}
	}
	state.Repo = input.Repo
	state.Issue = input.Issue
	state.UpdatedAt = now
	state.GateStatus = cloneOpenPilotGateStatus(state.GateStatus)
	state.ResumeInputs = cloneMap(state.ResumeInputs)
	s.openPilotIssueStates[key] = state
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) TransitionOpenPilotIssueState(repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" || next == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(repo, issue)
	if existing, ok := s.openPilotIssueStates[key]; ok {
		if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
			return OpenPilotIssueState{}, err
		}
		if err := ValidateOpenPilotTransition(existing.Phase, next); err != nil {
			return OpenPilotIssueState{}, err
		}
		existing.Phase = next
		existing.UpdatedAt = now
		s.openPilotIssueStates[key] = existing
		return cloneOpenPilotIssueState(existing), nil
	}
	if err := ValidateOpenPilotTransition("", next); err != nil {
		return OpenPilotIssueState{}, err
	}
	state := OpenPilotIssueState{
		ID:                newID("opi"),
		Repo:              repo,
		Issue:             issue,
		Phase:             next,
		IssueClosed:       false,
		RepairAttempts:    0,
		MaxRepairAttempts: 2,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	s.openPilotIssueStates[key] = state
	return cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) ClaimOpenPilotIssueStateForResume(repo, issue string, resumeInputs map[string]string) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(repo, issue)
	existing, ok := s.openPilotIssueStates[key]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	if !isOpenPilotResumablePhase(existing.Phase) {
		return OpenPilotIssueState{}, ErrInvalid
	}
	if err := normalizeOpenPilotRepairCounts(&existing); err != nil {
		return OpenPilotIssueState{}, err
	}
	existing.Phase = openPilotResumeClaimPhase(existing.Phase, resumeInputs)
	existing.ResumeInputs = cloneMap(resumeInputs)
	existing.LastError = ""
	existing.FailingGate = ""
	existing.OperatorSummary = ""
	existing.RecommendedNextAction = ""
	existing.UpdatedAt = s.now().UTC()
	s.openPilotIssueStates[key] = existing
	return cloneOpenPilotIssueState(existing), nil
}

func (s *MemoryStore) ClaimAndCreateOpenPilotResumeRun(repo, issue string, input CreateRunInput) (OpenPilotIssueState, Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(repo, issue)
	state, ok := s.openPilotIssueStates[key]
	if !ok || input.WorkflowID != string(WorkflowOpenPilot) {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	prior, ok := s.runs[state.RunID]
	if !ok || prior.Status == RunQueued || prior.Status == RunRunning {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	for _, run := range s.runs {
		if run.WorkflowID == string(WorkflowOpenPilot) && run.Inputs["repo"] == repo && run.Inputs["issue"] == issue && (run.Status == RunQueued || run.Status == RunRunning) {
			return OpenPilotIssueState{}, Run{}, ErrInvalid
		}
	}
	inputs, err := safeOpenPilotResumeInputs(state)
	if err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	for name, value := range input.Inputs {
		if name != "retry_from_step" && name != "retry_from_step_position" && name != "repo" && name != "issue" {
			inputs[name] = value
		}
	}
	stripOpenPilotCIRepairInputs(inputs)
	input.Inputs = inputs
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	run := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		run.PipelineID = snapshot.ID
		run.PipelineVersion = pipelineVersion(snapshot)
		run.PipelineSnapshot = &snapshot
	}
	state.Phase = openPilotResumeClaimPhase(state.Phase, inputs)
	state.RunID = run.ID
	state.ResumeInputs = cloneMap(inputs)
	state.LastError, state.FailingGate, state.OperatorSummary, state.RecommendedNextAction = "", "", "", ""
	state.UpdatedAt = now
	s.openPilotIssueStates[key] = state
	s.runs[run.ID] = run
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "run.created", Message: "Open Pilot resume queued", CreatedAt: now})
	return cloneOpenPilotIssueState(state), run, nil
}

func stripOpenPilotCIRepairInputs(inputs map[string]string) {
	for key := range inputs {
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") && key != openPilotCIRepairCountKey {
			delete(inputs, key)
		}
	}
}

func (s *MemoryStore) ClaimAndCreateOpenPilotInitialRun(repo, issue string, input CreateRunInput) (OpenPilotIssueState, Run, error) {
	if repo == "" || issue == "" || input.WorkflowID != string(WorkflowOpenPilot) {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(repo, issue)
	if _, exists := s.openPilotIssueStates[key]; exists {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	input.Inputs = cloneMap(input.Inputs)
	input.Inputs["repo"], input.Inputs["issue"] = repo, issue
	now := s.now().UTC()
	maxAttempts := input.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	run := Run{ID: newID("run"), WorkflowID: input.WorkflowID, TriggerID: input.TriggerID, Status: RunQueued, Inputs: cloneMap(input.Inputs), CreatedAt: now, QueuedAt: now, AvailableAt: now, MaxAttempts: maxAttempts}
	if input.PipelineSnapshot != nil {
		snapshot := clonePipeline(*input.PipelineSnapshot)
		run.PipelineID = snapshot.ID
		run.PipelineVersion = pipelineVersion(snapshot)
		run.PipelineSnapshot = &snapshot
	}
	state := OpenPilotIssueState{ID: newID("opi"), Repo: repo, Issue: issue, Phase: OpenPilotPhaseRequested, RunID: run.ID, ResumeInputs: cloneMap(input.Inputs), MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now}
	s.openPilotIssueStates[key] = state
	s.runs[run.ID] = run
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "run.created", Message: "Verified Open Pilot issue queued", CreatedAt: now})
	return cloneOpenPilotIssueState(state), run, nil
}

func (s *MemoryStore) ListOpenPilotIssueStates() []OpenPilotIssueState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]OpenPilotIssueState, 0, len(s.openPilotIssueStates))
	for _, state := range s.openPilotIssueStates {
		out = append(out, cloneOpenPilotIssueState(state))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo == out[j].Repo {
			return out[i].Issue < out[j].Issue
		}
		return out[i].Repo < out[j].Repo
	})
	return out
}

func openPilotIssueStateKey(repo, issue string) string {
	return repo + "\x00" + issue
}
