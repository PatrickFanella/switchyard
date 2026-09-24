package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Devlog repository evidence (issue #83).
//
// Run inputs `repositories`, `since`, and `until` switch the Devlog
// collect-history step into repository-evidence mode. Only repositories the
// operator already allows (the Open Pilot repository registry and
// DEVLOG_ALLOWED_REPOS) can be read. Collection is read-only, bounded per
// source, and records missing or failed sources instead of inventing activity.
// Without those inputs the run-history mode is unchanged.

const (
	devlogEvidenceVersion           = 1
	devlogEvidenceArtifactName      = "devlog-evidence.json"
	devlogEvidenceProvider          = "gitea"
	devlogMaxEvidenceRepositories   = 8
	devlogMaxEvidenceWindow         = 93 * 24 * time.Hour
	devlogEvidencePageSize          = 50
	devlogEvidenceMaxPages          = 4
	devlogMaxEvidenceItems          = 400
	devlogEvidenceTitleMaxRunes     = 200
	devlogEvidenceRepositoriesInput = "repositories"
	devlogEvidenceSinceInput        = "since"
	devlogEvidenceUntilInput        = "until"
)

// Devlog evidence item kinds.
const (
	DevlogEvidenceKindCommit      = "commit"
	DevlogEvidenceKindPullRequest = "pull_request"
	DevlogEvidenceKindRelease     = "release"
	DevlogEvidenceKindTag         = "tag"
)

// Devlog evidence item statuses. Deployed is claimed only when a release or
// tag points at the item's exact commit.
const (
	DevlogEvidenceMerged   = "merged"
	DevlogEvidenceDeployed = "deployed"
	DevlogEvidenceProposed = "proposed"
	DevlogEvidenceClosed   = "closed"
	DevlogEvidenceReleased = "released"
	DevlogEvidenceTagged   = "tagged"
)

// Devlog evidence source statuses.
const (
	DevlogSourceCollected = "collected"
	DevlogSourceTruncated = "truncated"
	DevlogSourceFailed    = "failed"
	DevlogSourceMissing   = "missing"
)

var devlogEvidenceSources = []string{"commits", "pull_requests", "releases", "tags"}

// DevlogEvidenceWindow is the half-open collection window [since, until).
type DevlogEvidenceWindow struct {
	Since string `json:"since"`
	Until string `json:"until"`
}

// DevlogEvidenceLimits records the collection bounds applied to the report.
type DevlogEvidenceLimits struct {
	MaxRepositories   int `json:"max_repositories"`
	PageSize          int `json:"page_size"`
	MaxPagesPerSource int `json:"max_pages_per_source"`
	MaxItems          int `json:"max_items"`
}

// DevlogEvidenceSource is the outcome of one source for one repository.
type DevlogEvidenceSource struct {
	Source string `json:"source"`
	Status string `json:"status"`
	Items  int    `json:"items"`
	Pages  int    `json:"pages"`
	Error  string `json:"error,omitempty"`
}

// DevlogEvidenceRepository groups source outcomes for one repository.
type DevlogEvidenceRepository struct {
	Repository string                 `json:"repository"`
	Sources    []DevlogEvidenceSource `json:"sources"`
}

// DevlogEvidenceLink is one source link for an item.
type DevlogEvidenceLink struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// DevlogDeploymentEvidence names the release or tag proving deployment.
type DevlogDeploymentEvidence struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	URL  string `json:"url,omitempty"`
}

// DevlogEvidenceItem is one deduplicated activity item with provenance.
type DevlogEvidenceItem struct {
	ID         string                    `json:"id"`
	Repository string                    `json:"repository"`
	Kind       string                    `json:"kind"`
	Status     string                    `json:"status"`
	Title      string                    `json:"title"`
	Number     int                       `json:"number,omitempty"`
	Ref        string                    `json:"ref,omitempty"`
	SHA        string                    `json:"sha,omitempty"`
	Author     string                    `json:"author,omitempty"`
	OccurredAt string                    `json:"occurred_at"`
	SourceURL  string                    `json:"source_url"`
	Links      []DevlogEvidenceLink      `json:"links"`
	Sources    []string                  `json:"sources"`
	Deployment *DevlogDeploymentEvidence `json:"deployment,omitempty"`
	Window     DevlogEvidenceWindow      `json:"window"`

	occurred time.Time
}

