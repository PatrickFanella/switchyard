package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Source collection.
//
// A pipeline step with executor "source" collects bounded, read-only source
// material through a named adapter and publishes it as named step outputs so a
// later drafting step sees the material explicitly. Collection is opt-in: the
// run must name its sources, and every source must also be allowlisted by the
// host operator. The adapter never writes to the source system.
//
// Results are deterministic for the same upstream data: sources are
// deduplicated and sorted, the collection window is anchored to the run's
// creation time (or an explicit end), items are deduplicated and ordered by a
// stable key, and every limit that drops material is recorded.

const (
	SourceAdapterGiteaActivity = "gitea-activity"

	sourceAdapterVersion           = 1
	sourceEvidenceSchema           = "switchyard.source-evidence.v1"
	sourceRepositoriesAllowlistEnv = "SWITCHYARD_SOURCE_REPOSITORIES"

	defaultSourceWindow         = 24 * time.Hour
	minSourceWindow             = time.Hour
	maxSourceWindow             = 30 * 24 * time.Hour
	maxSourceRepositories       = 10
	defaultSourceMaxItems       = 50
	maxSourceMaxItems           = 100
	defaultSourceExcerptChars   = 600
	maxSourceExcerptChars       = 2000
	maxSourceTitleChars         = 200
	sourceMaxPagesPerRepository = 5

	SourceStatusComplete = "complete"
	SourceStatusPartial  = "partial"
	SourceStatusEmpty    = "empty"

	sourceCollectedEvent   = "source.collected"
	sourceFailedEvent      = "source.collection.failed"
	sourceUnavailableEvent = "source.collection.unavailable"

	sourceContentOpenTag  = "<collected-sources>"
	sourceContentCloseTag = "</collected-sources>"
)

var (
	sourceRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	unresolvedRunInputValue = regexp.MustCompile(`^\{\{\s*[A-Za-z0-9_.-]+\s*\}\}$`)
)

// SourceCollectionRequest is the validated, normalized form of a source step's
// inputs.
type SourceCollectionRequest struct {
	Adapter      string
	Sources      []string
	WindowStart  time.Time
	WindowEnd    time.Time
	MaxItems     int
	ExcerptChars int
}

type sourceEvidenceWindow struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Duration string `json:"duration"`
}

type sourceEvidenceLimits struct {
	MaxItems           int `json:"max_items"`
	MaxExcerptChars    int `json:"max_excerpt_chars"`
	MaxPagesPerSource  int `json:"max_pages_per_source"`
	ContentBudgetBytes int `json:"content_budget_bytes"`
}

type sourceEvidenceSource struct {
	Source        string `json:"source"`
	Status        string `json:"status"`
	ReturnedItems int    `json:"returned_items"`
	Truncated     bool   `json:"truncated,omitempty"`
	Error         string `json:"error,omitempty"`
	Retryable     bool   `json:"retryable,omitempty"`
}

