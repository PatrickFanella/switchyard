package controlplane

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Evidence search scopes. Each scope selects which retained record kinds are
// searched and which fields form the match haystack.
const (
	EvidenceScopeAll          = "all"
	EvidenceScopeRuns         = "runs"
	EvidenceScopeSteps        = "steps"
	EvidenceScopeRequests     = "requests"
	EvidenceScopeErrors       = "errors"
	EvidenceScopeCorrelations = "correlations"
)

// Evidence hit kinds.
const (
	EvidenceKindRun     = "run"
	EvidenceKindStep    = "step"
	EvidenceKindRequest = "request"
)

const (
	evidenceSearchDefaultLimit    = 25
	evidenceSearchMaxLimit        = 100
	evidenceSearchMaxQueryLength  = 256
	evidenceSearchMaxFilterLength = 128
	evidenceSearchMaxTextLength   = 512
	// evidenceSearchDefaultRetention mirrors NewRetentionManager's default
	// window and only applies when no retention manager is attached.
	evidenceSearchDefaultRetention = 30 * 24 * time.Hour
)

var evidenceScopes = []string{EvidenceScopeAll, EvidenceScopeRuns, EvidenceScopeSteps, EvidenceScopeRequests, EvidenceScopeErrors, EvidenceScopeCorrelations}

// EvidenceSearchInput is a normalized, bounded search over retained runs,
// steps, and model requests. Both stores treat it identically: only records
// created at or after RetainedSince are searchable, matching is a
// case-insensitive substring test over non-payload fields, and pagination is
// a keyset over (created_at desc, kind desc, id desc).
type EvidenceSearchInput struct {
	Query         string
	Scope         string
	Status        string
	WorkflowID    string
	RunID         string
	Limit         int
	RetainedSince time.Time
	BeforeCreated *time.Time
	BeforeKind    string
	BeforeID      string
}