// DevlogEvidenceReport is the `devlog-evidence.json` artifact (version 1).
type DevlogEvidenceReport struct {
	Version      int                        `json:"version"`
	Provider     string                     `json:"provider"`
	Window       DevlogEvidenceWindow       `json:"window"`
	Complete     bool                       `json:"complete"`
	Limits       DevlogEvidenceLimits       `json:"limits"`
	Repositories []DevlogEvidenceRepository `json:"repositories"`
	Items        []DevlogEvidenceItem       `json:"items"`
	Missing      []string                   `json:"missing"`
}

type devlogEvidenceRequest struct {
	Repositories []string
	Since        time.Time
	Until        time.Time
}

func (r devlogEvidenceRequest) window() DevlogEvidenceWindow {
	return DevlogEvidenceWindow{Since: r.Since.UTC().Format(time.RFC3339), Until: r.Until.UTC().Format(time.RFC3339)}
}

func (r devlogEvidenceRequest) contains(at time.Time) bool {
	return !at.IsZero() && !at.Before(r.Since) && at.Before(r.Until)
}

// devlogEvidenceInputError is a deterministic input problem. Retrying the same
// run inputs cannot succeed, so the runner pauses instead of retrying.
type devlogEvidenceInputError struct{ message string }

func (e devlogEvidenceInputError) Error() string   { return e.message }
func (e devlogEvidenceInputError) Retryable() bool { return false }

func devlogInputErrorf(format string, args ...any) error {
	return devlogEvidenceInputError{message: "devlog evidence input: " + fmt.Sprintf(format, args...)}
}

// devlogEvidenceRequested reports whether any repository-evidence input is set.
func devlogEvidenceRequested(inputs map[string]string) bool {
	for _, key := range []string{devlogEvidenceRepositoriesInput, devlogEvidenceSinceInput, devlogEvidenceUntilInput} {
		if strings.TrimSpace(inputs[key]) != "" {
			return true
		}
	}
	return false
}

// devlogRepositoryAllowlist resolves requested names to canonical slugs.
type devlogRepositoryAllowlist struct {
	byName map[string]string
}

func (a devlogRepositoryAllowlist) resolve(name string) (string, bool) {
	slug, ok := a.byName[strings.ToLower(strings.TrimSpace(name))]
	return slug, ok
}

func (a devlogRepositoryAllowlist) names() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, slug := range a.byName {
		if !seen[slug] {
			seen[slug] = true
			out = append(out, slug)
		}
	}
	sort.Strings(out)
	return out
}

// devlogRepositoryAllowlist is the Open Pilot repository registry (slugs and
// aliases, resolved to the canonical slug) plus DEVLOG_ALLOWED_REPOS. Run
// inputs can only select from it; they never widen it.
func (e *EnvWorkflowExecutors) devlogRepositoryAllowlist() devlogRepositoryAllowlist {
	allow := devlogRepositoryAllowlist{byName: map[string]string{}}
	if e != nil && e.openPilotGate != nil && e.openPilotGate.registry != nil {
		for _, repository := range e.openPilotGate.registry.Repositories() {
			allow.byName[strings.ToLower(repository.Slug)] = repository.Slug
			for _, alias := range repository.Aliases {
				if _, exists := allow.byName[strings.ToLower(alias)]; !exists {
					allow.byName[strings.ToLower(alias)] = repository.Slug
				}
			}
		}
	}
	configured := ""
	if e != nil {
		configured = e.devlogAllowedRepos
	}
	for _, slug := range splitCSV(configured) {
		if validOpenPilotRepositorySlug(slug) {
			if _, exists := allow.byName[strings.ToLower(slug)]; !exists {
				allow.byName[strings.ToLower(slug)] = slug
			}
		}
	}
	return allow
}

func devlogAllowedReposFromEnv() string {
	return strings.TrimSpace(os.Getenv("DEVLOG_ALLOWED_REPOS"))
}

func parseDevlogEvidenceTime(key, raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, devlogInputErrorf("%s is required when repository evidence is requested", key)
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, devlogInputErrorf("%s must be an RFC 3339 timestamp or YYYY-MM-DD date", key)
}

