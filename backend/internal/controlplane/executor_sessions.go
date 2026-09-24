package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The executor session view is a bounded, read-only projection of OpenCode
// executor sessions built from native contracts: run-step correlation records
// (who used which session, for which run and request) and executor status
// (whether the owning executor answers and still holds an active session). It
// never reads the compatibility adapter snapshot, never returns run inputs,
// prompts, message bodies, endpoint URLs, or credentials, and exposes no
// session mutation. Aborting a session is only possible by canceling its
// owning run through POST /api/runs/{id}/cancel.

const (
	executorSessionDefaultLimit = 50
	executorSessionMaxLimit     = 100
	// executorSessionMaxScan caps how many session-bearing step records one
	// request reads, newest first.
	executorSessionMaxScan = 500
	// executorSessionMaxRuns caps the related run steps listed per session.
	executorSessionMaxRuns = 20
	// executorSessionMaxRequests caps the related request IDs listed per session.
	executorSessionMaxRequests = 20
	// executorSessionMaxProbes caps executor presence checks per request.
	executorSessionMaxProbes   = 16
	executorSessionMaxIDLength = 256
	executorSessionProbeBody   = 1 << 20
	// ExecutorSessionDefaultKey identifies the singleton OpenCode executor that
	// serves runs without a per-repository execution bundle.
	ExecutorSessionDefaultKey = "default"
)

// Session origins.
const (
	ExecutorSessionOriginNative        = "native"
	ExecutorSessionOriginCompatibility = "compatibility"
)

// Session statuses.
const (
	ExecutorSessionStatusActive                  = "active"
	ExecutorSessionStatusStale                   = "stale"
	ExecutorSessionStatusEnded                   = "ended"
	ExecutorSessionStatusHistoricalCompatibility = "historical_compatibility"
)

// Executor presence outcomes for one session.
const (
	ExecutorSessionPresent    = "present"
	ExecutorSessionMissing    = "missing"
	ExecutorSessionUnknown    = "unknown"
	ExecutorSessionNotChecked = "not_checked"
)

// Overall executor availability.
const (
	ExecutorAvailabilityAvailable     = "available"
	ExecutorAvailabilityPartial       = "partial"
	ExecutorAvailabilityUnavailable   = "unavailable"
	ExecutorAvailabilityNotConfigured = "not_configured"
)

var (
	executorSessionIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)
	executorSessionRepoSlug   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	executorSessionRequestKey = map[string]string{
		"opencode_request_id":     "executor_request",
		"ocq_request_id":          "executor_request",
		"model_broker_request_id": "model_request",
		"llama_line_request_id":   "model_request",
		"request_id":              "model_request",
	}
)

// ExecutorSessionStepsInput bounds one scan of session-bearing run steps.
type ExecutorSessionStepsInput struct {
	RetainedSince time.Time
	SessionID     string
	Limit         int
}

// ExecutorSessionStepRecord is a run step that recorded an executor session
// correlation, joined with the minimal owning-run facts the view needs. It
// deliberately carries no run or step inputs, outputs, or summaries.
type ExecutorSessionStepRecord struct {
	StepID             string
	StepName           string
	StepPosition       int
	StepExecutor       string
	StepStatus         RunStepStatus
	CorrelationIDs     map[string]string
	StepCreatedAt      time.Time
	StepStartedAt      *time.Time
	StepFinishedAt     *time.Time
	RunID              string
	WorkflowID         string
	RunStatus          RunStatus
	RunLeaseExpiresAt  *time.Time
	RepositoryID       string
	ExecutorGeneration string
	Repo               string
}

// ExecutorSessionStepPage is one newest-first scan result. Truncated reports
// that more matching records exist beyond Limit.
type ExecutorSessionStepPage struct {
	Records   []ExecutorSessionStepRecord
	Truncated bool
}

