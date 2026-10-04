package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Open Pilot issue inbox is a bounded, read-only, control-listener-only
// projection of intake candidates. It lists open issues from registered
// repositories the admission gate currently admits, evaluates each against the
// same pickup, specification, command-allowlist, and durable-claim rules the
// poller and validate-issue step enforce, and annotates every candidate with
// stable reason codes. It never dispatches, claims, or mutates anything;
// dispatch and resume go through the existing run and lifecycle endpoints.

const (
	openPilotInboxDefaultLimit = 25
	openPilotInboxMaxLimit     = 100
	openPilotInboxTimeout      = 8 * time.Second
	openPilotInboxMaxQuery     = 128
)

// Inbox-specific reason codes. Readiness codes are reused where they apply.
const (
	OpenPilotReasonRepositoryNotAdmitted  = "repository_not_admitted"
	OpenPilotReasonUpstreamUnavailable    = "upstream_unavailable"
	OpenPilotReasonUpstreamNotListed      = "upstream_not_listed"
	OpenPilotReasonIssueNotOpen           = "issue_not_open"
	OpenPilotReasonLabelsMissing          = "labels_missing"
	OpenPilotReasonIssueSpecMissing       = "issue_spec_missing"
	OpenPilotReasonIssueClaimed           = "issue_claimed"
	OpenPilotReasonIssueLifecycleComplete = "issue_lifecycle_complete"
)

const (
	OpenPilotInboxUpstreamAvailable   = "available"
	OpenPilotInboxUpstreamPartial     = "partial"
	OpenPilotInboxUpstreamUnavailable = "unavailable"
	OpenPilotInboxUpstreamNotQueried  = "not_queried"

	OpenPilotInboxRepositoryListed      = "listed"
	OpenPilotInboxRepositoryUnavailable = "unavailable"
	OpenPilotInboxRepositoryNotQueried  = "not_queried"

	OpenPilotInboxSourceUpstream = "upstream"
	OpenPilotInboxSourceState    = "state"

	OpenPilotInboxActionDispatch = "dispatch"
	OpenPilotInboxActionResume   = "resume"
	OpenPilotInboxActionNone     = "none"
)

type OpenPilotInboxFilter struct {
	Repo        string `json:"repo,omitempty"`
	Eligibility string `json:"eligibility"`
	Phase       string `json:"phase,omitempty"`
	Pickup      string `json:"pickup,omitempty"`
	Query       string `json:"q,omitempty"`
	Limit       int    `json:"limit"`
	Cursor      string `json:"cursor,omitempty"`
}

type OpenPilotInboxPolicy struct {
	RequiredLabels   []string `json:"required_labels"`
	Assignee         string   `json:"assignee,omitempty"`
	RequireIssueSpec bool     `json:"require_issue_spec"`
	PollRepositories []string `json:"poll_repositories"`
}

type OpenPilotInboxRepository struct {
	ID                 string `json:"id"`
	Slug               string `json:"slug"`
	ExecutorGeneration string `json:"executor_generation"`
	Admitted           bool   `json:"admitted"`
	AdmissionCode      string `json:"admission_code"`
	AdmissionMessage   string `json:"admission_message,omitempty"`
	Polled             bool   `json:"polled"`
	Upstream           string `json:"upstream"`
	UpstreamError      string `json:"upstream_error,omitempty"`
	Listed             int    `json:"listed"`
	Claims             int    `json:"claims"`
}