// parseDevlogEvidenceRequest validates the explicit repository and window
// inputs. ok is false when no evidence input is present.
func parseDevlogEvidenceRequest(inputs map[string]string, allow devlogRepositoryAllowlist) (devlogEvidenceRequest, bool, error) {
	if !devlogEvidenceRequested(inputs) {
		return devlogEvidenceRequest{}, false, nil
	}
	requested := splitCSV(inputs[devlogEvidenceRepositoriesInput])
	if len(requested) == 0 {
		return devlogEvidenceRequest{}, true, devlogInputErrorf("repositories is required when since or until is set")
	}
	allowed := allow.names()
	repositories := []string{}
	seen := map[string]bool{}
	for _, name := range requested {
		slug, ok := allow.resolve(name)
		if !ok {
			if len(allowed) == 0 {
				return devlogEvidenceRequest{}, true, devlogInputErrorf("repository %q is not allowed: no repositories are configured (register it in the Open Pilot repository registry or DEVLOG_ALLOWED_REPOS)", name)
			}
			return devlogEvidenceRequest{}, true, devlogInputErrorf("repository %q is not allowed by configuration; allowed repositories: %s", name, strings.Join(allowed, ", "))
		}
		if !seen[slug] {
			seen[slug] = true
			repositories = append(repositories, slug)
		}
	}
	if len(repositories) > devlogMaxEvidenceRepositories {
		return devlogEvidenceRequest{}, true, devlogInputErrorf("at most %d repositories can be collected in one run", devlogMaxEvidenceRepositories)
	}
	since, err := parseDevlogEvidenceTime(devlogEvidenceSinceInput, inputs[devlogEvidenceSinceInput])
	if err != nil {
		return devlogEvidenceRequest{}, true, err
	}
	until, err := parseDevlogEvidenceTime(devlogEvidenceUntilInput, inputs[devlogEvidenceUntilInput])
	if err != nil {
		return devlogEvidenceRequest{}, true, err
	}
	if !since.Before(until) {
		return devlogEvidenceRequest{}, true, devlogInputErrorf("since must be earlier than until")
	}
	if until.Sub(since) > devlogMaxEvidenceWindow {
		return devlogEvidenceRequest{}, true, devlogInputErrorf("the window must not exceed %d days", int(devlogMaxEvidenceWindow/(24*time.Hour)))
	}
	return devlogEvidenceRequest{Repositories: repositories, Since: since, Until: until}, true, nil
}

// collectDevlogEvidence reads bounded activity for each repository. Source
// failures and absent configuration are recorded in the report; the function
// itself fails only on context cancellation.
func collectDevlogEvidence(ctx context.Context, reader GiteaRepositoryActivityReader, request devlogEvidenceRequest) (DevlogEvidenceReport, error) {
	window := request.window()
	report := DevlogEvidenceReport{
		Version:  devlogEvidenceVersion,
		Provider: devlogEvidenceProvider,
		Window:   window,
		Limits:   DevlogEvidenceLimits{MaxRepositories: devlogMaxEvidenceRepositories, PageSize: devlogEvidencePageSize, MaxPagesPerSource: devlogEvidenceMaxPages, MaxItems: devlogMaxEvidenceItems},
		Items:    []DevlogEvidenceItem{},
		Missing:  []string{},
	}
	access := GiteaAccess{}
	unavailable := ""
	switch {
	case reader == nil:
		unavailable = "no Gitea activity reader is configured"
	default:
		if reporter, ok := reader.(GiteaAccessReporter); ok {
			access = reporter.GiteaAccess()
			if !access.BaseURLConfigured {
				unavailable = "Gitea origin is not configured (GITEA_BASE_URL)"
			} else if !access.TokenConfigured {
				unavailable = "Gitea credentials are absent (GITEA_TOKEN)"
			}
		}
	}
	for _, repo := range request.Repositories {
		entry := DevlogEvidenceRepository{Repository: repo, Sources: []DevlogEvidenceSource{}}
		if unavailable != "" {
			for _, source := range devlogEvidenceSources {
				entry.Sources = append(entry.Sources, DevlogEvidenceSource{Source: source, Status: DevlogSourceMissing, Error: unavailable})
			}
			report.Missing = append(report.Missing, repo+": all sources missing: "+unavailable)
			report.Repositories = append(report.Repositories, entry)
			continue
		}
		items, sources, err := collectDevlogRepositoryEvidence(ctx, reader, access, repo, request)
		if err != nil {
			return DevlogEvidenceReport{}, err
		}
		entry.Sources = sources
		for _, source := range sources {
			switch source.Status {
			case DevlogSourceFailed:
				report.Missing = append(report.Missing, repo+": "+source.Source+" failed: "+source.Error)
			case DevlogSourceTruncated:
				report.Missing = append(report.Missing, repo+": "+source.Source+" truncated after "+strconv.Itoa(source.Pages)+" pages; older activity in the window may be absent")
			}
		}
		report.Items = append(report.Items, items...)
		report.Repositories = append(report.Repositories, entry)
	}
	sortDevlogEvidenceItems(report.Items)
	if len(report.Items) > devlogMaxEvidenceItems {
		report.Missing = append(report.Missing, fmt.Sprintf("report truncated to %d of %d items", devlogMaxEvidenceItems, len(report.Items)))
		report.Items = report.Items[:devlogMaxEvidenceItems]
	}
	report.Complete = len(report.Missing) == 0
	return report, nil
}