func normalizeExecutorSessionStepsInput(input ExecutorSessionStepsInput) (ExecutorSessionStepsInput, error) {
	input.SessionID = strings.TrimSpace(input.SessionID)
	if input.SessionID != "" && !executorSessionIDPattern.MatchString(input.SessionID) {
		return ExecutorSessionStepsInput{}, fmt.Errorf("%w: session_id must match %s", ErrInvalid, executorSessionIDPattern.String())
	}
	if input.Limit < 1 || input.Limit > executorSessionMaxScan {
		return ExecutorSessionStepsInput{}, fmt.Errorf("%w: scan limit must be between 1 and %d", ErrInvalid, executorSessionMaxScan)
	}
	if input.RetainedSince.IsZero() {
		return ExecutorSessionStepsInput{}, fmt.Errorf("%w: retention floor is required", ErrInvalid)
	}
	input.RetainedSince = input.RetainedSince.UTC()
	return input, nil
}

// executorSessionCorrelation returns the session ID, the correlation key it
// came from, and its origin. Only opencode_session_id recorded by the native
// executor is native; ocq_session_id and OCQ-routed records are historical
// compatibility records.
func executorSessionCorrelation(correlation map[string]string) (string, string, string) {
	if sessionID := strings.TrimSpace(correlation["opencode_session_id"]); sessionID != "" {
		origin := ExecutorSessionOriginNative
		if strings.EqualFold(strings.TrimSpace(correlation["opencode_executor_backend"]), "ocq") {
			origin = ExecutorSessionOriginCompatibility
		}
		return sessionID, "opencode_session_id", origin
	}
	if sessionID := strings.TrimSpace(correlation["ocq_session_id"]); sessionID != "" {
		return sessionID, "ocq_session_id", ExecutorSessionOriginCompatibility
	}
	return "", "", ""
}

func executorSessionRecordMatches(correlation map[string]string, sessionID string) bool {
	opencode := strings.TrimSpace(correlation["opencode_session_id"])
	ocq := strings.TrimSpace(correlation["ocq_session_id"])
	if opencode == "" && ocq == "" {
		return false
	}
	return sessionID == "" || opencode == sessionID || ocq == sessionID
}

// ExecutorAvailability describes one OpenCode executor the view knows about.
// It carries a coarse status only: no endpoint URL and no upstream error text.
type ExecutorAvailability struct {
	Key                string    `json:"key"`
	Scope              string    `json:"scope"`
	RepositoryID       string    `json:"repository_id,omitempty"`
	ExecutorGeneration string    `json:"executor_generation,omitempty"`
	RepositorySlug     string    `json:"repository_slug,omitempty"`
	Backend            string    `json:"backend"`
	Compatibility      bool      `json:"compatibility"`
	Status             string    `json:"status"`
	Available          bool      `json:"available"`
	CheckedAt          time.Time `json:"checked_at"`
}

type ExecutorSessionRepository struct {
	RepositoryID       string `json:"repository_id,omitempty"`
	ExecutorGeneration string `json:"executor_generation,omitempty"`
	Repo               string `json:"repo,omitempty"`
}

type ExecutorSessionRun struct {
	RunID        string     `json:"run_id"`
	WorkflowID   string     `json:"workflow_id"`
	RunStatus    RunStatus  `json:"run_status"`
	StepID       string     `json:"step_id"`
	StepName     string     `json:"step_name"`
	StepPosition int        `json:"step_position"`
	StepStatus   string     `json:"step_status"`
	Active       bool       `json:"active"`
	Cancelable   bool       `json:"cancelable"`
	ObservedAt   time.Time  `json:"observed_at"`
	LeaseExpires *time.Time `json:"lease_expires_at,omitempty"`
}

type ExecutorSessionRequest struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
	RunID string `json:"run_id"`
}