type OpenPilotInboxClaim struct {
	StateID               string    `json:"state_id"`
	Phase                 string    `json:"phase"`
	RunID                 string    `json:"run_id,omitempty"`
	RunStatus             string    `json:"run_status,omitempty"`
	ActiveRun             bool      `json:"active_run"`
	NextAction            string    `json:"next_action"`
	LastStepName          string    `json:"last_step_name,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
	FailingGate           string    `json:"failing_gate,omitempty"`
	OperatorSummary       string    `json:"operator_summary,omitempty"`
	RecommendedNextAction string    `json:"recommended_next_action,omitempty"`
	Branch                string    `json:"branch,omitempty"`
	PullRequestURL        string    `json:"pull_request_url,omitempty"`
	PullRequestNumber     int       `json:"pull_request_number,omitempty"`
	MergeCommitSHA        string    `json:"merge_commit_sha,omitempty"`
	IssueClosed           bool      `json:"issue_closed"`
	RepairAttempts        int       `json:"repair_attempts"`
	MaxRepairAttempts     int       `json:"max_repair_attempts"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type OpenPilotInboxLinks struct {
	Issue       string `json:"issue,omitempty"`
	PullRequest string `json:"pull_request,omitempty"`
	Run         string `json:"run,omitempty"`
	State       string `json:"state,omitempty"`
	Readiness   string `json:"readiness"`
}

type OpenPilotInboxIssue struct {
	Key                string                    `json:"key"`
	Repo               string                    `json:"repo"`
	RepositoryID       string                    `json:"repository_id"`
	ExecutorGeneration string                    `json:"executor_generation"`
	Issue              string                    `json:"issue"`
	IssueID            int                       `json:"issue_id,omitempty"`
	Title              string                    `json:"title,omitempty"`
	State              string                    `json:"state,omitempty"`
	Labels             []string                  `json:"labels"`
	Assignees          []string                  `json:"assignees"`
	Source             string                    `json:"source"`
	Pickup             string                    `json:"pickup"`
	BotAssigned        bool                      `json:"bot_assigned"`
	LabelsSatisfied    bool                      `json:"labels_satisfied"`
	MissingLabels      []string                  `json:"missing_labels"`
	SpecRequired       bool                      `json:"spec_required"`
	SpecReady          bool                      `json:"spec_ready"`
	TestCommand        string                    `json:"test_command,omitempty"`
	Eligible           bool                      `json:"eligible"`
	Action             string                    `json:"action"`
	ReasonCodes        []string                  `json:"reason_codes"`
	Checks             []OpenPilotReadinessCheck `json:"checks"`
	Claim              *OpenPilotInboxClaim      `json:"claim,omitempty"`
	Links              OpenPilotInboxLinks       `json:"links"`
	number             int
}

type OpenPilotInboxReport struct {
	CheckedAt      time.Time                  `json:"checked_at"`
	ActivationMode string                     `json:"activation_mode,omitempty"`
	Upstream       string                     `json:"upstream"`
	UpstreamError  string                     `json:"upstream_error,omitempty"`
	Policy         OpenPilotInboxPolicy       `json:"policy"`
	Repositories   []OpenPilotInboxRepository `json:"repositories"`
	Issues         []OpenPilotInboxIssue      `json:"issues"`
	Filter         OpenPilotInboxFilter       `json:"filter"`
	Matched        int                        `json:"matched"`
	HasMore        bool                       `json:"has_more"`
	NextCursor     string                     `json:"next_cursor,omitempty"`
	Checks         []OpenPilotReadinessCheck  `json:"checks"`
	Blockers       []string                   `json:"blockers"`
}

func (r *OpenPilotInboxReport) add(check OpenPilotReadinessCheck) {
	r.Checks = append(r.Checks, check)
	if check.Status == OpenPilotReadinessFail {
		r.Blockers = append(r.Blockers, check.Code)
	}
}

func (i *OpenPilotInboxIssue) add(check OpenPilotReadinessCheck) {
	i.Checks = append(i.Checks, check)
	if check.Status == OpenPilotReadinessFail {
		i.ReasonCodes = append(i.ReasonCodes, check.Code)
	}
}

type openPilotInboxInput struct {
	Filter    OpenPilotInboxFilter
	Gate      *OpenPilotAdmissionGate
	GateError error
	Executors *EnvWorkflowExecutors
	Issues    GiteaIssueReader
	Store     Store
	PollRepos []string
	Now       time.Time
}

func openPilotPollRepositoriesFromEnv() []string {
	repos := splitCSV(os.Getenv("OPEN_PILOT_POLL_REPOS"))
	for index := range repos {
		repos[index] = strings.ToLower(strings.TrimSpace(repos[index]))
	}
	return repos
}

func openPilotInboxPolicy(env *EnvWorkflowExecutors, pollRepos []string) OpenPilotInboxPolicy {
	policy := OpenPilotInboxPolicy{RequiredLabels: []string{}, PollRepositories: append([]string{}, pollRepos...)}
	if env != nil {
		policy.RequiredLabels = append(policy.RequiredLabels, env.openPilotLabels...)
		policy.Assignee = env.openPilotAssignee
		policy.RequireIssueSpec = env.requireIssueSpec
		return policy
	}
	policy.RequiredLabels = append(policy.RequiredLabels, splitCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_REQUIRED_LABELS"), "ready-for-agent,agent:queued"))...)
	policy.Assignee = strings.TrimSpace(os.Getenv("OPEN_PILOT_ASSIGNEE"))
	policy.RequireIssueSpec = strings.EqualFold(os.Getenv("OPEN_PILOT_REQUIRE_ISSUE_SPEC"), "true")
	return policy
}

// parseOpenPilotInboxFilter validates the bounded query contract. It returns a
// human-readable message for the 400 response when the query is malformed.
func parseOpenPilotInboxFilter(values map[string][]string) (OpenPilotInboxFilter, string) {
	get := func(key string) string {
		if list := values[key]; len(list) > 0 {
			return strings.TrimSpace(list[0])
		}
		return ""
	}
	filter := OpenPilotInboxFilter{Repo: get("repo"), Eligibility: firstNonEmpty(get("eligibility"), "all"), Phase: get("phase"), Pickup: get("pickup"), Query: get("q"), Cursor: get("cursor"), Limit: openPilotInboxDefaultLimit}
	if len(filter.Repo) > 256 || len(filter.Query) > openPilotInboxMaxQuery || len(filter.Cursor) > 512 {
		return filter, "repo, q, or cursor exceeds its bounded length"
	}
	switch filter.Eligibility {
	case "all", "eligible", "ineligible", "claimed":
	default:
		return filter, "eligibility must be one of all, eligible, ineligible, claimed"
	}
	if filter.Phase != "" && !isOpenPilotKnownPhase(OpenPilotPhase(filter.Phase)) {
		return filter, "phase is not a known Open Pilot lifecycle phase"
	}
	switch filter.Pickup {
	case "", "assigned", "labels", "none":
	default:
		return filter, "pickup must be one of assigned, labels, none"
	}
	if raw := get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > openPilotInboxMaxLimit {
			return filter, "limit must be between 1 and " + strconv.Itoa(openPilotInboxMaxLimit)
		}
		filter.Limit = parsed
	}
	if filter.Cursor != "" {
		if _, _, ok := decodeOpenPilotInboxCursor(filter.Cursor); !ok {
			return filter, "cursor is not a valid inbox cursor"
		}
	}
	return filter, ""
}

type openPilotInboxCursor struct {
	Repo  string `json:"repo"`
	Issue string `json:"issue"`
}

func encodeOpenPilotInboxCursor(repo, issue string) string {
	raw, _ := json.Marshal(openPilotInboxCursor{Repo: repo, Issue: issue})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOpenPilotInboxCursor(cursor string) (string, string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", false
	}
	var decoded openPilotInboxCursor
	if err := json.Unmarshal(raw, &decoded); err != nil || !validOpenPilotRepositorySlug(decoded.Repo) {
		return "", "", false
	}
	if _, err := ParseOpenPilotIssue(decoded.Issue); err != nil {
		return "", "", false
	}
	return decoded.Repo, decoded.Issue, true
}

func buildOpenPilotInbox(ctx context.Context, input openPilotInboxInput) OpenPilotInboxReport {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	filter := input.Filter
	if filter.Limit <= 0 {
		filter.Limit = openPilotInboxDefaultLimit
	}
	if filter.Eligibility == "" {
		filter.Eligibility = "all"
	}
	report := OpenPilotInboxReport{
		CheckedAt: now.UTC(), Upstream: OpenPilotInboxUpstreamNotQueried, Policy: openPilotInboxPolicy(input.Executors, input.PollRepos),
		Repositories: []OpenPilotInboxRepository{}, Issues: []OpenPilotInboxIssue{}, Filter: filter,
		Checks: []OpenPilotReadinessCheck{}, Blockers: []string{},
	}
	gate := input.Gate
	if input.GateError != nil || gate == nil || gate.registry == nil {
		message := "the Open Pilot admission gate could not be constructed"
		if input.GateError != nil {
			message = input.GateError.Error()
		}
		report.add(OpenPilotReadinessCheck{Name: "admission_gate", Code: OpenPilotReasonGateUnavailable, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Fix the runtime configuration named in the message and restart the API; the inbox does not query Gitea until the gate loads."})
		return report
	}
	report.ActivationMode = gate.mode

	repositories := gate.registry.Repositories()
	if filter.Repo != "" {
		resolved, err := gate.registry.ResolveTarget(filter.Repo, "1")
		if err != nil {
			report.add(OpenPilotReadinessCheck{Name: "repository_registered", Code: OpenPilotReasonRepositoryUnregistered, Status: OpenPilotReadinessFail, Message: "repository " + filter.Repo + " matches no registered canonical slug or alias",
				Remediation: "Add the repository to the operator registry with its exact Gitea origin slug, re-render the topology, and restart the API."})
			return report
		}
		filtered := repositories[:0:0]
		for _, repository := range repositories {
			if repository.ID == resolved.RepositoryID {
				filtered = append(filtered, repository)
			}
		}
		repositories = filtered
	}

	polled := map[string]bool{}
	wildcard := len(input.PollRepos) == 1 && input.PollRepos[0] == "*"
	var discoveryError error
	if wildcard && input.Issues != nil {
		if discovery, ok := input.Issues.(GiteaRepositoryLister); ok {
			var visible []string
			visible, discoveryError = discovery.ListOpenPilotRepositories(ctx)
			for _, slug := range visible {
				if target, err := gate.registry.ResolveTarget(slug, "1"); err == nil {
					polled[target.RepositoryID] = true
				}
			}
		} else {
			discoveryError = errors.New("the Gitea issue reader does not support repository discovery")
		}
	}
	for _, slug := range input.PollRepos {
		if target, err := gate.registry.ResolveTarget(slug, "1"); err == nil {
			polled[target.RepositoryID] = true
		}
	}
	states := []OpenPilotIssueState{}
	if input.Store != nil {
		states = input.Store.ListOpenPilotIssueStates()
	}
	runs := []Run{}
	if input.Store != nil {
		runs = input.Store.ListRuns()
	}
	if input.Issues == nil {
		report.UpstreamError = "no Gitea issue reader is configured"
	}

	// Independent repository reads share a bounded worker pool. Slow upstream
	// responses must not consume the entire inbox budget before later entries
	// can be inspected.
	upstreams := make([]struct {
		issues []GiteaIssue
		err    error
	}, len(repositories))
	if input.Issues != nil && discoveryError == nil {
		jobs := make(chan int, len(repositories))
		for index, repository := range repositories {
			admitted, _, _ := openPilotInboxRepositoryAdmission(gate, repository, now)
			if admitted && (!wildcard || polled[repository.ID]) {
				jobs <- index
			}
		}
		close(jobs)
		var workers sync.WaitGroup
		for worker := 0; worker < 4; worker++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for index := range jobs {
					upstreams[index].issues, upstreams[index].err = openPilotInboxListRepository(ctx, input.Issues, repositories[index].Slug, report.Policy)
				}
			}()
		}
		workers.Wait()
	}
	queried, listed := 0, 0
	var candidates []OpenPilotInboxIssue
	for index, repository := range repositories {
		entry := OpenPilotInboxRepository{ID: repository.ID, Slug: repository.Slug, ExecutorGeneration: repository.ExecutorGeneration, Polled: polled[repository.ID], Upstream: OpenPilotInboxRepositoryNotQueried}
		entry.Admitted, entry.AdmissionCode, entry.AdmissionMessage = openPilotInboxRepositoryAdmission(gate, repository, now)
		var upstream []GiteaIssue
		upstreamKnown := false
		if entry.Admitted && input.Issues != nil && discoveryError != nil {
			queried++
			entry.Upstream, entry.UpstreamError = OpenPilotInboxRepositoryUnavailable, RedactForLog(discoveryError.Error())
		} else if entry.Admitted && input.Issues != nil && (!wildcard || polled[repository.ID]) {
			queried++
			issues, err := upstreams[index].issues, upstreams[index].err
			if err != nil {
				entry.Upstream, entry.UpstreamError = OpenPilotInboxRepositoryUnavailable, RedactForLog(err.Error())
			} else {
				listed++
				entry.Upstream, entry.Listed, upstream, upstreamKnown = OpenPilotInboxRepositoryListed, len(issues), issues, true
			}
		}
		seen := map[string]bool{}
		for _, issue := range upstream {
			item := openPilotInboxIssueFromUpstream(input, gate, repository, issue, states, runs, report.Policy)
			if seen[item.Key] {
				continue
			}
			seen[item.Key] = true
			candidates = append(candidates, item)
		}
		for _, state := range states {
			target, err := gate.registry.ResolveTarget(state.Repo, state.Issue)
			if err != nil || target.RepositoryID != repository.ID {
				continue
			}
			entry.Claims++
			key := target.RepositorySlug + "#" + target.Issue
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, openPilotInboxIssueFromState(gate, repository, target, state, runs, entry, upstreamKnown))
		}
		report.Repositories = append(report.Repositories, entry)
	}

	switch {
	case queried == 0:
		report.Upstream = OpenPilotInboxUpstreamNotQueried
	case listed == queried:
		report.Upstream = OpenPilotInboxUpstreamAvailable
	case listed == 0:
		report.Upstream = OpenPilotInboxUpstreamUnavailable
	default:
		report.Upstream = OpenPilotInboxUpstreamPartial
	}
	if report.Upstream == OpenPilotInboxUpstreamUnavailable || report.Upstream == OpenPilotInboxUpstreamPartial {
		for _, repository := range report.Repositories {
			if repository.UpstreamError != "" {
				report.UpstreamError = repository.Slug + ": " + repository.UpstreamError
				break
			}
		}
		report.add(OpenPilotReadinessCheck{Name: "upstream", Code: OpenPilotReasonUpstreamUnavailable, Status: OpenPilotReadinessWarn, Message: "Gitea issue listing failed for at least one admitted repository; the queue may be incomplete rather than empty",
			Remediation: "Check GITEA_BASE_URL, the Gitea token's read scope, and Gitea availability, then refresh. Durable claims are still shown from the store."})
	}

	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Repo != candidates[right].Repo {
			return candidates[left].Repo < candidates[right].Repo
		}
		return candidates[left].number < candidates[right].number
	})
	matched := candidates[:0:0]
	for _, item := range candidates {
		if openPilotInboxMatches(item, filter) {
			matched = append(matched, item)
		}
	}
	report.Matched = len(matched)
	start := 0
	if filter.Cursor != "" {
		repo, issue, ok := decodeOpenPilotInboxCursor(filter.Cursor)
		if ok {
			number, _ := strconv.Atoi(issue)
			for index, item := range matched {
				if item.Repo < repo || (item.Repo == repo && item.number <= number) {
					start = index + 1
				}
			}
		}
	}
	if start > len(matched) {
		start = len(matched)
	}
	page := matched[start:]
	if len(page) > filter.Limit {
		page = page[:filter.Limit]
		report.HasMore = true
		last := page[len(page)-1]
		report.NextCursor = encodeOpenPilotInboxCursor(last.Repo, last.Issue)
	}
	report.Issues = append(report.Issues, page...)
	return report
}