func sortDevlogEvidenceItems(items []DevlogEvidenceItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].occurred.Equal(items[j].occurred) {
			return items[i].occurred.After(items[j].occurred)
		}
		return items[i].ID < items[j].ID
	})
}

type devlogPageFunc[T any] func(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]T, error)

// collectDevlogPages reads at most devlogEvidenceMaxPages pages. stop reports
// that the rest of the listing is older than the window.
func collectDevlogPages[T any](ctx context.Context, repo, source string, request devlogEvidenceRequest, list devlogPageFunc[T], stop func([]T) bool) ([]T, DevlogEvidenceSource, error) {
	result := DevlogEvidenceSource{Source: source, Status: DevlogSourceCollected}
	var all []T
	for page := 1; page <= devlogEvidenceMaxPages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, result, err
		}
		items, err := list(ctx, repo, GiteaActivityPageQuery{Page: page, Limit: devlogEvidencePageSize, Since: request.Since, Until: request.Until})
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, result, ctxErr
			}
			result.Status = DevlogSourceFailed
			result.Error = RedactForLog(err.Error())
			return nil, result, nil
		}
		result.Pages = page
		all = append(all, items...)
		if len(items) < devlogEvidencePageSize || (stop != nil && stop(items)) {
			return all, result, nil
		}
	}
	result.Status = DevlogSourceTruncated
	return all, result, nil
}

func devlogTime(values ...*time.Time) time.Time {
	for _, value := range values {
		if value != nil && !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}

func devlogTitle(raw string) string {
	line := strings.TrimSpace(strings.SplitN(strings.ReplaceAll(raw, "\r", ""), "\n", 2)[0])
	line = RedactForLog(line)
	runes := []rune(line)
	if len(runes) > devlogEvidenceTitleMaxRunes {
		line = string(runes[:devlogEvidenceTitleMaxRunes]) + "…"
	}
	if line == "" {
		return "(untitled)"
	}
	return line
}

func devlogWebURL(access GiteaAccess, repo string, parts ...string) string {
	if access.WebBaseURL == "" {
		return ""
	}
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		escaped = append(escaped, url.PathEscape(part))
	}
	return access.WebBaseURL + "/" + repo + "/" + strings.Join(escaped, "/")
}