type ExecutorSessionView struct {
	SessionID          string                    `json:"session_id"`
	CorrelationKey     string                    `json:"correlation_key"`
	Origin             string                    `json:"origin"`
	ExecutorKey        string                    `json:"executor_key"`
	ExecutorStatus     string                    `json:"executor_status"`
	Repository         ExecutorSessionRepository `json:"repository"`
	Status             string                    `json:"status"`
	StatusReason       string                    `json:"status_reason"`
	Active             bool                      `json:"active"`
	Presence           string                    `json:"presence"`
	PresenceReason     string                    `json:"presence_reason"`
	ExecutorActivity   string                    `json:"executor_activity,omitempty"`
	FirstObservedAt    time.Time                 `json:"first_observed_at"`
	LastObservedAt     time.Time                 `json:"last_observed_at"`
	LastObservedSource string                    `json:"last_observed_source"`
	Runs               []ExecutorSessionRun      `json:"runs"`
	RunsTruncated      bool                      `json:"runs_truncated"`
	Requests           []ExecutorSessionRequest  `json:"requests"`
	RequestsTruncated  bool                      `json:"requests_truncated"`
}

type ExecutorSessionCounts struct {
	Total                   int `json:"total"`
	Active                  int `json:"active"`
	Stale                   int `json:"stale"`
	Ended                   int `json:"ended"`
	HistoricalCompatibility int `json:"historical_compatibility"`
}

// ExecutorSessionAbortPolicy states the only supported abort path.
type ExecutorSessionAbortPolicy struct {
	DirectAbort   bool   `json:"direct_abort"`
	Route         string `json:"route"`
	AdminRequired bool   `json:"admin_required"`
	Note          string `json:"note"`
}

type ExecutorSessionsResponse struct {
	GeneratedAt          time.Time                  `json:"generated_at"`
	Retention            EvidenceRetention          `json:"retention"`
	Limit                int                        `json:"limit"`
	SessionFilter        string                     `json:"session_id,omitempty"`
	RepositoryFilter     string                     `json:"repository,omitempty"`
	ScannedSteps         int                        `json:"scanned_steps"`
	ScanLimit            int                        `json:"scan_limit"`
	Truncated            bool                       `json:"truncated"`
	ExecutorAvailability string                     `json:"executor_availability"`
	Executors            []ExecutorAvailability     `json:"executors"`
	Warnings             []string                   `json:"warnings"`
	Counts               ExecutorSessionCounts      `json:"counts"`
	Sessions             []ExecutorSessionView      `json:"sessions"`
	Abort                ExecutorSessionAbortPolicy `json:"abort"`
}

var executorSessionAbortPolicy = ExecutorSessionAbortPolicy{
	DirectAbort:   false,
	Route:         "POST /api/runs/{id}/cancel",
	AdminRequired: true,
	Note:          "Sessions cannot be aborted directly. Cancel the owning run; run cancellation aborts its active executor session through the run's recorded executor route.",
}

// ExecutorSessionProbe is the executor's answer about one session.
type ExecutorSessionProbe struct {
	Presence   string
	Reason     string
	Activity   string
	ObservedAt *time.Time
}

// ExecutorSessionProber checks whether an executor still holds a session. It
// reads only message metadata, never message content.
type ExecutorSessionProber interface {
	ProbeSession(ctx context.Context, executorKey, sessionID string) ExecutorSessionProbe
}

func executorSessionRepositoryKey(repositoryID, generation string) string {
	return repositoryID + "/" + generation
}

type executorSessionRoute struct {
	baseURL  string
	username string
	password string
	native   bool
}

type envExecutorSessionProber struct {
	routes map[string]executorSessionRoute
	err    error
	client *http.Client
}

