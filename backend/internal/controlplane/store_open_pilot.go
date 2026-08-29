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
	openPilotCIRepairBaseRepoKey       = "resume.open-pilot.ci-repair.base_repo"
	openPilotCIRepairBaseBranchKey     = "resume.open-pilot.ci-repair.base_branch"
	openPilotCIRepairExhaustedRunIDKey = "resume.open-pilot.ci-repair.exhausted_parent_run_id"
	openPilotCIRepairMaxAttempts       = 2
	openPilotCIRepairMaxFeedbackBytes  = 4096
	openPilotCIRepairMaxPromptBytes    = 8192
)

func (s *MemoryStore) HandleOpenPilotCIFailure(request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error) {
	return s.handleOpenPilotCIFailure(RunAttemptFence{}, false, nil, request)
}

func (s *MemoryStore) handleOpenPilotCIFailure(fence RunAttemptFence, present bool, fenceErr error, request OpenPilotCIRepairRequest) (OpenPilotCIRepairResult, error) {
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
		if present {
			return OpenPilotCIRepairResult{}, ErrStaleRunAttempt
		}
		return OpenPilotCIRepairResult{}, ErrNotFound
	}
	if present && (fenceErr != nil || state.RunID != fence.RunID || request.ParentRunID != fence.RunID || request.ExpectedWorkerID != fence.WorkerID || request.ExpectedAttemptCount != fence.Attempt) {
		return OpenPilotCIRepairResult{}, ErrStaleRunAttempt
	}
	if err := s.validateRunAttemptFenceLocked(fence, present, fenceErr, request.ParentRunID); err != nil {
		return OpenPilotCIRepairResult{}, err
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
		if err := validateOpenPilotRunStateTarget(child, state, request.Target); err != nil {
			return OpenPilotCIRepairResult{}, err
		}
		if err := validateOpenPilotRunAffinityTarget(parent, state, request.Target); err != nil {
			return OpenPilotCIRepairResult{}, err
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
	if err := validateOpenPilotRunStateTarget(parent, state, request.Target); err != nil {
		return OpenPilotCIRepairResult{}, err
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

func (s *MemoryStore) ReconcileOpenPilotCIRepairTerminal(runID string, gates ...*OpenPilotAdmissionGate) (bool, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil {
		return false, ErrInvalid
	}
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
		if _, err := gate.ValidateRunState(run, state); err != nil {
			return false, err
		}
		setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		return true, nil
	}
	return false, nil
}

func (s *MemoryStore) ReconcileOpenPilotMergeTerminal(runID string, gates ...*OpenPilotAdmissionGate) (bool, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil {
		return false, ErrInvalid
	}
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
		if _, err := gate.ValidateRunState(run, state); err != nil {
			return false, err
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		return true, nil
	}
	return false, nil
}

func (s *MemoryStore) ReconcileOpenPilotMergeTerminals(limit int, gates ...*OpenPilotAdmissionGate) (int, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil || limit < 1 || limit > 100 {
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
		if _, err := gate.ValidateRunState(run, state); err != nil {
			continue
		}
		setOpenPilotMergeTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		reconciled++
	}
	return reconciled, nil
}

func (s *MemoryStore) ReconcileOpenPilotCIRepairTerminals(limit int, gates ...*OpenPilotAdmissionGate) (int, error) {
	gate := firstOpenPilotGate(gates)
	if gate == nil || limit < 1 || limit > 100 {
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
		if _, err := gate.ValidateRunState(run, state); err != nil {
			continue
		}
		setOpenPilotCIRepairTerminalDiagnostic(&state, run.Summary, s.now().UTC())
		s.openPilotIssueStates[key] = state
		reconciled++
	}
	return reconciled, nil
}

func firstOpenPilotGate(gates []*OpenPilotAdmissionGate) *OpenPilotAdmissionGate {
	if len(gates) == 0 {
		return nil
	}
	return gates[0]
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
	// A CI repair adds a new commit on top of the failed head. The prior
	// attempt's intent remains useful audit evidence on that run, but it cannot
	// reconcile the repair diff. Start a fresh durable intent for this child;
	// retries of the child will persist and reuse the replacement intent.
	for _, field := range []string{"parent", "tree", "message", "status_before", "diffstat", "name_status"} {
		delete(inputs, openPilotCommitIntentPrefix+field)
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
	inputs[openPilotCIRepairBaseRepoKey] = state.Repo
	inputs[openPilotCIRepairBaseBranchKey] = state.Base
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

func (s *MemoryStore) OpenPilotIssueStateForTarget(target OpenPilotTarget) (OpenPilotIssueState, error) {
	if err := validateOpenPilotTarget(target); err != nil {
		return OpenPilotIssueState{}, err
	}
	candidates := openPilotTargetCandidates(target)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matched OpenPilotIssueState
	matches := 0
	for _, state := range s.openPilotIssueStates {
		if state.CanonicalIssue != target.Issue || (!containsOpenPilotCandidate(candidates, foldOpenPilotRepositoryName(state.CanonicalRepo)) && !openPilotCandidatesOverlap(candidates, s.openPilotReservations[state.ID])) {
			continue
		}
		matched = state
		matches++
		if matches > 1 {
			return OpenPilotIssueState{}, ErrConflict
		}
	}
	if matches == 0 {
		return OpenPilotIssueState{}, ErrNotFound
	}
	return cloneOpenPilotIssueState(matched), nil
}

func (s *MemoryStore) BindOpenPilotIssueStateAffinity(repo, issue string, target OpenPilotTarget) (OpenPilotIssueState, error) {
	if err := validateOpenPilotTarget(target); err != nil {
		return OpenPilotIssueState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := openPilotIssueStateKey(repo, issue)
	state, ok := s.openPilotIssueStates[key]
	if !ok {
		return OpenPilotIssueState{}, ErrNotFound
	}
	if err := validateOpenPilotAffinityBindingLocked(s.openPilotIssueStates, s.openPilotReservations, state, target); err != nil {
		return OpenPilotIssueState{}, err
	}
	if state.RepositoryID != "" {
		s.openPilotReservations[state.ID] = unionOpenPilotCandidates(s.openPilotReservations[state.ID], openPilotTargetCandidates(target))
		return cloneOpenPilotIssueState(state), nil
	}
	state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
	state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
	state.UpdatedAt = s.now().UTC()
	s.openPilotIssueStates[key] = state
	s.openPilotReservations[state.ID] = unionOpenPilotCandidates(s.openPilotReservations[state.ID], openPilotTargetCandidates(target))
	return cloneOpenPilotIssueState(state), nil
}

const openPilotAffinityAdoptionDiagnostic = "Open Pilot repository affinity could not be adopted safely"

func (s *MemoryStore) AdoptOpenPilotRunAffinity(runID, expectedWorkerID string, expectedAttemptCount int, gate *OpenPilotAdmissionGate) (Run, OpenPilotIssueState, error) {
	if runID == "" || expectedWorkerID == "" || expectedAttemptCount < 1 {
		return Run{}, OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	run, ok := s.runs[runID]
	if !ok {
		return Run{}, OpenPilotIssueState{}, ErrNotFound
	}
	if run.Status != RunRunning || run.ClaimedBy != expectedWorkerID || run.AttemptCount != expectedAttemptCount || run.LeaseExpiresAt == nil || !run.LeaseExpiresAt.After(now) {
		return Run{}, OpenPilotIssueState{}, ErrConflict
	}
	return s.adoptOpenPilotRunAffinityLocked(runID, gate, now)
}

func (s *MemoryStore) adoptOpenPilotRunAffinityLocked(runID string, gate *OpenPilotAdmissionGate, now time.Time) (Run, OpenPilotIssueState, error) {
	run, ok := s.runs[runID]
	if !ok {
		return Run{}, OpenPilotIssueState{}, ErrNotFound
	}
	if run.WorkflowID != string(WorkflowOpenPilot) {
		return run, OpenPilotIssueState{}, nil
	}
	if run.Status != RunQueued && run.Status != RunRunning {
		return Run{}, OpenPilotIssueState{}, ErrConflict
	}
	key := openPilotIssueStateKey(run.Inputs["repo"], run.Inputs["issue"])
	state, stateFound := s.openPilotIssueStates[key]
	unsafe := gate == nil || !stateFound || state.RunID != run.ID || state.Repo != run.Inputs["repo"] || state.Issue != run.Inputs["issue"]
	var target OpenPilotTarget
	if !unsafe {
		var err error
		target, err = gate.Resolve(state.Repo, state.Issue)
		unsafe = err != nil
	}
	stateNull := state.RepositoryID == "" && state.ExecutorGeneration == ""
	runNull := run.Inputs[openPilotRepositoryIDInputKey] == "" && run.Inputs[openPilotExecutorGenerationInputKey] == ""
	stateExact := !stateNull && state.RepositoryID == target.RepositoryID && state.ExecutorGeneration == target.ExecutorGeneration && state.CanonicalRepo == target.RepositorySlug && state.CanonicalIssue == target.Issue
	runExact := !runNull && run.Inputs[openPilotRepositoryIDInputKey] == target.RepositoryID && run.Inputs[openPilotExecutorGenerationInputKey] == target.ExecutorGeneration
	if !unsafe && ((stateExact && runExact) || (stateNull && runNull)) {
		unsafe = validateOpenPilotAffinityBindingLocked(s.openPilotIssueStates, s.openPilotReservations, state, target) != nil
	}
	canonicalKey := openPilotIssueStateKey(target.RepositorySlug, target.Issue)
	if existing, found := s.openPilotIssueStates[canonicalKey]; !unsafe && found && existing.ID != state.ID {
		unsafe = true
	}
	if unsafe || (!stateExact && !stateNull) || (!runExact && !runNull) {
		return s.quarantineOpenPilotRunAffinityLocked(run, state, stateFound, now)
	}
	legacyKey := key
	if state.LegacyRepo == "" {
		state.LegacyRepo = state.Repo
	}
	if state.LegacyIssue == "" {
		state.LegacyIssue = state.Issue
	}
	state.Repo, state.Issue = target.RepositorySlug, target.Issue
	state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
	state.CanonicalRepo, state.CanonicalIssue, state.UpdatedAt = target.RepositorySlug, target.Issue, now
	if legacyKey != canonicalKey {
		delete(s.openPilotIssueStates, legacyKey)
	}
	s.openPilotIssueStates[canonicalKey] = state
	s.openPilotReservations[state.ID] = unionOpenPilotCandidates(s.openPilotReservations[state.ID], openPilotTargetCandidates(target))
	run.Inputs = cloneMap(run.Inputs)
	run.Inputs["repo"], run.Inputs["issue"] = target.RepositorySlug, target.Issue
	setOpenPilotAffinityInputs(run.Inputs, target)
	s.runs[run.ID] = run
	return run, cloneOpenPilotIssueState(state), nil
}

func (s *MemoryStore) quarantineOpenPilotRunAffinityLocked(run Run, state OpenPilotIssueState, stateFound bool, now time.Time) (Run, OpenPilotIssueState, error) {
	run.Status, run.Summary, run.FinishedAt = RunPaused, openPilotAffinityAdoptionDiagnostic, &now
	run.ClaimedAt, run.LeaseExpiresAt, run.ClaimedBy = nil, nil, ""
	s.runs[run.ID] = run
	if stateFound {
		state.Phase, state.LastError, state.FailingGate = OpenPilotPhasePaused, openPilotAffinityAdoptionDiagnostic, "repository_affinity"
		state.OperatorSummary, state.RecommendedNextAction, state.UpdatedAt = openPilotAffinityAdoptionDiagnostic, "manual_review", now
		s.openPilotIssueStates[openPilotIssueStateKey(state.Repo, state.Issue)] = state
	}
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "run.paused", Message: openPilotAffinityAdoptionDiagnostic, CreatedAt: now})
	return run, cloneOpenPilotIssueState(state), ErrConflict
}

func (s *MemoryStore) quarantineDuplicateOpenPilotRunStatesLocked(run Run, now time.Time) {
	run.Status, run.Summary, run.FinishedAt = RunPaused, openPilotAffinityAdoptionDiagnostic, &now
	run.ClaimedAt, run.LeaseExpiresAt, run.ClaimedBy = nil, nil, ""
	s.runs[run.ID] = run
	for key, state := range s.openPilotIssueStates {
		if state.RunID != run.ID {
			continue
		}
		state.Phase, state.LastError, state.FailingGate = OpenPilotPhasePaused, openPilotAffinityAdoptionDiagnostic, "repository_affinity"
		state.OperatorSummary, state.RecommendedNextAction, state.UpdatedAt = openPilotAffinityAdoptionDiagnostic, "manual_review", now
		s.openPilotIssueStates[key] = state
	}
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "run.paused", Message: openPilotAffinityAdoptionDiagnostic, CreatedAt: now})
}

func validateOpenPilotAffinityBindingLocked(states map[string]OpenPilotIssueState, reservations map[string][]string, state OpenPilotIssueState, target OpenPilotTarget) error {
	if err := validateOpenPilotTarget(target); err != nil {
		return err
	}
	if state.RepositoryID != "" && (state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue) {
		return ErrConflict
	}
	targetCandidates := openPilotTargetCandidates(target)
	for _, candidate := range states {
		if candidate.ID != state.ID && candidate.CanonicalIssue == target.Issue && (containsOpenPilotCandidate(targetCandidates, foldOpenPilotRepositoryName(candidate.CanonicalRepo)) || openPilotCandidatesOverlap(targetCandidates, reservations[candidate.ID])) {
			return ErrConflict
		}
	}
	return nil
}

func (s *MemoryStore) UpsertOpenPilotIssueState(input OpenPilotIssueState) (OpenPilotIssueState, error) {
	return s.upsertOpenPilotIssueState(RunAttemptFence{}, false, nil, input)
}

func (s *MemoryStore) upsertOpenPilotIssueState(fence RunAttemptFence, present bool, fenceErr error, input OpenPilotIssueState) (OpenPilotIssueState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(input.Repo, input.Issue)
	var existing *OpenPilotIssueState
	if saved, ok := s.openPilotIssueStates[key]; ok {
		existing = &saved
	}
	if present {
		if existing == nil || fenceErr != nil || existing.RunID != fence.RunID || input.RunID != fence.RunID {
			return OpenPilotIssueState{}, ErrStaleRunAttempt
		}
		if err := s.validateRunAttemptFenceLocked(fence, true, nil, fence.RunID); err != nil {
			return OpenPilotIssueState{}, err
		}
	}
	state, err := validateOpenPilotIssueStateForUpsert(input, existing)
	if err != nil {
		return OpenPilotIssueState{}, err
	}
	if existing == nil {
		if state.ID == "" {
			state.ID = newID("opi")
		}
		for savedKey, saved := range s.openPilotIssueStates {
			if savedKey != key && saved.ID == state.ID {
				return OpenPilotIssueState{}, ErrConflict
			}
		}
		if state.CreatedAt.IsZero() {
			state.CreatedAt = now
		}
	}
	if state.RepositoryID == "" && memoryOpenPilotReservationCollision(s.openPilotIssueStates, s.openPilotReservations, state.ID, state.CanonicalRepo, state.CanonicalIssue) {
		return OpenPilotIssueState{}, ErrConflict
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
	return s.transitionOpenPilotIssueState(RunAttemptFence{}, false, nil, repo, issue, next)
}

func (s *MemoryStore) transitionOpenPilotIssueState(fence RunAttemptFence, present bool, fenceErr error, repo, issue string, next OpenPilotPhase) (OpenPilotIssueState, error) {
	if repo == "" || issue == "" || next == "" {
		return OpenPilotIssueState{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	key := openPilotIssueStateKey(repo, issue)
	if existing, ok := s.openPilotIssueStates[key]; ok {
		if present {
			if fenceErr != nil || existing.RunID != fence.RunID {
				return OpenPilotIssueState{}, ErrStaleRunAttempt
			}
			if err := s.validateRunAttemptFenceLocked(fence, true, nil, fence.RunID); err != nil {
				return OpenPilotIssueState{}, err
			}
		}
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
	if present {
		return OpenPilotIssueState{}, ErrStaleRunAttempt
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
	state.LegacyRepo, state.LegacyIssue = repo, issue
	populateOpenPilotCanonicalShadow(&state)
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
	if target := input.OpenPilotTarget; target != nil {
		if state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue {
			return OpenPilotIssueState{}, Run{}, ErrConflict
		}
		if err := validateOpenPilotAffinityBindingLocked(s.openPilotIssueStates, s.openPilotReservations, state, *target); err != nil {
			return OpenPilotIssueState{}, Run{}, err
		}
		state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
		state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
		s.openPilotReservations[state.ID] = unionOpenPilotCandidates(s.openPilotReservations[state.ID], openPilotTargetCandidates(*target))
	} else {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	prior, ok := s.runs[state.RunID]
	if !ok || prior.Status == RunQueued || prior.Status == RunRunning {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	if err := validateOpenPilotRunStateTarget(prior, state, *input.OpenPilotTarget); err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	for _, run := range s.runs {
		if run.WorkflowID == string(WorkflowOpenPilot) && run.Inputs[openPilotRepositoryIDInputKey] == input.OpenPilotTarget.RepositoryID && openPilotRunIssueMatchesTarget(run, *input.OpenPilotTarget) && (run.Status == RunQueued || run.Status == RunRunning) {
			return OpenPilotIssueState{}, Run{}, ErrInvalid
		}
	}
	inputs, err := safeOpenPilotResumeInputs(state, input.PipelineSnapshot)
	if err != nil {
		return OpenPilotIssueState{}, Run{}, err
	}
	for name, value := range input.Inputs {
		if name != "retry_from_step" && name != "retry_from_step_position" && name != "repo" && name != "issue" && !strings.HasPrefix(name, "resume.open-pilot.ci-repair.") {
			inputs[name] = value
		}
	}
	stripOpenPilotCIRepairInputs(inputs)
	inputs["repo"], inputs["issue"] = input.OpenPilotTarget.RepositorySlug, input.OpenPilotTarget.Issue
	setOpenPilotAffinityInputs(inputs, *input.OpenPilotTarget)
	inputs[openPilotLifecycleResumeInputKey] = "true"
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

func openPilotRunIssueMatchesTarget(run Run, target OpenPilotTarget) bool {
	issue, err := ParseOpenPilotIssue(run.Inputs["issue"])
	return err == nil && issue == target.Issue
}

func stripOpenPilotCIRepairInputs(inputs map[string]string) {
	for key := range inputs {
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") && !openPilotCIRepairExternalResumeInputAllowed(key) {
			delete(inputs, key)
		}
	}
}

func removeOpenPilotCIRepairInputs(inputs map[string]string) {
	for key := range inputs {
		if strings.HasPrefix(key, "resume.open-pilot.ci-repair.") {
			delete(inputs, key)
		}
	}
}

func openPilotCIRepairExternalResumeInputAllowed(key string) bool {
	switch key {
	case openPilotCIRepairCountKey, openPilotCIRepairFeedbackKey, openPilotCIRepairOriginalPromptKey:
		return true
	default:
		return false
	}
}

func (s *MemoryStore) ClaimAndCreateOpenPilotInitialRun(repo, issue string, input CreateRunInput) (OpenPilotIssueState, Run, error) {
	if repo == "" || issue == "" || input.WorkflowID != string(WorkflowOpenPilot) {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if target := input.OpenPilotTarget; target != nil {
		if err := validateOpenPilotTarget(*target); err != nil {
			return OpenPilotIssueState{}, Run{}, err
		}
		canonicalRepo, repoErr := ParseOpenPilotRepositorySlug(repo)
		canonicalIssue, issueErr := ParseOpenPilotIssue(issue)
		if repoErr != nil || issueErr != nil || !strings.EqualFold(target.RepositorySlug, canonicalRepo) || target.Issue != canonicalIssue {
			return OpenPilotIssueState{}, Run{}, ErrInvalid
		}
		for _, existing := range s.openPilotIssueStates {
			if existing.CanonicalIssue == target.Issue && (containsOpenPilotCandidate(openPilotTargetCandidates(*target), foldOpenPilotRepositoryName(existing.CanonicalRepo)) || openPilotCandidatesOverlap(openPilotTargetCandidates(*target), s.openPilotReservations[existing.ID])) {
				return OpenPilotIssueState{}, Run{}, ErrConflict
			}
		}
	}
	if input.OpenPilotTarget == nil {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	key := openPilotIssueStateKey(repo, issue)
	if _, exists := s.openPilotIssueStates[key]; exists {
		return OpenPilotIssueState{}, Run{}, ErrInvalid
	}
	input.Inputs = cloneMap(input.Inputs)
	if input.Inputs == nil {
		input.Inputs = map[string]string{}
	}
	input.Inputs["repo"], input.Inputs["issue"] = repo, issue
	setOpenPilotAffinityInputs(input.Inputs, *input.OpenPilotTarget)
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
	state := OpenPilotIssueState{ID: newID("opi"), Repo: repo, Issue: issue, Phase: OpenPilotPhaseRequested, RunID: run.ID, ResumeInputs: cloneMap(input.Inputs), MaxRepairAttempts: 2, CreatedAt: now, UpdatedAt: now, LegacyRepo: repo, LegacyIssue: issue}
	if target := input.OpenPilotTarget; target != nil {
		state.RepositoryID, state.ExecutorGeneration = target.RepositoryID, target.ExecutorGeneration
		state.CanonicalRepo, state.CanonicalIssue = target.RepositorySlug, target.Issue
		s.openPilotReservations[state.ID] = openPilotTargetCandidates(*target)
	} else {
		populateOpenPilotCanonicalShadow(&state)
	}
	s.openPilotIssueStates[key] = state
	s.runs[run.ID] = run
	s.events[run.ID] = append(s.events[run.ID], RunEvent{ID: newID("evt"), RunID: run.ID, Type: "run.created", Message: "Verified Open Pilot issue queued", CreatedAt: now})
	return cloneOpenPilotIssueState(state), run, nil
}

func openPilotCandidatesOverlap(left, right []string) bool {
	for _, candidate := range left {
		if containsOpenPilotCandidate(right, candidate) {
			return true
		}
	}
	return false
}

func memoryOpenPilotReservationCollision(states map[string]OpenPilotIssueState, reservations map[string][]string, excludeID, repo, issue string) bool {
	if repo == "" || issue == "" {
		return false
	}
	folded := foldOpenPilotRepositoryName(repo)
	for _, state := range states {
		if state.ID != excludeID && state.RepositoryID != "" && state.CanonicalIssue == issue && containsOpenPilotCandidate(reservations[state.ID], folded) {
			return true
		}
	}
	return false
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

func (s *MemoryStore) ListOpenPilotIssueStatesForAudit(limit int) ([]OpenPilotIssueState, error) {
	if limit < 1 || limit > maxOpenPilotAuditStates+1 {
		return nil, ErrInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	states := make([]OpenPilotIssueState, 0, limit)
	for _, state := range s.openPilotIssueStates {
		index := sort.Search(len(states), func(i int) bool {
			if states[i].Repo != state.Repo {
				return states[i].Repo >= state.Repo
			}
			if states[i].Issue != state.Issue {
				return states[i].Issue >= state.Issue
			}
			return states[i].ID >= state.ID
		})
		if index >= limit {
			continue
		}
		states = append(states, OpenPilotIssueState{})
		copy(states[index+1:], states[index:])
		states[index] = cloneOpenPilotIssueState(state)
		if len(states) > limit {
			states = states[:limit]
		}
	}
	return states, nil
}

func openPilotIssueStateKey(repo, issue string) string {
	return repo + "\x00" + issue
}