func collectDevlogRepositoryEvidence(ctx context.Context, reader GiteaRepositoryActivityReader, access GiteaAccess, repo string, request devlogEvidenceRequest) ([]DevlogEvidenceItem, []DevlogEvidenceSource, error) {
	window := request.window()
	sources := make([]DevlogEvidenceSource, 0, len(devlogEvidenceSources))

	commits, commitSource, err := collectDevlogPages(ctx, repo, "commits", request, reader.ListCommitActivity, nil)
	if err != nil {
		return nil, nil, err
	}
	pulls, pullSource, err := collectDevlogPages(ctx, repo, "pull_requests", request, reader.ListPullRequestActivity, func(page []GiteaPullRequestActivity) bool {
		// Sorted by most recent update: once a whole page predates the
		// window, no later page can contain in-window activity.
		for _, pr := range page {
			if updated := devlogTime(pr.UpdatedAt, pr.CreatedAt); updated.IsZero() || !updated.Before(request.Since) {
				return false
			}
		}
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	releases, releaseSource, err := collectDevlogPages(ctx, repo, "releases", request, reader.ListReleaseActivity, func(page []GiteaReleaseActivity) bool {
		for _, release := range page {
			if created := devlogTime(release.PublishedAt, release.CreatedAt); created.IsZero() || !created.Before(request.Since) {
				return false
			}
		}
		return true
	})
	if err != nil {
		return nil, nil, err
	}
	tags, tagSource, err := collectDevlogPages(ctx, repo, "tags", request, reader.ListTagActivity, nil)
	if err != nil {
		return nil, nil, err
	}

	items := map[string]*DevlogEvidenceItem{}
	order := []string{}
	add := func(item DevlogEvidenceItem) *DevlogEvidenceItem {
		if existing, ok := items[item.ID]; ok {
			return existing
		}
		item.Window = window
		stored := item
		items[item.ID] = &stored
		order = append(order, item.ID)
		return &stored
	}

	// Deployment evidence: exact commit identity of every observed tag, with
	// the release that published it when one exists. Draft releases are not
	// evidence.
	deployments := map[string]DevlogDeploymentEvidence{}
	tagSHA := map[string]string{}
	for _, tag := range tags {
		sha := strings.TrimSpace(firstNonEmpty(tag.Commit.SHA, tag.ID))
		if tag.Name == "" || sha == "" {
			continue
		}
		tagSHA[tag.Name] = sha
		if _, ok := deployments[sha]; !ok {
			deployments[sha] = DevlogDeploymentEvidence{Kind: DevlogEvidenceKindTag, Ref: tag.Name, SHA: sha, URL: devlogWebURL(access, repo, "src", "tag", tag.Name)}
		}
	}
	sortedReleases := append([]GiteaReleaseActivity(nil), releases...)
	sort.SliceStable(sortedReleases, func(i, j int) bool {
		return devlogTime(sortedReleases[i].PublishedAt, sortedReleases[i].CreatedAt).Before(devlogTime(sortedReleases[j].PublishedAt, sortedReleases[j].CreatedAt))
	})
	for _, release := range sortedReleases {
		sha := tagSHA[release.TagName]
		if release.Draft || sha == "" {
			continue
		}
		if existing, ok := deployments[sha]; !ok || existing.Kind != DevlogEvidenceKindRelease {
			deployments[sha] = DevlogDeploymentEvidence{Kind: DevlogEvidenceKindRelease, Ref: release.TagName, SHA: sha, URL: release.HTMLURL}
		}
	}
	markDeployed := func(item *DevlogEvidenceItem, sha string) {
		if sha == "" {
			return
		}
		if evidence, ok := deployments[sha]; ok {
			evidence := evidence
			item.Status = DevlogEvidenceDeployed
			item.Deployment = &evidence
		}
	}

	// Pull requests first so commits that belong to them fold into one item.
	pullCount := 0
	prCommits := map[string]*DevlogEvidenceItem{}
	for _, pr := range pulls {
		if pr.Number < 1 {
			continue
		}
		created := devlogTime(pr.CreatedAt)
		merged := devlogTime(pr.MergedAt)
		closed := devlogTime(pr.ClosedAt)
		updated := devlogTime(pr.UpdatedAt)
		var status string
		var occurred time.Time
		switch {
		case pr.Merged && request.contains(merged):
			status, occurred = DevlogEvidenceMerged, merged
		case !pr.Merged && strings.EqualFold(pr.State, "closed") && request.contains(closed):
			status, occurred = DevlogEvidenceClosed, closed
		case request.contains(created):
			status, occurred = DevlogEvidenceProposed, created
		case strings.EqualFold(pr.State, "open") && request.contains(updated):
			status, occurred = DevlogEvidenceProposed, updated
		default:
			continue
		}
		sourceURL := firstNonEmpty(pr.HTMLURL, devlogWebURL(access, repo, "pulls", strconv.Itoa(pr.Number)), pr.URL)
		author := ""
		if pr.User != nil {
			author = pr.User.Login
		}
		id := repo + "#pr/" + strconv.Itoa(pr.Number)
		if _, exists := items[id]; exists {
			continue
		}
		item := add(DevlogEvidenceItem{
			ID: id, Repository: repo, Kind: DevlogEvidenceKindPullRequest, Status: status, Title: devlogTitle(pr.Title), Number: pr.Number,
			Ref: pr.Base.Ref, Author: author, OccurredAt: occurred.Format(time.RFC3339), occurred: occurred,
			SourceURL: sourceURL, Links: []DevlogEvidenceLink{{Kind: DevlogEvidenceKindPullRequest, URL: sourceURL}}, Sources: []string{"pull_requests"},
		})
		pullCount++
		if status == DevlogEvidenceMerged {
			item.SHA = pr.MergeCommitSHA
			markDeployed(item, pr.MergeCommitSHA)
			if pr.MergeCommitSHA != "" {
				prCommits[pr.MergeCommitSHA] = item
			}
			if pr.Head.SHA != "" {
				if _, taken := prCommits[pr.Head.SHA]; !taken {
					prCommits[pr.Head.SHA] = item
				}
			}
		}
	}

	commitCount := 0
	for _, commit := range commits {
		sha := strings.TrimSpace(commit.SHA)
		if sha == "" {
			continue
		}
		occurred := commit.Commit.Committer.Date.UTC()
		if occurred.IsZero() {
			occurred = commit.Commit.Author.Date.UTC()
		}
		if !request.contains(occurred) {
			continue
		}
		sourceURL := firstNonEmpty(commit.HTMLURL, devlogWebURL(access, repo, "commit", sha))
		if owner, ok := prCommits[sha]; ok {
			owner.Links = appendDevlogLink(owner.Links, DevlogEvidenceLink{Kind: DevlogEvidenceKindCommit, URL: sourceURL})
			owner.Sources = appendDevlogSource(owner.Sources, "commits")
			commitCount++
			continue
		}
		id := repo + "@" + sha
		if _, exists := items[id]; exists {
			continue
		}
		author := commit.Commit.Author.Name
		if commit.Author != nil && commit.Author.Login != "" {
			author = commit.Author.Login
		}
		item := add(DevlogEvidenceItem{
			ID: id, Repository: repo, Kind: DevlogEvidenceKindCommit, Status: DevlogEvidenceMerged, Title: devlogTitle(commit.Commit.Message), SHA: sha,
			Author: author, OccurredAt: occurred.Format(time.RFC3339), occurred: occurred,
			SourceURL: sourceURL, Links: []DevlogEvidenceLink{{Kind: DevlogEvidenceKindCommit, URL: sourceURL}}, Sources: []string{"commits"},
		})
		markDeployed(item, sha)
		commitCount++
	}

	releaseCount := 0
	releaseItems := map[string]*DevlogEvidenceItem{}
	for _, release := range releases {
		occurred := devlogTime(release.PublishedAt, release.CreatedAt)
		if release.Draft || release.TagName == "" || !request.contains(occurred) {
			continue
		}
		id := repo + "@release/" + release.TagName
		if _, exists := items[id]; exists {
			continue
		}
		sourceURL := firstNonEmpty(release.HTMLURL, devlogWebURL(access, repo, "releases", "tag", release.TagName))
		title := devlogTitle(firstNonEmpty(release.Name, release.TagName))
		if release.Prerelease {
			title += " (pre-release)"
		}
		item := add(DevlogEvidenceItem{
			ID: id, Repository: repo, Kind: DevlogEvidenceKindRelease, Status: DevlogEvidenceReleased, Title: title, Ref: release.TagName, SHA: tagSHA[release.TagName],
			OccurredAt: occurred.Format(time.RFC3339), occurred: occurred,
			SourceURL: sourceURL, Links: []DevlogEvidenceLink{{Kind: DevlogEvidenceKindRelease, URL: sourceURL}}, Sources: []string{"releases"},
		})
		releaseItems[release.TagName] = item
		releaseCount++
	}

	tagCount := 0
	for _, tag := range tags {
		occurred := devlogTime(tag.Commit.Created)
		sha := strings.TrimSpace(firstNonEmpty(tag.Commit.SHA, tag.ID))
		if tag.Name == "" {
			continue
		}
		sourceURL := firstNonEmpty(devlogWebURL(access, repo, "src", "tag", tag.Name), tag.Commit.URL)
		if release, ok := releaseItems[tag.Name]; ok {
			if sourceURL != "" {
				release.Links = appendDevlogLink(release.Links, DevlogEvidenceLink{Kind: DevlogEvidenceKindTag, URL: sourceURL})
			}
			release.Sources = appendDevlogSource(release.Sources, "tags")
			tagCount++
			continue
		}
		if !request.contains(occurred) || sourceURL == "" {
			continue
		}
		id := repo + "@tag/" + tag.Name
		if _, exists := items[id]; exists {
			continue
		}
		add(DevlogEvidenceItem{
			ID: id, Repository: repo, Kind: DevlogEvidenceKindTag, Status: DevlogEvidenceTagged, Title: devlogTitle(tag.Name), Ref: tag.Name, SHA: sha,
			OccurredAt: occurred.Format(time.RFC3339), occurred: occurred,
			SourceURL: sourceURL, Links: []DevlogEvidenceLink{{Kind: DevlogEvidenceKindTag, URL: sourceURL}}, Sources: []string{"tags"},
		})
		tagCount++
	}

	commitSource.Items = commitCount
	pullSource.Items = pullCount
	releaseSource.Items = releaseCount
	tagSource.Items = tagCount
	sources = append(sources, commitSource, pullSource, releaseSource, tagSource)

	out := make([]DevlogEvidenceItem, 0, len(order))
	for _, id := range order {
		item := *items[id]
		if item.SourceURL == "" {
			// Provenance is mandatory: an item without any link is not reported.
			continue
		}
		out = append(out, item)
	}
	return out, sources, nil
}

func appendDevlogLink(links []DevlogEvidenceLink, link DevlogEvidenceLink) []DevlogEvidenceLink {
	if link.URL == "" {
		return links
	}
	for _, existing := range links {
		if existing.URL == link.URL {
			return links
		}
	}
	return append(links, link)
}

func appendDevlogSource(sources []string, source string) []string {
	for _, existing := range sources {
		if existing == source {
			return sources
		}
	}
	return append(sources, source)
}

func (r DevlogEvidenceReport) JSON() string {
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(encoded) + "\n"
}

func (r DevlogEvidenceReport) repositoryNames() []string {
	out := make([]string, 0, len(r.Repositories))
	for _, repository := range r.Repositories {
		out = append(out, repository.Repository)
	}
	return out
}

// Markdown renders the deterministic evidence section. Every item line carries
// its source link and the collection window.
func (r DevlogEvidenceReport) Markdown() string {
	var b strings.Builder
	window := r.Window.Since + " to " + r.Window.Until
	b.WriteString("## Repository evidence\n\n")
	b.WriteString("Window: " + window + " (start inclusive, end exclusive). Source: Gitea API, read-only. ")
	b.WriteString("A change is marked deployed only when a release or tag points at its exact commit.\n")
	statusOrder := []struct{ status, heading string }{
		{DevlogEvidenceDeployed, "Deployed (release or tag at the exact commit)"},
		{DevlogEvidenceMerged, "Merged, no deployment evidence"},
		{DevlogEvidenceProposed, "Proposed (open or unmerged in the window)"},
		{DevlogEvidenceClosed, "Closed without merge"},
		{DevlogEvidenceReleased, "Releases"},
		{DevlogEvidenceTagged, "Tags"},
	}
	for _, repository := range r.Repositories {
		b.WriteString("\n### " + repository.Repository + "\n\n")
		parts := make([]string, 0, len(repository.Sources))
		for _, source := range repository.Sources {
			part := source.Source + " " + source.Status
			if source.Status == DevlogSourceCollected || source.Status == DevlogSourceTruncated {
				part += " (" + strconv.Itoa(source.Items) + ")"
			}
			parts = append(parts, part)
		}
		b.WriteString("Sources: " + strings.Join(parts, ", ") + ".\n")
		found := false
		for _, group := range statusOrder {
			var lines []string
			for _, item := range r.Items {
				if item.Repository != repository.Repository || item.Status != group.status {
					continue
				}
				lines = append(lines, devlogEvidenceLine(item))
			}
			if len(lines) == 0 {
				continue
			}
			found = true
			b.WriteString("\n" + group.heading + ":\n\n")
			for _, line := range lines {
				b.WriteString(line + "\n")
			}
		}
		if !found {
			b.WriteString("\nNo activity was found in the collected sources for this window.\n")
		}
	}
	if len(r.Missing) > 0 {
		b.WriteString("\n### Missing or incomplete sources\n\n")
		for _, missing := range r.Missing {
			b.WriteString("- " + missing + "\n")
		}
	}
	return b.String()
}

func devlogEvidenceLine(item DevlogEvidenceItem) string {
	label := item.Title
	switch item.Kind {
	case DevlogEvidenceKindPullRequest:
		label = "PR #" + strconv.Itoa(item.Number) + ": " + item.Title
	case DevlogEvidenceKindCommit:
		label = "Commit " + shortDevlogSHA(item.SHA) + ": " + item.Title
	case DevlogEvidenceKindRelease:
		label = "Release " + item.Ref + ": " + item.Title
	case DevlogEvidenceKindTag:
		label = "Tag " + item.Ref
	}
	line := "- " + strings.NewReplacer("[", "(", "]", ")").Replace(label) + " — [source](" + item.SourceURL + ") · " + item.OccurredAt
	if item.Deployment != nil {
		line += " · deployed via " + item.Deployment.Kind + " " + item.Deployment.Ref
		if item.Deployment.URL != "" {
			line += " ([" + item.Deployment.Kind + "](" + item.Deployment.URL + "))"
		}
	}
	line += " · window " + item.Window.Since + "/" + item.Window.Until
	return line
}

func shortDevlogSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// devlogEvidenceOutputs are the named step outputs published by
// collect-history in evidence mode.
func devlogEvidenceOutputs(report DevlogEvidenceReport) map[string]string {
	encoded := report.JSON()
	sum := sha256.Sum256([]byte(encoded))
	return map[string]string{
		"evidence_json":         encoded,
		"evidence_sha256":       hex.EncodeToString(sum[:]),
		"evidence_items":        strconv.Itoa(len(report.Items)),
		"evidence_complete":     strconv.FormatBool(report.Complete),
		"evidence_missing":      strconv.Itoa(len(report.Missing)),
		"evidence_repositories": strings.Join(report.repositoryNames(), ","),
		"evidence_since":        report.Window.Since,
		"evidence_until":        report.Window.Until,
		"artifact_name":         devlogEvidenceArtifactName,
	}
}

func devlogEvidenceEvents(report DevlogEvidenceReport) []WorkflowExecutionEvent {
	events := []WorkflowExecutionEvent{{
		Type:    "devlog.evidence.collected",
		Message: fmt.Sprintf("Collected %d evidence items from %d repositories for window %s/%s", len(report.Items), len(report.Repositories), report.Window.Since, report.Window.Until),
	}}
	for index, missing := range report.Missing {
		if index == 10 {
			events = append(events, WorkflowExecutionEvent{Type: "devlog.evidence.missing", Message: fmt.Sprintf("%d more missing or incomplete sources are listed in %s", len(report.Missing)-index, devlogEvidenceArtifactName)})
			break
		}
		events = append(events, WorkflowExecutionEvent{Type: "devlog.evidence.missing", Message: missing})
	}
	return events
}

func devlogEvidenceArtifact(report DevlogEvidenceReport) CreateArtifactInput {
	return CreateArtifactInput{Name: devlogEvidenceArtifactName, Kind: "json", URI: inlineArtifactURI("application/json;charset=utf-8", report.JSON())}
}

// devlogCollection is the collect-history result shared by every Devlog path.
type devlogCollection struct {
	seed     string
	evidence *DevlogEvidenceReport
}

func (c devlogCollection) summary() string {
	if c.evidence == nil {
		return "Collected recent Switchyard run history"
	}
	if c.evidence.Complete {
		return fmt.Sprintf("Collected recent Switchyard run history and %d repository evidence items", len(c.evidence.Items))
	}
	return fmt.Sprintf("Collected recent Switchyard run history and %d repository evidence items; %d sources missing or incomplete", len(c.evidence.Items), len(c.evidence.Missing))
}

func (c devlogCollection) generatedMessage() string {
	if c.evidence == nil {
		return "Generated devlog from Switchyard run history"
	}
	return "Generated devlog from Switchyard run history and repository evidence"
}

// finalMarkdown keeps provenance when a model rewrote the seed: the
// deterministic evidence section is appended as the source record.
func (c devlogCollection) finalMarkdown(markdown string, generated bool) string {
	if c.evidence == nil || !generated {
		return markdown
	}
	return strings.TrimRight(markdown, "\n") + "\n\n---\n\nSource record:\n\n" + c.evidence.Markdown()
}

func (e *EnvWorkflowExecutors) devlogActivityReader() GiteaRepositoryActivityReader {
	if e == nil {
		return nil
	}
	return e.devlogEvidenceReader
}

// collectDevlog builds the seed Markdown and, when requested, the repository
// evidence report. Without evidence inputs the result is exactly the
// run-history seed.
func (e *EnvWorkflowExecutors) collectDevlog(ctx context.Context, run Run) (devlogCollection, error) {
	seed := e.buildDevlogMarkdown(run)
	request, ok, err := parseDevlogEvidenceRequest(run.Inputs, e.devlogRepositoryAllowlist())
	if err != nil {
		return devlogCollection{}, err
	}
	if !ok {
		return devlogCollection{seed: seed}, nil
	}
	report, err := collectDevlogEvidence(ctx, e.devlogActivityReader(), request)
	if err != nil {
		return devlogCollection{}, err
	}
	return devlogCollection{seed: strings.TrimRight(seed, "\n") + "\n\n" + report.Markdown(), evidence: &report}, nil
}