// NewEnvExecutorSessionProber builds routes for the default executor and each
// loaded Open Pilot repository execution bundle, mirroring run cancellation.
func NewEnvExecutorSessionProber() ExecutorSessionProber {
	prober := &envExecutorSessionProber{routes: map[string]executorSessionRoute{}, client: &http.Client{Timeout: 2 * time.Second}}
	prober.routes[ExecutorSessionDefaultKey] = executorSessionRoute{
		baseURL:  strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/"),
		username: os.Getenv("OPENCODE_SERVER_USERNAME"),
		password: os.Getenv("OPENCODE_SERVER_PASSWORD"),
		native:   resolveOpenCodeExecutorBackend().Backend != "ocq",
	}
	gate, err := NewEnvOpenPilotAdmissionGate()
	if err != nil {
		prober.err = err
		return prober
	}
	bundles, err := newOpenPilotExecutionBundles(gate)
	if err != nil {
		prober.err = err
		return prober
	}
	for _, bundle := range bundles {
		executor, ok := bundle.openCodeExecutor.(*EnvOpenCodeExecutor)
		if !ok {
			continue
		}
		key := executorSessionRepositoryKey(bundle.repository.ID, bundle.repository.ExecutorGeneration)
		prober.routes[key] = executorSessionRoute{baseURL: executor.baseURL, username: executor.username, password: executor.password, native: true}
	}
	return prober
}

func (p *envExecutorSessionProber) ProbeSession(ctx context.Context, executorKey, sessionID string) ExecutorSessionProbe {
	if !executorSessionIDPattern.MatchString(sessionID) {
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "session identifier is not a valid executor session ID"}
	}
	route, ok := p.routes[executorKey]
	if !ok {
		if p.err != nil {
			return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "repository executor routing is unavailable"}
		}
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "no executor route is loaded for this repository generation"}
	}
	if !route.native {
		return ExecutorSessionProbe{Presence: ExecutorSessionNotChecked, Reason: "compatibility executor presence is not checked"}
	}
	if route.baseURL == "" {
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor is not configured"}
	}
	endpoint, err := url.JoinPath(route.baseURL, "/session/", sessionID, "/message")
	if err != nil {
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor endpoint is invalid"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?limit=1", nil)
	if err != nil {
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor request could not be built"}
	}
	if route.username != "" || route.password != "" {
		request.SetBasicAuth(firstNonEmpty(route.username, "switchyard"), route.password)
	}
	response, err := p.client.Do(request)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor did not answer in time"}
		}
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor is unreachable"}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return ExecutorSessionProbe{Presence: ExecutorSessionMissing, Reason: "executor no longer has this session"}
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return ExecutorSessionProbe{Presence: ExecutorSessionUnknown, Reason: "executor returned HTTP " + strconv.Itoa(response.StatusCode)}
	}
	return decodeExecutorSessionProbe(io.LimitReader(response.Body, executorSessionProbeBody))
}