func openPilotInboxRepositoryAdmission(gate *OpenPilotAdmissionGate, repository OpenPilotRuntimeRepository, now time.Time) (bool, string, string) {
	if _, err := gate.ResolvePollRepository(repository.Slug); err == nil {
		return true, "repository_admitted", ""
	}
	switch gate.mode {
	case "disabled":
		return false, OpenPilotReasonActivationDisabled, "SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE=disabled admits no work"
	case "durable":
		target, err := gate.registry.ResolveTarget(repository.Slug, "1")
		if err != nil {
			return false, OpenPilotReasonRepositoryNotAdmitted, "the repository does not resolve through the loaded registry"
		}
		rollout := openPilotReadinessRollout(gate.registry.manifest.RegistryFingerprint, gate.rollout, now)
		check := openPilotRolloutCheck(rollout, gate.rollout != nil, target, true, false, now)
		if check.Status == OpenPilotReadinessFail {
			return false, check.Code, check.Message
		}
		return false, OpenPilotReasonRepositoryNotAdmitted, check.Message
	case "", "legacy-singleton":
		return false, OpenPilotReasonRepositoryNotBound, "legacy-singleton runtime is bound to " + gate.bound.Slug + " and rejects other registered repositories"
	default:
		return false, OpenPilotReasonRepositoryNotAdmitted, "the admission gate rejects this repository"
	}
}