// EvidenceHit is the redacted projection returned by evidence search. It never
// carries run inputs, step inputs, or model request/response bodies.
type EvidenceHit struct {
	Kind           string            `json:"kind"`
	ID             string            `json:"id"`
	RunID          string            `json:"run_id,omitempty"`
	WorkflowID     string            `json:"workflow_id,omitempty"`
	Status         string            `json:"status"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary,omitempty"`
	Error          string            `json:"error,omitempty"`
	CorrelationIDs map[string]string `json:"correlation_ids,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	FinishedAt     *time.Time        `json:"finished_at,omitempty"`
}

type EvidenceSearchPage struct {
	Hits    []EvidenceHit
	HasMore bool
}

// EvidenceRetention states the explicit search bound: records created before
// Floor are outside the searchable window even if retention has not pruned
// them yet.
type EvidenceRetention struct {
	Window     string    `json:"window"`
	Floor      time.Time `json:"floor"`
	SearchedAt time.Time `json:"searched_at"`
}

func normalizeEvidenceSearchInput(input EvidenceSearchInput) (EvidenceSearchInput, error) {
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	input.WorkflowID = strings.TrimSpace(input.WorkflowID)
	input.RunID = strings.TrimSpace(input.RunID)
	if input.Scope == "" {
		input.Scope = EvidenceScopeAll
	}
	if !evidenceScopeValid(input.Scope) {
		return EvidenceSearchInput{}, fmt.Errorf("%w: scope must be one of %s", ErrInvalid, strings.Join(evidenceScopes, ", "))
	}
	if len(input.Query) > evidenceSearchMaxQueryLength {
		return EvidenceSearchInput{}, fmt.Errorf("%w: q must be at most %d characters", ErrInvalid, evidenceSearchMaxQueryLength)
	}
	for name, value := range map[string]string{"status": input.Status, "workflow": input.WorkflowID, "run_id": input.RunID} {
		if len(value) > evidenceSearchMaxFilterLength {
			return EvidenceSearchInput{}, fmt.Errorf("%w: %s must be at most %d characters", ErrInvalid, name, evidenceSearchMaxFilterLength)
		}
	}
	if input.Limit == 0 {
		input.Limit = evidenceSearchDefaultLimit
	}
	if input.Limit < 1 || input.Limit > evidenceSearchMaxLimit {
		return EvidenceSearchInput{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalid, evidenceSearchMaxLimit)
	}
	if input.RetainedSince.IsZero() {
		return EvidenceSearchInput{}, fmt.Errorf("%w: retention floor is required", ErrInvalid)
	}
	input.RetainedSince = input.RetainedSince.UTC()
	if input.BeforeCreated != nil {
		if !evidenceKindValid(input.BeforeKind) || strings.TrimSpace(input.BeforeID) == "" {
			return EvidenceSearchInput{}, fmt.Errorf("%w: invalid cursor", ErrInvalid)
		}
		created := input.BeforeCreated.UTC()
		input.BeforeCreated = &created
	}
	return input, nil
}

func evidenceScopeValid(scope string) bool {
	for _, candidate := range evidenceScopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func evidenceKindValid(kind string) bool {
	return kind == EvidenceKindRun || kind == EvidenceKindStep || kind == EvidenceKindRequest
}

// evidenceScopeKinds lists the record kinds a scope searches.
func evidenceScopeKinds(scope string) []string {
	switch scope {
	case EvidenceScopeRuns:
		return []string{EvidenceKindRun}
	case EvidenceScopeSteps:
		return []string{EvidenceKindStep}
	case EvidenceScopeRequests:
		return []string{EvidenceKindRequest}
	case EvidenceScopeCorrelations:
		return []string{EvidenceKindStep, EvidenceKindRequest}
	default:
		return []string{EvidenceKindRun, EvidenceKindStep, EvidenceKindRequest}
	}
}

// evidenceCandidate is the store-neutral matching record. Haystack holds the
// default searchable text; CorrelationHaystack holds only IDs and correlation
// values for the correlations scope. Neither includes inputs or bodies.
type evidenceCandidate struct {
	hit                 EvidenceHit
	haystack            string
	correlationHaystack string
	isError             bool
}

func evidenceCandidateFromRun(run Run) evidenceCandidate {
	return evidenceCandidate{
		hit:                 EvidenceHit{Kind: EvidenceKindRun, ID: run.ID, RunID: run.ID, WorkflowID: run.WorkflowID, Status: string(run.Status), Title: run.WorkflowID, Summary: run.Summary, CreatedAt: run.CreatedAt, FinishedAt: run.FinishedAt},
		haystack:            strings.ToLower(strings.Join([]string{run.ID, run.WorkflowID, run.TriggerID, string(run.Status), run.Summary}, " ")),
		correlationHaystack: strings.ToLower(run.ID),
		isError:             run.Status == RunFailed || run.Status == RunTimedOut,
	}
}

func evidenceCandidateFromStep(step RunStep, workflowID string) evidenceCandidate {
	correlations := sortedStringMapValues(step.CorrelationIDs)
	return evidenceCandidate{
		hit:                 EvidenceHit{Kind: EvidenceKindStep, ID: step.ID, RunID: step.RunID, WorkflowID: workflowID, Status: string(step.Status), Title: step.Name, Summary: step.OutputSummary, Error: step.Error, CorrelationIDs: cloneMap(step.CorrelationIDs), CreatedAt: step.CreatedAt, FinishedAt: step.FinishedAt},
		haystack:            strings.ToLower(strings.Join(append([]string{step.ID, step.RunID, step.Name, step.Executor, step.Model, string(step.Status), step.OutputSummary, step.Error}, correlations...), " ")),
		correlationHaystack: strings.ToLower(strings.Join(append([]string{step.ID, step.RunID}, correlations...), " ")),
		isError:             step.Status == StepFailed || step.Status == StepTimedOut || step.Error != "",
	}
}

func evidenceCandidateFromModelRequest(req ModelBrokerRequest) evidenceCandidate {
	correlations := sortedStringMapValues(req.CorrelationIDs)
	return evidenceCandidate{
		hit:                 EvidenceHit{Kind: EvidenceKindRequest, ID: req.ID, Status: req.Status, Title: req.Model, Summary: strings.TrimSpace(req.Path + " " + req.Client), Error: req.Error, CorrelationIDs: cloneMap(req.CorrelationIDs), CreatedAt: req.CreatedAt, FinishedAt: req.FinishedAt},
		haystack:            strings.ToLower(strings.Join(append([]string{req.ID, req.Model, req.Path, req.Client, req.Status, req.Error}, correlations...), " ")),
		correlationHaystack: strings.ToLower(strings.Join(append([]string{req.ID}, correlations...), " ")),
		isError:             req.Status == ModelBrokerRequestFailed || req.Error != "",
	}
}

func sortedStringMapValues(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

// evidenceCandidateMatches applies scope, query, and filters to a candidate.
// Retention and cursor bounds are applied separately by each store.
func evidenceCandidateMatches(candidate evidenceCandidate, input EvidenceSearchInput) bool {
	kindAllowed := false
	for _, kind := range evidenceScopeKinds(input.Scope) {
		if kind == candidate.hit.Kind {
			kindAllowed = true
			break
		}
	}
	if !kindAllowed {
		return false
	}
	if input.Scope == EvidenceScopeErrors && !candidate.isError {
		return false
	}
	if input.Query != "" {
		haystack := candidate.haystack
		if input.Scope == EvidenceScopeCorrelations {
			haystack = candidate.correlationHaystack
		}
		if !strings.Contains(haystack, input.Query) {
			return false
		}
	}
	if input.Status != "" && strings.ToLower(candidate.hit.Status) != input.Status {
		return false
	}
	if input.WorkflowID != "" && candidate.hit.WorkflowID != input.WorkflowID {
		return false
	}
	if input.RunID != "" {
		switch candidate.hit.Kind {
		case EvidenceKindRequest:
			if !stringMapHasValue(candidate.hit.CorrelationIDs, input.RunID) {
				return false
			}
		default:
			if candidate.hit.RunID != input.RunID {
				return false
			}
		}
	}
	return true
}

func stringMapHasValue(values map[string]string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// evidenceHitBefore reports whether left sorts after right in the
// (created_at desc, kind desc, id desc) hit order, i.e. left is "newer".
func evidenceHitNewer(left, right EvidenceHit) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.After(right.CreatedAt)
	}
	if left.Kind != right.Kind {
		return left.Kind > right.Kind
	}
	return left.ID > right.ID
}

// evidenceHitAfterCursor reports whether a hit falls strictly after the cursor
// position in the descending hit order.
func evidenceHitAfterCursor(hit EvidenceHit, input EvidenceSearchInput) bool {
	if input.BeforeCreated == nil {
		return true
	}
	cursor := EvidenceHit{CreatedAt: *input.BeforeCreated, Kind: input.BeforeKind, ID: input.BeforeID}
	return evidenceHitNewer(cursor, hit)
}

func encodeEvidenceCursor(hit EvidenceHit) string {
	return base64.RawURLEncoding.EncodeToString([]byte(hit.CreatedAt.UTC().Format(time.RFC3339Nano) + "\n" + hit.Kind + "\n" + hit.ID))
}

func decodeEvidenceCursor(cursor string) (time.Time, string, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", "", err
	}
	parts := strings.Split(string(decoded), "\n")
	if len(parts) != 3 || !evidenceKindValid(parts[1]) || strings.TrimSpace(parts[2]) == "" {
		return time.Time{}, "", "", fmt.Errorf("malformed cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", "", err
	}
	return created, parts[1], parts[2], nil
}

// redactEvidenceHit bounds and redacts every free-text field before it leaves
// the control plane. Stored values may predate persisted redaction.
func redactEvidenceHit(hit EvidenceHit) EvidenceHit {
	hit.Title = boundedEvidenceText(hit.Title)
	hit.Summary = boundedEvidenceText(hit.Summary)
	hit.Error = boundedEvidenceText(hit.Error)
	if len(hit.CorrelationIDs) > 0 {
		redacted := make(map[string]string, len(hit.CorrelationIDs))
		for key, value := range hit.CorrelationIDs {
			if sensitiveJSONKey(key) {
				redacted[key] = "[REDACTED]"
				continue
			}
			redacted[key] = boundedEvidenceText(value)
		}
		hit.CorrelationIDs = redacted
	}
	return hit
}

func boundedEvidenceText(value string) string {
	value = RedactForLog(value)
	if len(value) <= evidenceSearchMaxTextLength {
		return value
	}
	cut := evidenceSearchMaxTextLength
	for cut > 0 && !isRuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

func (a *API) now() time.Time {
	if a.clock != nil {
		return a.clock().UTC()
	}
	return time.Now().UTC()
}

func (a *API) evidenceRetentionWindow() time.Duration {
	if a.retention != nil {
		if window := a.retention.Window(); window > 0 {
			return window
		}
	}
	return evidenceSearchDefaultRetention
}

func (a *API) evidenceSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	input := EvidenceSearchInput{Query: query.Get("q"), Scope: query.Get("scope"), Status: query.Get("status"), WorkflowID: query.Get("workflow"), RunID: query.Get("run_id"), Limit: evidenceSearchDefaultLimit}
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > evidenceSearchMaxLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", evidenceSearchMaxLimit))
			return
		}
		input.Limit = parsed
	}
	if raw := strings.TrimSpace(query.Get("cursor")); raw != "" {
		created, kind, id, err := decodeEvidenceCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		input.BeforeCreated, input.BeforeKind, input.BeforeID = &created, kind, id
	}
	now := a.now()
	window := a.evidenceRetentionWindow()
	input.RetainedSince = now.Add(-window)
	normalized, err := normalizeEvidenceSearchInput(input)
	if err != nil {
		writeControlError(w, err)
		return
	}
	page, err := a.store.SearchEvidence(r.Context(), normalized)
	if errors.Is(err, ErrInvalid) {
		writeControlError(w, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evidence search unavailable")
		return
	}
	hits := make([]EvidenceHit, 0, len(page.Hits))
	for _, hit := range page.Hits {
		hits = append(hits, redactEvidenceHit(hit))
	}
	nextCursor := ""
	if page.HasMore && len(hits) > 0 {
		nextCursor = encodeEvidenceCursor(hits[len(hits)-1])
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hits":        hits,
		"next_cursor": nextCursor,
		"query":       normalized.Query,
		"scope":       normalized.Scope,
		"limit":       normalized.Limit,
		"retention":   EvidenceRetention{Window: window.String(), Floor: normalized.RetainedSince, SearchedAt: now},
	})
}