// decodeExecutorSessionProbe reads only message metadata: role, finish,
// error name, and timestamps. Message parts are never decoded into the view.
func decodeExecutorSessionProbe(body io.Reader) ExecutorSessionProbe {
	var messages []struct {
		Info struct {
			Role   string `json:"role"`
			Finish string `json:"finish"`
			Error  *struct {
				Name string `json:"name"`
			} `json:"error"`
			Time struct {
				Created   float64 `json:"created"`
				Completed float64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
	}
	if err := json.NewDecoder(body).Decode(&messages); err != nil {
		return ExecutorSessionProbe{Presence: ExecutorSessionPresent, Reason: "executor holds this session; message metadata was unreadable"}
	}
	probe := ExecutorSessionProbe{Presence: ExecutorSessionPresent, Reason: "executor holds this session", Activity: "idle"}
	if len(messages) == 0 {
		return probe
	}
	last := messages[len(messages)-1].Info
	stamp := last.Time.Completed
	if last.Time.Created > stamp {
		stamp = last.Time.Created
	}
	if stamp > 0 {
		observed := time.UnixMilli(int64(stamp)).UTC()
		probe.ObservedAt = &observed
	}
	if last.Role != "assistant" || (last.Finish == "" && last.Error == nil && last.Time.Completed == 0) {
		probe.Activity = "busy"
	}
	return probe
}

func executorStatusName(status OpenCodeExecutorStatus) string {
	switch status.Status {
	case "healthy", "unreachable", "failed", "not_configured":
		return status.Status
	case "":
		return "unknown"
	default:
		return "failed"
	}
}

func executorAvailabilityFrom(key, scope string, status OpenCodeExecutorStatus) ExecutorAvailability {
	compatibility := strings.EqualFold(status.TransitionalBackend, "ocq") || strings.EqualFold(status.ConfiguredSelector, "ocq")
	backend := "native"
	if compatibility {
		backend = "ocq"
	}
	checked := status.CheckedAt
	return ExecutorAvailability{Key: key, Scope: scope, Backend: backend, Compatibility: compatibility, Status: executorStatusName(status), Available: status.Healthy, CheckedAt: checked}
}

type executorSessionInputs struct {
	now        time.Time
	records    []ExecutorSessionStepRecord
	defaultExe *OpenCodeExecutorStatus
	repos      *OpenPilotRepositoryStatus
	limit      int
	repository string
	staleAfter time.Duration
	prober     ExecutorSessionProber
}

type executorSessionGroup struct {
	view     ExecutorSessionView
	runs     map[string]bool
	requests map[string]bool
	hasNat   bool
}

func buildExecutorSessionsView(ctx context.Context, in executorSessionInputs) ([]ExecutorAvailability, string, []string, []ExecutorSessionView, bool) {
	executors := []ExecutorAvailability{}
	warnings := []string{}
	byKey := map[string]ExecutorAvailability{}
	if in.defaultExe != nil {
		entry := executorAvailabilityFrom(ExecutorSessionDefaultKey, "default", *in.defaultExe)
		executors = append(executors, entry)
		byKey[entry.Key] = entry
	}
	repositoryRouting := false
	if in.repos != nil {
		repositoryRouting = in.repos.Configured || in.repos.Error != ""
		if in.repos.Error != "" {
			warnings = append(warnings, "Open Pilot repository executor status is unavailable; repository sessions cannot be checked.")
		}
		for _, repository := range in.repos.Repositories {
			entry := executorAvailabilityFrom(executorSessionRepositoryKey(repository.RepositoryID, repository.ExecutorGeneration), "repository", repository.OpenCode)
			entry.RepositoryID, entry.ExecutorGeneration, entry.RepositorySlug = repository.RepositoryID, repository.ExecutorGeneration, repository.RepositorySlug
			if entry.CheckedAt.IsZero() {
				entry.CheckedAt = in.repos.CheckedAt
			}
			executors = append(executors, entry)
			byKey[entry.Key] = entry
		}
	}
	availability := overallExecutorAvailability(executors, in.repos)

	groups := map[string]*executorSessionGroup{}
	order := []string{}
	for _, record := range in.records {
		sessionID, correlationKey, origin := executorSessionCorrelation(record.CorrelationIDs)
		if sessionID == "" {
			continue
		}
		executorKey := ExecutorSessionDefaultKey
		if repositoryRouting && record.RepositoryID != "" && record.ExecutorGeneration != "" {
			executorKey = executorSessionRepositoryKey(record.RepositoryID, record.ExecutorGeneration)
		}
		groupKey := executorKey + "\x00" + sessionID
		group, ok := groups[groupKey]
		if !ok {
			repo := strings.TrimSpace(record.Repo)
			if !executorSessionRepoSlug.MatchString(repo) {
				repo = ""
			}
			group = &executorSessionGroup{view: ExecutorSessionView{
				SessionID: boundedEvidenceText(sessionID), CorrelationKey: correlationKey, Origin: origin, ExecutorKey: executorKey,
				Repository: ExecutorSessionRepository{RepositoryID: boundedEvidenceText(record.RepositoryID), ExecutorGeneration: boundedEvidenceText(record.ExecutorGeneration), Repo: repo},
				Runs:       []ExecutorSessionRun{}, Requests: []ExecutorSessionRequest{}, FirstObservedAt: record.StepCreatedAt.UTC(), LastObservedSource: "run_step",
			}, runs: map[string]bool{}, requests: map[string]bool{}}
			groups[groupKey] = group
			order = append(order, groupKey)
		}
		if origin == ExecutorSessionOriginNative {
			group.hasNat = true
		}
		observed := executorSessionRecordObservedAt(record)
		if record.StepCreatedAt.Before(group.view.FirstObservedAt) {
			group.view.FirstObservedAt = record.StepCreatedAt.UTC()
		}
		if observed.After(group.view.LastObservedAt) {
			group.view.LastObservedAt = observed
		}
		stepActive := record.StepStatus == StepRunning || record.StepStatus == StepQueued
		if stepActive {
			group.view.Active = true
		}
		runKey := record.RunID + "\x00" + record.StepID
		if !group.runs[runKey] {
			group.runs[runKey] = true
			if len(group.view.Runs) < executorSessionMaxRuns {
				var lease *time.Time
				if record.RunLeaseExpiresAt != nil {
					value := record.RunLeaseExpiresAt.UTC()
					lease = &value
				}
				group.view.Runs = append(group.view.Runs, ExecutorSessionRun{
					RunID: record.RunID, WorkflowID: record.WorkflowID, RunStatus: record.RunStatus,
					StepID: record.StepID, StepName: boundedEvidenceText(record.StepName), StepPosition: record.StepPosition, StepStatus: string(record.StepStatus),
					Active: stepActive, Cancelable: !isTerminalRunStatus(record.RunStatus), ObservedAt: observed, LeaseExpires: lease,
				})
			} else {
				group.view.RunsTruncated = true
			}
		}
		keys := make([]string, 0, len(record.CorrelationIDs))
		for key := range record.CorrelationIDs {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			kind, ok := executorSessionRequestKey[key]
			value := strings.TrimSpace(record.CorrelationIDs[key])
			if !ok || value == "" {
				continue
			}
			requestKey := key + "\x00" + value
			if group.requests[requestKey] {
				continue
			}
			group.requests[requestKey] = true
			if len(group.view.Requests) < executorSessionMaxRequests {
				group.view.Requests = append(group.view.Requests, ExecutorSessionRequest{Kind: kind, Key: key, Value: boundedEvidenceText(value), RunID: record.RunID})
			} else {
				group.view.RequestsTruncated = true
			}
		}
	}

	sessions := make([]ExecutorSessionView, 0, len(order))
	for _, key := range order {
		group := groups[key]
		if group.hasNat {
			group.view.Origin = ExecutorSessionOriginNative
			group.view.CorrelationKey = "opencode_session_id"
		}
		if in.repository != "" && !executorSessionMatchesRepository(group.view, in.repository) {
			continue
		}
		executor, known := byKey[group.view.ExecutorKey]
		switch {
		case known:
			group.view.ExecutorStatus = executor.Status
		case group.view.ExecutorKey == ExecutorSessionDefaultKey:
			group.view.ExecutorStatus = "unknown"
		default:
			group.view.ExecutorStatus = "not_loaded"
		}
		sessions = append(sessions, group.view)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		if sessions[i].Active != sessions[j].Active {
			return sessions[i].Active
		}
		if !sessions[i].LastObservedAt.Equal(sessions[j].LastObservedAt) {
			return sessions[i].LastObservedAt.After(sessions[j].LastObservedAt)
		}
		return sessions[i].SessionID < sessions[j].SessionID
	})
	truncated := false
	if len(sessions) > in.limit {
		sessions = sessions[:in.limit]
		truncated = true
	}

	probeSessions(ctx, sessions, byKey, in.prober)
	for index := range sessions {
		classifyExecutorSession(&sessions[index], in.now, in.staleAfter)
	}
	return executors, availability, warnings, sessions, truncated
}

func executorSessionMatchesRepository(view ExecutorSessionView, repository string) bool {
	if repository == ExecutorSessionDefaultKey {
		return view.ExecutorKey == ExecutorSessionDefaultKey
	}
	return view.Repository.RepositoryID == repository || view.Repository.Repo == repository || view.ExecutorKey == repository
}

func executorSessionRecordObservedAt(record ExecutorSessionStepRecord) time.Time {
	observed := record.StepCreatedAt.UTC()
	for _, candidate := range []*time.Time{record.StepStartedAt, record.StepFinishedAt} {
		if candidate != nil && candidate.After(observed) {
			observed = candidate.UTC()
		}
	}
	return observed
}

func overallExecutorAvailability(executors []ExecutorAvailability, repos *OpenPilotRepositoryStatus) string {
	configured, available := 0, 0
	for _, executor := range executors {
		if executor.Status == "not_configured" {
			continue
		}
		configured++
		if executor.Available {
			available++
		}
	}
	repositoryError := repos != nil && repos.Error != ""
	switch {
	case configured == 0 && !repositoryError:
		return ExecutorAvailabilityNotConfigured
	case available == 0:
		return ExecutorAvailabilityUnavailable
	case available < configured || repositoryError:
		return ExecutorAvailabilityPartial
	default:
		return ExecutorAvailabilityAvailable
	}
}

// probeSessions asks the owning executor about active native sessions only,
// within a fixed budget. Unavailable executors are never probed and never
// reported as healthy or empty.
func probeSessions(ctx context.Context, sessions []ExecutorSessionView, executors map[string]ExecutorAvailability, prober ExecutorSessionProber) {
	type job struct{ index int }
	jobs := []job{}
	for index := range sessions {
		session := &sessions[index]
		session.Presence, session.PresenceReason = ExecutorSessionNotChecked, "only active native sessions are checked against the executor"
		if !session.Active {
			continue
		}
		if session.Origin != ExecutorSessionOriginNative {
			session.PresenceReason = "compatibility session; executor presence is not checked"
			continue
		}
		executor, known := executors[session.ExecutorKey]
		if !known {
			session.Presence, session.PresenceReason = ExecutorSessionUnknown, "no executor is loaded for this repository generation"
			continue
		}
		if executor.Compatibility {
			session.PresenceReason = "compatibility executor presence is not checked"
			continue
		}
		if !executor.Available {
			session.Presence, session.PresenceReason = ExecutorSessionUnknown, "owning executor is "+executor.Status
			continue
		}
		if prober == nil {
			session.Presence, session.PresenceReason = ExecutorSessionUnknown, "executor presence checks are not configured"
			continue
		}
		if len(jobs) >= executorSessionMaxProbes {
			session.PresenceReason = "executor check budget for this request is exhausted"
			continue
		}
		jobs = append(jobs, job{index: index})
	}
	var wait sync.WaitGroup
	for _, item := range jobs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			session := &sessions[index]
			probe := prober.ProbeSession(ctx, session.ExecutorKey, session.SessionID)
			session.Presence, session.PresenceReason, session.ExecutorActivity = probe.Presence, probe.Reason, probe.Activity
			if probe.ObservedAt != nil && probe.ObservedAt.After(session.LastObservedAt) {
				session.LastObservedAt = probe.ObservedAt.UTC()
				session.LastObservedSource = "executor"
			}
		}(item.index)
	}
	wait.Wait()
}