func openPilotInboxListRepository(ctx context.Context, reader GiteaIssueReader, slug string, policy OpenPilotInboxPolicy) ([]GiteaIssue, error) {
	labeled, err := reader.ListIssues(ctx, slug, policy.RequiredLabels)
	if err != nil {
		return nil, err
	}
	issues := append([]GiteaIssue(nil), labeled...)
	if policy.Assignee != "" {
		assigned, err := reader.ListAssignedIssues(ctx, slug, policy.Assignee)
		if err != nil {
			return nil, err
		}
		issues = append(issues, assigned...)
	}
	return mergeOpenPilotInboxIssues(issues), nil
}

// mergeOpenPilotInboxIssues collapses the label and assignee listings into one
// entry per issue number, keeping the union of labels and assignees so the
// pickup path reflects both listings.
func mergeOpenPilotInboxIssues(issues []GiteaIssue) []GiteaIssue {
	index := map[int]int{}
	merged := make([]GiteaIssue, 0, len(issues))
	for _, issue := range issues {
		position, ok := index[issue.Number]
		if !ok {
			index[issue.Number] = len(merged)
			issue.Labels = append([]string(nil), issue.Labels...)
			issue.Assignees = append([]GiteaIssueAssignee(nil), issue.Assignees...)
			merged = append(merged, issue)
			continue
		}
		existing := &merged[position]
		for _, label := range issue.Labels {
			if !hasAllLabels(existing.Labels, []string{label}) {
				existing.Labels = append(existing.Labels, label)
			}
		}
		for _, assignee := range issue.Assignees {
			if !openPilotIssueAssignedTo(*existing, assignee.Login) {
				existing.Assignees = append(existing.Assignees, assignee)
			}
		}
		if existing.Body == "" {
			existing.Body = issue.Body
		}
		if existing.HTMLURL == "" {
			existing.HTMLURL = issue.HTMLURL
		}
	}
	return merged
}