// SourceEvidenceItem is one collected item as recorded in the evidence
// artifact. ID is the citation handle (S1, S2, ...) the drafting step uses.
type SourceEvidenceItem struct {
	ID         string   `json:"id"`
	Source     string   `json:"source"`
	Kind       string   `json:"kind"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	State      string   `json:"state"`
	URL        string   `json:"url"`
	Author     string   `json:"author,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at"`
	ClosedAt   string   `json:"closed_at,omitempty"`
	Excerpt    string   `json:"excerpt"`
	BodyBytes  int      `json:"body_bytes"`
	BodySHA256 string   `json:"body_sha256"`
}

type sourceEvidenceOmission struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

// SourceEvidence is the attached evidence record for one collection.
type SourceEvidence struct {
	Schema            string                   `json:"schema"`
	Adapter           string                   `json:"adapter"`
	AdapterVersion    int                      `json:"adapter_version"`
	Window            sourceEvidenceWindow     `json:"window"`
	Limits            sourceEvidenceLimits     `json:"limits"`
	Status            string                   `json:"status"`
	ItemCount         int                      `json:"item_count"`
	SourceCount       int                      `json:"source_count"`
	FailedSourceCount int                      `json:"failed_source_count"`
	OmittedItemCount  int                      `json:"omitted_item_count"`
	Sources           []sourceEvidenceSource   `json:"sources"`
	Items             []SourceEvidenceItem     `json:"items"`
	OmittedItems      []sourceEvidenceOmission `json:"omitted_items"`
}

type sourceCandidate struct {
	source string
	item   GiteaActivityItem
}

func (c sourceCandidate) ref() string {
	return c.source + "#" + strconv.Itoa(c.item.Number)
}

// sourceInputValue treats an empty value or an unrendered run-input template
// such as "{{source_repositories}}" as unset, so optional run inputs stay
// optional.
func sourceInputValue(value string) string {
	value = strings.TrimSpace(value)
	if unresolvedRunInputValue.MatchString(value) {
		return ""
	}
	return value
}

func invalidSourceRequest(stepID, format string, args ...any) error {
	return sourceCollectionFailure{reason: fmt.Sprintf("source step %q: ", stepID) + fmt.Sprintf(format, args...), retryable: false}
}

func parseSourceCollectionRequest(run Run, step PipelineStep, inputs map[string]string) (SourceCollectionRequest, error) {
	request := SourceCollectionRequest{Adapter: strings.TrimSpace(inputs["adapter"]), MaxItems: defaultSourceMaxItems, ExcerptChars: defaultSourceExcerptChars}
	if request.Adapter != SourceAdapterGiteaActivity {
		return request, invalidSourceRequest(step.ID, "unsupported source adapter %q (supported: %s)", request.Adapter, SourceAdapterGiteaActivity)
	}
	seen := map[string]bool{}
	for _, field := range strings.FieldsFunc(sourceInputValue(inputs["repositories"]), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
		if !sourceRepositoryPattern.MatchString(field) || strings.Contains(field, "..") {
			return request, invalidSourceRequest(step.ID, "repository %q must be OWNER/REPO", field)
		}
		key := strings.ToLower(field)
		if seen[key] {
			continue
		}
		seen[key] = true
		request.Sources = append(request.Sources, key)
	}
	sort.Strings(request.Sources)
	if len(request.Sources) > maxSourceRepositories {
		return request, invalidSourceRequest(step.ID, "%d repositories requested; limit is %d", len(request.Sources), maxSourceRepositories)
	}
	window := defaultSourceWindow
	if raw := sourceInputValue(inputs["window"]); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed < minSourceWindow || parsed > maxSourceWindow {
			return request, invalidSourceRequest(step.ID, "window %q must be a duration from %s to %s", raw, minSourceWindow, maxSourceWindow)
		}
		window = parsed
	}
	anchor := run.CreatedAt.UTC().Truncate(time.Second)
	if anchor.IsZero() {
		return request, invalidSourceRequest(step.ID, "run creation time is required to anchor the collection window")
	}
	end := anchor
	if raw := sourceInputValue(inputs["until"]); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return request, invalidSourceRequest(step.ID, "until %q must be an RFC 3339 timestamp", raw)
		}
		parsed = parsed.UTC().Truncate(time.Second)
		if parsed.After(anchor) {
			return request, invalidSourceRequest(step.ID, "until %s is after the run creation time %s; a later window end would change between collections", parsed.Format(time.RFC3339), anchor.Format(time.RFC3339))
		}
		end = parsed
	}
	request.WindowEnd, request.WindowStart = end, end.Add(-window)
	var err error
	if request.MaxItems, err = boundedSourceInt(step.ID, inputs, "max_items", defaultSourceMaxItems, 1, maxSourceMaxItems); err != nil {
		return request, err
	}
	if request.ExcerptChars, err = boundedSourceInt(step.ID, inputs, "max_excerpt_chars", defaultSourceExcerptChars, 0, maxSourceExcerptChars); err != nil {
		return request, err
	}
	return request, nil
}

func boundedSourceInt(stepID string, inputs map[string]string, key string, fallback, minimum, maximum int) (int, error) {
	raw := sourceInputValue(inputs[key])
	if raw == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, invalidSourceRequest(stepID, "%s %q must be an integer from %d to %d", key, raw, minimum, maximum)
	}
	return parsed, nil
}

func sourceRepositoryAllowlist() map[string]bool {
	allowed := map[string]bool{}
	for _, entry := range splitCSV(os.Getenv(sourceRepositoriesAllowlistEnv)) {
		allowed[strings.ToLower(entry)] = true
	}
	return allowed
}

// sourceContentBudget bounds each rendered output so it stays under the step
// output limit and is never omitted downstream.
func sourceContentBudget() int {
	limit := maxStepOutputBytes()
	if budget := limit - 4096; budget >= limit/2 {
		return budget
	}
	return limit / 2
}

func (g genericWorkflowExecutor) executeGenericSourceStep(ctx context.Context, run Run, step PipelineStep, inputs map[string]string) (PipelineStepExecution, error) {
	request, err := parseSourceCollectionRequest(run, step, inputs)
	if err != nil {
		return PipelineStepExecution{}, err
	}
	var reader GiteaRepositoryActivityReader
	if g.env != nil {
		reader = g.env.sourceActivityReader
	}
	artifactName := firstNonEmpty(sourceInputValue(inputs["artifact_name"]), step.ID+"-evidence.json")
	return collectSourceActivity(ctx, step.ID, request, reader, artifactName)
}

func collectSourceActivity(ctx context.Context, stepID string, request SourceCollectionRequest, reader GiteaRepositoryActivityReader, artifactName string) (PipelineStepExecution, error) {
	evidence := SourceEvidence{
		Schema:         sourceEvidenceSchema,
		Adapter:        request.Adapter,
		AdapterVersion: sourceAdapterVersion,
		Window:         sourceEvidenceWindow{Start: request.WindowStart.Format(time.RFC3339), End: request.WindowEnd.Format(time.RFC3339), Duration: request.WindowEnd.Sub(request.WindowStart).String()},
		Limits:         sourceEvidenceLimits{MaxItems: request.MaxItems, MaxExcerptChars: request.ExcerptChars, MaxPagesPerSource: sourceMaxPagesPerRepository, ContentBudgetBytes: sourceContentBudget()},
		SourceCount:    len(request.Sources),
		Sources:        []sourceEvidenceSource{},
		Items:          []SourceEvidenceItem{},
		OmittedItems:   []sourceEvidenceOmission{},
	}
	allowlist := sourceRepositoryAllowlist()
	var candidates []sourceCandidate
	var events []WorkflowExecutionEvent
	allDeterministic := true
	for _, source := range request.Sources {
		if err := ctx.Err(); err != nil {
			return PipelineStepExecution{}, err
		}
		record := sourceEvidenceSource{Source: source}
		var items []GiteaActivityItem
		var failure error
		switch {
		case !allowlist[source]:
			failure = sourceCollectionFailure{reason: "repository is not listed in " + sourceRepositoriesAllowlistEnv, retryable: false}
		case reader == nil:
			failure = sourceCollectionFailure{reason: "no Gitea activity reader is configured", retryable: false}
		default:
			items, record.Truncated, failure = reader.ListRepositoryActivity(ctx, source, request.WindowStart, request.WindowEnd, sourceMaxPagesPerRepository)
		}
		if failure != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(failure, ctxErr) {
				return PipelineStepExecution{}, ctxErr
			}
			record.Status = "failed"
			record.Error = RedactForLog(failure.Error())
			record.Retryable = sourceFailureRetryable(failure)
			if record.Retryable {
				allDeterministic = false
			}
			evidence.FailedSourceCount++
			events = append(events, WorkflowExecutionEvent{Type: sourceFailedEvent, Message: source + ": " + record.Error})
		} else {
			record.Status = "collected"
			record.ReturnedItems = len(items)
			for _, item := range items {
				candidates = append(candidates, sourceCandidate{source: source, item: item})
			}
		}
		evidence.Sources = append(evidence.Sources, record)
	}
	selectSourceItems(request, candidates, &evidence)
	outputs := renderSourceOutputs(request, &evidence)
	evidenceBody, _ := json.MarshalIndent(evidence, "", "  ")
	evidenceDigest := stepOutputDigest(string(evidenceBody))
	outputs["evidence_sha256"] = evidenceDigest
	outputs["artifact_name"] = artifactName
	out := PipelineStepExecution{
		Inputs: map[string]string{
			"adapter":           request.Adapter,
			"repositories":      strings.Join(request.Sources, ","),
			"window_start":      evidence.Window.Start,
			"window_end":        evidence.Window.End,
			"max_items":         strconv.Itoa(request.MaxItems),
			"max_excerpt_chars": strconv.Itoa(request.ExcerptChars),
		},
		Artifacts: []CreateArtifactInput{{Name: artifactName, Kind: "source-evidence", URI: inlineArtifactURI("application/json;charset=utf-8", string(evidenceBody))}},
		Events:    events,
	}
	if evidence.SourceCount > 0 && evidence.FailedSourceCount == evidence.SourceCount {
		out.Summary = fmt.Sprintf("No sources available: all %d requested sources failed", evidence.SourceCount)
		out.Events = append(out.Events, WorkflowExecutionEvent{Type: sourceUnavailableEvent, Message: out.Summary})
		return out, sourceCollectionFailure{reason: fmt.Sprintf("source step %q: every requested source failed; no draft will be generated from unavailable sources (%s)", stepID, outputs["failures"]), retryable: !allDeterministic}
	}
	out.Outputs = outputs
	out.Summary = fmt.Sprintf("Collected %d source items from %d of %d sources (%s)", evidence.ItemCount, evidence.SourceCount-evidence.FailedSourceCount, evidence.SourceCount, evidence.Status)
	out.Events = append(out.Events, WorkflowExecutionEvent{Type: sourceCollectedEvent, Message: fmt.Sprintf("%s %s; window %s to %s; evidence sha256 %s", request.Adapter, out.Summary, evidence.Window.Start, evidence.Window.End, evidenceDigest)})
	return out, nil
}

// selectSourceItems filters to the window, deduplicates, applies the item cap
// and output budget, orders deterministically, and assigns citation IDs.
func selectSourceItems(request SourceCollectionRequest, candidates []sourceCandidate, evidence *SourceEvidence) {
	byRef := map[string]sourceCandidate{}
	for _, candidate := range candidates {
		updated := candidate.item.UpdatedAt.UTC()
		if updated.Before(request.WindowStart) || updated.After(request.WindowEnd) {
			evidence.OmittedItems = append(evidence.OmittedItems, sourceEvidenceOmission{Ref: candidate.ref(), Reason: "updated outside the collection window"})
			continue
		}
		key := candidate.ref()
		if existing, ok := byRef[key]; ok && !candidate.item.UpdatedAt.After(existing.item.UpdatedAt) {
			continue
		}
		byRef[key] = candidate
	}
	selected := make([]sourceCandidate, 0, len(byRef))
	for _, candidate := range byRef {
		selected = append(selected, candidate)
	}
	// Keep the most recently updated items when the cap applies.
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		if !left.item.UpdatedAt.Equal(right.item.UpdatedAt) {
			return left.item.UpdatedAt.After(right.item.UpdatedAt)
		}
		return sourceCandidateLess(left, right)
	})
	if len(selected) > request.MaxItems {
		for _, dropped := range selected[request.MaxItems:] {
			evidence.OmittedItems = append(evidence.OmittedItems, sourceEvidenceOmission{Ref: dropped.ref(), Reason: fmt.Sprintf("exceeds max_items %d", request.MaxItems)})
		}
		selected = selected[:request.MaxItems]
	}
	sort.Slice(selected, func(i, j int) bool { return sourceCandidateLess(selected[i], selected[j]) })
	budget := evidence.Limits.ContentBudgetBytes
	contentBytes, referenceBytes, itemBytes := 0, 0, 0
	for index, candidate := range selected {
		item := sourceEvidenceItemFor(candidate, request.ExcerptChars)
		item.ID = "S" + strconv.Itoa(len(evidence.Items)+1)
		block, reference, summary := renderSourceItemBlock(item), renderSourceItemReference(item), sourceItemSummaryJSON(item)
		if contentBytes+len(block) > budget || referenceBytes+len(reference) > budget || itemBytes+len(summary)+1 > budget {
			for _, dropped := range selected[index:] {
				evidence.OmittedItems = append(evidence.OmittedItems, sourceEvidenceOmission{Ref: dropped.ref(), Reason: fmt.Sprintf("exceeds the %d-byte source content budget", budget)})
			}
			break
		}
		contentBytes += len(block)
		referenceBytes += len(reference)
		itemBytes += len(summary) + 1
		evidence.Items = append(evidence.Items, item)
	}
	sort.SliceStable(evidence.OmittedItems, func(i, j int) bool { return evidence.OmittedItems[i].Ref < evidence.OmittedItems[j].Ref })
	evidence.ItemCount = len(evidence.Items)
	evidence.OmittedItemCount = len(evidence.OmittedItems)
	switch {
	case evidence.ItemCount == 0:
		evidence.Status = SourceStatusEmpty
	case evidence.FailedSourceCount > 0:
		evidence.Status = SourceStatusPartial
	default:
		evidence.Status = SourceStatusComplete
	}
}

func sourceCandidateLess(left, right sourceCandidate) bool {
	if left.source != right.source {
		return left.source < right.source
	}
	return left.item.Number < right.item.Number
}

func sourceEvidenceItemFor(candidate sourceCandidate, excerptChars int) SourceEvidenceItem {
	item := candidate.item
	kind, state := "issue", strings.ToLower(strings.TrimSpace(item.State))
	if item.IsPullRequest {
		kind = "pull request"
		if item.Merged {
			state = "merged"
		}
	}
	out := SourceEvidenceItem{
		Source:     candidate.source,
		Kind:       kind,
		Number:     item.Number,
		Title:      sanitizeSourceText(truncateRunes(strings.Join(strings.Fields(item.Title), " "), maxSourceTitleChars)),
		State:      state,
		URL:        strings.TrimSpace(item.HTMLURL),
		Author:     strings.TrimSpace(item.Author),
		UpdatedAt:  item.UpdatedAt.UTC().Format(time.RFC3339),
		Excerpt:    sanitizeSourceText(truncateRunes(strings.TrimSpace(strings.ReplaceAll(item.Body, "\r\n", "\n")), excerptChars)),
		BodyBytes:  len(item.Body),
		BodySHA256: stepOutputDigest(item.Body),
	}
	if len(item.Labels) > 0 {
		out.Labels = append([]string(nil), item.Labels...)
		sort.Strings(out.Labels)
	}
	if !item.CreatedAt.IsZero() {
		out.CreatedAt = item.CreatedAt.UTC().Format(time.RFC3339)
	}
	if item.ClosedAt != nil {
		out.ClosedAt = item.ClosedAt.UTC().Format(time.RFC3339)
	}
	return out
}

// sanitizeSourceText keeps collected text from closing the delimiter that
// separates source material from instructions in the drafting prompt.
func sanitizeSourceText(value string) string {
	value = strings.ReplaceAll(value, sourceContentCloseTag, "[removed collected-sources tag]")
	return strings.ReplaceAll(value, sourceContentOpenTag, "[removed collected-sources tag]")
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + " [truncated]"
}

func renderSourceItemBlock(item SourceEvidenceItem) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[%s] %s#%d (%s, %s)\n", item.ID, item.Source, item.Number, item.Kind, item.State)
	fmt.Fprintf(&builder, "Title: %s\n", item.Title)
	if item.URL != "" {
		fmt.Fprintf(&builder, "URL: %s\n", item.URL)
	}
	meta := []string{"updated " + item.UpdatedAt}
	if item.Author != "" {
		meta = append([]string{"author " + item.Author}, meta...)
	}
	if item.ClosedAt != "" {
		meta = append(meta, "closed "+item.ClosedAt)
	}
	builder.WriteString(strings.Join(meta, "; ") + "\n")
	if len(item.Labels) > 0 {
		builder.WriteString("Labels: " + strings.Join(item.Labels, ", ") + "\n")
	}
	if item.Excerpt == "" {
		builder.WriteString("Body excerpt: (empty)\n")
	} else {
		builder.WriteString("Body excerpt:\n")
		for _, line := range strings.Split(item.Excerpt, "\n") {
			builder.WriteString("> " + line + "\n")
		}
	}
	builder.WriteString("\n")
	return builder.String()
}

func renderSourceItemReference(item SourceEvidenceItem) string {
	target := fmt.Sprintf("%s#%d", item.Source, item.Number)
	if item.URL != "" {
		target = "[" + target + "](" + item.URL + ")"
	}
	return fmt.Sprintf("- [%s] %s (%s, %s, updated %s): %s\n", item.ID, target, item.Kind, item.State, item.UpdatedAt, item.Title)
}

func sourceItemSummaryJSON(item SourceEvidenceItem) string {
	encoded, _ := json.Marshal(sourceItemSummary(item))
	return string(encoded)
}

type sourceItemSummaryRecord struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Kind      string `json:"kind"`
	Number    int    `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updated_at"`
}