func classifyExecutorSession(session *ExecutorSessionView, now time.Time, staleAfter time.Duration) {
	if !session.Active {
		if session.Origin == ExecutorSessionOriginCompatibility {
			session.Status, session.StatusReason = ExecutorSessionStatusHistoricalCompatibility, "historical compatibility record; no related step is active"
			return
		}
		session.Status, session.StatusReason = ExecutorSessionStatusEnded, "no related step is active"
		return
	}
	activeRunFinished, leaseExpired := false, false
	for _, run := range session.Runs {
		if !run.Active {
			continue
		}
		if isTerminalRunStatus(run.RunStatus) {
			activeRunFinished = true
		}
		if run.RunStatus == RunRunning && run.LeaseExpires != nil && run.LeaseExpires.Before(now) {
			leaseExpired = true
		}
	}
	switch {
	case session.Presence == ExecutorSessionMissing:
		session.Status, session.StatusReason = ExecutorSessionStatusStale, "a related step is active but the executor no longer has this session"
	case activeRunFinished:
		session.Status, session.StatusReason = ExecutorSessionStatusStale, "a related step is still marked active after its run finished"
	case leaseExpired:
		session.Status, session.StatusReason = ExecutorSessionStatusStale, "the owning run's worker lease expired"
	case session.Presence != ExecutorSessionPresent && staleAfter > 0 && now.Sub(session.LastObservedAt) > staleAfter:
		session.Status, session.StatusReason = ExecutorSessionStatusStale, "no observation within the executor message timeout ("+staleAfter.String()+")"
	default:
		session.Status, session.StatusReason = ExecutorSessionStatusActive, "a related step is running or queued"
	}
}