func openPilotInboxIssueFromUpstream(input openPilotInboxInput, gate *OpenPilotAdmissionGate, repository OpenPilotRuntimeRepository, issue GiteaIssue, states []OpenPilotIssueState, runs []Run, policy OpenPilotInboxPolicy) OpenPilotInboxIssue {
	number := strconv.Itoa(issue.Number)
	item := OpenPilotInboxIssue{
		Key: repository.Slug + "#" + number, Repo: repository.Slug, RepositoryID: repository.ID, ExecutorGeneration: repository.ExecutorGeneration,
		Issue: number, IssueID: issue.ID, Title: issue.Title, State: issue.State, Labels: append([]string{}, issue.Labels...), Assignees: []string{},
		Source: OpenPilotInboxSourceUpstream, MissingLabels: []string{}, ReasonCodes: []string{}, Checks: []OpenPilotReadinessCheck{}, number: issue.Number,
		Links: OpenPilotInboxLinks{Issue: issue.HTMLURL, Readiness: "/api/open-pilot/readiness?repo=" + repository.Slug + "&issue=" + number},
	}
	for _, assignee := range issue.Assignees {
		if assignee.Login != "" {
			item.Assignees = append(item.Assignees, assignee.Login)
		}
	}
	item.SpecRequired = policy.RequireIssueSpec

	target, err := gate.Resolve(repository.Slug, number)
	if err != nil {
		if gate.mode == "durable" {
			item.add(OpenPilotReadinessCheck{Name: "admission", Code: OpenPilotReasonRolloutProofExcluded, Status: OpenPilotReadinessFail, Message: "the durable rollout gate does not admit this issue; proof mode admits only its configured proof targets",
				Remediation: "Dispatch a listed proof target, or promote the gate to allow after both proof targets close successfully."})
		} else {
			item.add(OpenPilotReadinessCheck{Name: "admission", Code: OpenPilotReasonIssueInvalid, Status: OpenPilotReadinessFail, Message: "the admission gate rejects this issue number",
				Remediation: "Issue numbers must be positive decimals; check the registry binding for this repository."})
		}
	} else {
		item.Issue, item.Key = target.Issue, target.RepositorySlug+"#"+target.Issue
		item.Links.Readiness = "/api/open-pilot/readiness?repo=" + target.RepositorySlug + "&issue=" + target.Issue
		item.add(OpenPilotReadinessCheck{Name: "admission", Code: "issue_admitted", Status: OpenPilotReadinessPass, Message: "the admission gate resolves " + target.RepositorySlug + "#" + target.Issue + " to " + target.RepositoryID + "/" + target.ExecutorGeneration})
	}

	if issue.State != "open" || issue.ID < 1 {
		item.add(OpenPilotReadinessCheck{Name: "issue_open", Code: OpenPilotReasonIssueNotOpen, Status: OpenPilotReadinessFail, Message: "the issue is not open (" + firstNonEmpty(issue.State, "unknown") + ")",
			Remediation: "Only open issues with a Gitea identity are picked up; reopen the issue if work should continue."})
	} else {
		item.add(OpenPilotReadinessCheck{Name: "issue_open", Code: "issue_open", Status: OpenPilotReadinessPass, Message: "the issue is open"})
	}

	item.BotAssigned = openPilotIssueAssignedTo(issue, policy.Assignee)
	item.LabelsSatisfied = hasAllLabels(issue.Labels, policy.RequiredLabels)
	item.MissingLabels = openPilotMissingLabels(issue.Labels, policy.RequiredLabels)
	switch {
	case item.BotAssigned && item.LabelsSatisfied:
		item.Pickup = "both"
		item.add(OpenPilotReadinessCheck{Name: "pickup", Code: "pickup_assigned", Status: OpenPilotReadinessPass, Message: "assigned to " + policy.Assignee + " and carries every queue label"})
	case item.BotAssigned:
		item.Pickup = "assigned"
		item.add(OpenPilotReadinessCheck{Name: "pickup", Code: "pickup_assigned", Status: OpenPilotReadinessPass, Message: "assigned to " + policy.Assignee + "; queue labels are not required for the assignment path"})
	case item.LabelsSatisfied:
		item.Pickup = "labels"
		item.add(OpenPilotReadinessCheck{Name: "pickup", Code: "pickup_labels", Status: OpenPilotReadinessPass, Message: "carries every queue label: " + strings.Join(policy.RequiredLabels, ", ")})
	default:
		item.Pickup = "none"
		message := "missing queue label(s): " + strings.Join(item.MissingLabels, ", ")
		remediation := "Add the missing label(s) after reviewing the issue body."
		if policy.Assignee != "" {
			message += "; not assigned to " + policy.Assignee
			remediation = "Add the missing label(s) after reviewing the issue body, or assign the issue to " + policy.Assignee + "."
		}
		item.add(OpenPilotReadinessCheck{Name: "pickup", Code: OpenPilotReasonLabelsMissing, Status: OpenPilotReadinessFail, Message: message, Remediation: remediation})
	}

	item.SpecReady = openPilotIssueSpecReady(issue.Body)
	switch {
	case item.SpecReady:
		item.add(OpenPilotReadinessCheck{Name: "specification", Code: "issue_spec_ready", Status: OpenPilotReadinessPass, Message: "the body mentions acceptance criteria and test instructions"})
	case policy.RequireIssueSpec:
		item.add(OpenPilotReadinessCheck{Name: "specification", Code: OpenPilotReasonIssueSpecMissing, Status: OpenPilotReadinessFail, Message: "OPEN_PILOT_REQUIRE_ISSUE_SPEC=true and the body lacks acceptance criteria or test instructions",
			Remediation: "Add Acceptance Criteria and Test Command sections to the issue body."})
	default:
		item.add(OpenPilotReadinessCheck{Name: "specification", Code: OpenPilotReasonIssueSpecMissing, Status: OpenPilotReadinessWarn, Message: "the body lacks acceptance criteria or test instructions; validation does not require them",
			Remediation: "Add Acceptance Criteria and Test Command sections so the agent has a verifiable target."})
	}

	item.TestCommand = openPilotIssueTestCommand(issue.Body)
	if item.TestCommand == "" {
		item.add(OpenPilotReadinessCheck{Name: "test_command", Code: OpenPilotReasonCommandNotProvided, Status: OpenPilotReadinessSkip, Message: "the body has no Test Command section; the verify step is skipped unless a command is supplied at dispatch"})
	} else {
		var bundles map[string]openPilotExecutionBundle
		if input.Executors != nil {
			bundles = input.Executors.openPilotBundles
		}
		var probe OpenPilotReadinessReport
		openPilotReadinessCommandCheck(&probe, openPilotReadinessInput{TestCommand: item.TestCommand, Executors: input.Executors}, bundles, target, err == nil)
		for _, check := range probe.Checks {
			item.add(check)
		}
	}

	if err == nil {
		openPilotInboxClaimChecks(&item, gate, input.Store, target, states, runs, true)
	}
	item.Eligible = len(item.ReasonCodes) == 0
	if item.Eligible {
		item.Action = OpenPilotInboxActionDispatch
		item.add(OpenPilotReadinessCheck{Name: "dispatch", Code: "dispatch_eligible", Status: OpenPilotReadinessPass, Message: "dispatch creates a new lifecycle through the existing admission and claim path"})
	} else if item.Action == "" {
		item.Action = OpenPilotInboxActionNone
	}
	return item
}