func sourceItemSummary(item SourceEvidenceItem) sourceItemSummaryRecord {
	return sourceItemSummaryRecord{ID: item.ID, Source: item.Source, Kind: item.Kind, Number: item.Number, Title: item.Title, State: item.State, URL: item.URL, UpdatedAt: item.UpdatedAt}
}

// renderSourceOutputs builds the named outputs published by a source step.
func renderSourceOutputs(request SourceCollectionRequest, evidence *SourceEvidence) map[string]string {
	var content, references, failures strings.Builder
	ids := make([]string, 0, len(evidence.Items))
	summaries := make([]sourceItemSummaryRecord, 0, len(evidence.Items))
	for _, item := range evidence.Items {
		content.WriteString(renderSourceItemBlock(item))
		references.WriteString(renderSourceItemReference(item))
		ids = append(ids, item.ID)
		summaries = append(summaries, sourceItemSummary(item))
	}
	if len(evidence.Items) == 0 {
		content.WriteString("No source items were collected.\n")
		references.WriteString("No sources collected.\n")
	}
	for _, source := range evidence.Sources {
		if source.Status == "failed" {
			fmt.Fprintf(&failures, "- %s: %s\n", source.Source, source.Error)
		}
		if source.Truncated {
			fmt.Fprintf(&references, "\nNote: %s returned more than %d pages of activity; later pages were not read.\n", source.Source, sourceMaxPagesPerRepository)
		}
	}
	if evidence.OmittedItemCount > 0 {
		fmt.Fprintf(&references, "\nNote: %d item(s) were omitted by collection limits; see the evidence artifact.\n", evidence.OmittedItemCount)
	}
	failureText := strings.TrimSpace(failures.String())
	if failureText == "" {
		failureText = "none"
	}
	itemsJSON, _ := json.Marshal(summaries)
	outputs := map[string]string{
		"adapter":             request.Adapter,
		"status":              evidence.Status,
		"item_count":          strconv.Itoa(evidence.ItemCount),
		"source_count":        strconv.Itoa(evidence.SourceCount),
		"failed_source_count": strconv.Itoa(evidence.FailedSourceCount),
		"omitted_item_count":  strconv.Itoa(evidence.OmittedItemCount),
		"window_start":        evidence.Window.Start,
		"window_end":          evidence.Window.End,
		"content":             strings.TrimRight(content.String(), "\n"),
		"references":          strings.TrimRight(references.String(), "\n"),
		"failures":            failureText,
		"source_ids":          strings.Join(ids, ","),
		"items":               string(itemsJSON),
	}
	return outputs
}