func (a *API) executorSessions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := executorSessionDefaultLimit
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > executorSessionMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", executorSessionMaxLimit))
			return
		}
		limit = parsed
	}
	sessionID := strings.TrimSpace(query.Get("session_id"))
	if sessionID != "" && !executorSessionIDPattern.MatchString(sessionID) {
		writeError(w, http.StatusBadRequest, "session_id must be 1-256 characters of letters, digits, '.', '_', ':', or '-'")
		return
	}
	repository := strings.TrimSpace(query.Get("repository"))
	if len(repository) > evidenceSearchMaxFilterLength {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("repository must be at most %d characters", evidenceSearchMaxFilterLength))
		return
	}
	now := a.now()
	window := a.evidenceRetentionWindow()
	input, err := normalizeExecutorSessionStepsInput(ExecutorSessionStepsInput{RetainedSince: now.Add(-window), SessionID: sessionID, Limit: executorSessionMaxScan})
	if err != nil {
		writeControlError(w, err)
		return
	}
	page, err := a.store.ListExecutorSessionSteps(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "executor session records unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	var defaultStatus *OpenCodeExecutorStatus
	var repositoryStatus *OpenPilotRepositoryStatus
	var probes sync.WaitGroup
	if a.openCode != nil {
		probes.Add(1)
		go func() {
			defer probes.Done()
			status := a.openCode.Status(ctx)
			defaultStatus = &status
		}()
	}
	if a.openPilotRepositories != nil {
		probes.Add(1)
		go func() {
			defer probes.Done()
			status := a.openPilotRepositories.Status(ctx)
			repositoryStatus = &status
		}()
	}
	probes.Wait()

	executors, availability, warnings, sessions, truncated := buildExecutorSessionsView(ctx, executorSessionInputs{
		now: now, records: page.Records, defaultExe: defaultStatus, repos: repositoryStatus,
		limit: limit, repository: repository, staleAfter: openCodeMessageTimeout(), prober: a.executorSessionProber,
	})
	if page.Truncated {
		warnings = append(warnings, fmt.Sprintf("Only the newest %d session-bearing step records were read; older sessions are omitted.", executorSessionMaxScan))
	}
	if availability != ExecutorAvailabilityAvailable && len(sessions) == 0 {
		warnings = append(warnings, "No sessions are listed and executor availability is "+availability+"; an empty list does not mean the executor is idle or healthy.")
	}
	counts := ExecutorSessionCounts{Total: len(sessions)}
	for _, session := range sessions {
		switch session.Status {
		case ExecutorSessionStatusActive:
			counts.Active++
		case ExecutorSessionStatusStale:
			counts.Stale++
		case ExecutorSessionStatusEnded:
			counts.Ended++
		case ExecutorSessionStatusHistoricalCompatibility:
			counts.HistoricalCompatibility++
		}
	}
	writeJSON(w, http.StatusOK, ExecutorSessionsResponse{
		GeneratedAt: now, Retention: EvidenceRetention{Window: window.String(), Floor: input.RetainedSince, SearchedAt: now},
		Limit: limit, SessionFilter: sessionID, RepositoryFilter: repository,
		ScannedSteps: len(page.Records), ScanLimit: executorSessionMaxScan, Truncated: truncated || page.Truncated,
		ExecutorAvailability: availability, Executors: executors, Warnings: warnings, Counts: counts, Sessions: sessions,
		Abort: executorSessionAbortPolicy,
	})
}