func openPilotInboxIssueFromState(gate *OpenPilotAdmissionGate, repository OpenPilotRuntimeRepository, target OpenPilotTarget, state OpenPilotIssueState, runs []Run, entry OpenPilotInboxRepository, upstreamKnown bool) OpenPilotInboxIssue {
	number, _ := strconv.Atoi(target.Issue)
	item := OpenPilotInboxIssue{
		Key: target.RepositorySlug + "#" + target.Issue, Repo: target.RepositorySlug, RepositoryID: repository.ID, ExecutorGeneration: repository.ExecutorGeneration,
		Issue: target.Issue, Labels: []string{}, Assignees: []string{}, Source: OpenPilotInboxSourceState, Pickup: "unknown", MissingLabels: []string{},
		ReasonCodes: []string{}, Checks: []OpenPilotReadinessCheck{}, number: number,
		Links: OpenPilotInboxLinks{Readiness: "/api/open-pilot/readiness?repo=" + target.RepositorySlug + "&issue=" + target.Issue},
	}
	switch {
	case entry.Upstream == OpenPilotInboxRepositoryUnavailable:
		item.add(OpenPilotReadinessCheck{Name: "upstream", Code: OpenPilotReasonUpstreamUnavailable, Status: OpenPilotReadinessWarn, Message: "Gitea could not be listed; labels, assignees, and open state are unknown for this claim",
			Remediation: "Restore Gitea access and refresh before acting on eligibility."})
	case !upstreamKnown:
		item.add(OpenPilotReadinessCheck{Name: "upstream", Code: OpenPilotReasonUpstreamNotListed, Status: OpenPilotReadinessWarn, Message: "Gitea was not queried for this repository; only the durable claim is shown"})
	default:
		item.add(OpenPilotReadinessCheck{Name: "upstream", Code: OpenPilotReasonUpstreamNotListed, Status: OpenPilotReadinessWarn, Message: "the issue is no longer listed by Gitea with queue labels or bot assignment; the durable claim remains",
			Remediation: "Finish, resume, or pause the lifecycle from its durable state; relabel the issue only if a new lifecycle is intended."})
	}
	openPilotInboxClaimChecks(&item, gate, nil, target, []OpenPilotIssueState{state}, runs, false)
	item.Eligible = false
	if item.Action == "" {
		item.Action = OpenPilotInboxActionNone
	}
	return item
}

// openPilotInboxClaimChecks projects the durable lifecycle state for an issue
// and decides whether intake, resume, or nothing applies.
func openPilotInboxClaimChecks(item *OpenPilotInboxIssue, gate *OpenPilotAdmissionGate, store Store, target OpenPilotTarget, states []OpenPilotIssueState, runs []Run, lookup bool) {
	var state OpenPilotIssueState
	found := false
	if lookup && store != nil {
		resolved, err := store.OpenPilotIssueStateForTarget(target)
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonRolloutStoreUnavailable, Status: OpenPilotReadinessFail, Message: "the durable issue state could not be read: " + RedactForLog(err.Error()),
				Remediation: "Check store connectivity and retry."})
			return
		default:
			state, found = resolved, true
		}
	} else {
		for _, candidate := range states {
			if candidate.CanonicalIssue == target.Issue && (candidate.RepositoryID == target.RepositoryID || foldOpenPilotRepositoryName(candidate.CanonicalRepo) == foldOpenPilotRepositoryName(target.RepositorySlug)) {
				state, found = candidate, true
				break
			}
		}
	}
	if !found {
		item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: "issue_state_new", Status: OpenPilotReadinessPass, Message: "no durable lifecycle state exists; dispatch starts a new lifecycle at issue validation"})
		return
	}
	claim := &OpenPilotInboxClaim{
		StateID: state.ID, Phase: string(state.Phase), RunID: state.RunID, NextAction: openPilotNextAction(state), LastStepName: state.LastStepName,
		LastError: state.LastError, FailingGate: state.FailingGate, OperatorSummary: state.OperatorSummary, RecommendedNextAction: state.RecommendedNextAction,
		Branch: state.Branch, PullRequestURL: state.PullRequestURL, PullRequestNumber: state.PullRequestNum, MergeCommitSHA: state.MergeCommitSHA,
		IssueClosed: state.IssueClosed, RepairAttempts: state.RepairAttempts, MaxRepairAttempts: state.MaxRepairAttempts, UpdatedAt: state.UpdatedAt.UTC(),
	}
	for _, run := range runs {
		if run.WorkflowID != string(WorkflowOpenPilot) || run.Inputs["repo"] != state.Repo || run.Inputs["issue"] != state.Issue {
			continue
		}
		if run.Status == RunQueued || run.Status == RunRunning {
			claim.ActiveRun = true
			claim.RunID, claim.RunStatus = run.ID, string(run.Status)
			break
		}
		if run.ID == state.RunID {
			claim.RunStatus = string(run.Status)
		}
	}
	item.Claim = claim
	item.Links.PullRequest = state.PullRequestURL
	item.Links.State = "/api/open-pilot/state?repo=" + target.RepositorySlug + "&issue=" + target.Issue
	if claim.RunID != "" {
		item.Links.Run = "/api/runs/" + claim.RunID
	}
	if _, err := gate.ValidateState(state); err != nil {
		bound := state.RepositoryID + "/" + state.ExecutorGeneration
		if state.RepositoryID == "" {
			bound = "no affinity"
		}
		item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueAffinityStale, Status: OpenPilotReadinessFail, Message: "the durable issue state is bound to " + bound + " but the registry now resolves " + target.RepositoryID + "/" + target.ExecutorGeneration,
			Remediation: "Do not edit affinity by hand. Finish or pause the issue on its original executor generation, or review it through GET /api/open-pilot/repository-audit."})
		item.Action = OpenPilotInboxActionNone
		return
	}
	switch {
	case claim.ActiveRun:
		item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueRunActive, Status: OpenPilotReadinessFail, Message: "a queued or running Open Pilot run already owns this issue (" + claim.RunID + ")",
			Remediation: "Wait for the run to finish, or cancel it, before dispatching again."})
		item.Action = OpenPilotInboxActionNone
	case state.Phase == OpenPilotPhaseIssueClosed:
		item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueLifecycleComplete, Status: OpenPilotReadinessFail, Message: "the lifecycle completed and the issue was closed",
			Remediation: "No action; open a new issue for further work."})
		item.Action = OpenPilotInboxActionNone
	default:
		message := "durable phase " + string(state.Phase) + "; next action: " + openPilotNextAction(state)
		if state.LastError != "" {
			message += "; last failure: " + state.LastError
		}
		item.add(OpenPilotReadinessCheck{Name: "issue_state", Code: OpenPilotReasonIssueClaimed, Status: OpenPilotReadinessFail, Message: message,
			Remediation: "Continue the existing lifecycle with POST /api/open-pilot/state/resume instead of dispatching a second lifecycle."})
		if isOpenPilotResumablePhase(state.Phase) {
			item.Action = OpenPilotInboxActionResume
		} else {
			item.Action = OpenPilotInboxActionNone
		}
	}
}

func openPilotMissingLabels(actual, required []string) []string {
	set := map[string]struct{}{}
	for _, label := range actual {
		set[strings.ToLower(strings.TrimSpace(label))] = struct{}{}
	}
	missing := []string{}
	for _, label := range required {
		if _, ok := set[strings.ToLower(strings.TrimSpace(label))]; !ok {
			missing = append(missing, label)
		}
	}
	return missing
}

// openPilotIssueSpecReady mirrors validateOpenPilotIssue's specification rule.
func openPilotIssueSpecReady(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "acceptance") && strings.Contains(lower, "test")
}

func openPilotInboxMatches(item OpenPilotInboxIssue, filter OpenPilotInboxFilter) bool {
	switch filter.Eligibility {
	case "eligible":
		if !item.Eligible {
			return false
		}
	case "ineligible":
		if item.Eligible || item.Claim != nil {
			return false
		}
	case "claimed":
		if item.Claim == nil {
			return false
		}
	}
	if filter.Phase != "" && (item.Claim == nil || item.Claim.Phase != filter.Phase) {
		return false
	}
	switch filter.Pickup {
	case "assigned":
		if !item.BotAssigned {
			return false
		}
	case "labels":
		if !item.LabelsSatisfied {
			return false
		}
	case "none":
		if item.BotAssigned || item.LabelsSatisfied {
			return false
		}
	}
	if query := strings.ToLower(strings.TrimSpace(filter.Query)); query != "" {
		haystack := strings.ToLower(item.Key + " " + item.Title + " " + strings.Join(item.Labels, " ") + " " + strings.Join(item.ReasonCodes, " "))
		if !strings.Contains(haystack, query) {
			return false
		}
	}
	return true
}
