package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeDevlogActivityReader serves fixed Gitea fixtures page by page.
type fakeDevlogActivityReader struct {
	access   *GiteaAccess
	commits  map[string][][]GiteaCommitActivity
	pulls    map[string][][]GiteaPullRequestActivity
	releases map[string][][]GiteaReleaseActivity
	tags     map[string][][]GiteaTagActivity
	errs     map[string]error
	calls    map[string]int
	queries  []GiteaActivityPageQuery
}

func newFakeDevlogActivityReader() *fakeDevlogActivityReader {
	return &fakeDevlogActivityReader{
		access:   &GiteaAccess{BaseURLConfigured: true, TokenConfigured: true, WebBaseURL: "https://git.example.test"},
		commits:  map[string][][]GiteaCommitActivity{},
		pulls:    map[string][][]GiteaPullRequestActivity{},
		releases: map[string][][]GiteaReleaseActivity{},
		tags:     map[string][][]GiteaTagActivity{},
		errs:     map[string]error{},
		calls:    map[string]int{},
	}
}

func (f *fakeDevlogActivityReader) GiteaAccess() GiteaAccess {
	if f.access == nil {
		return GiteaAccess{}
	}
	return *f.access
}

func fakeDevlogPage[T any](f *fakeDevlogActivityReader, source, repo string, pages map[string][][]T, query GiteaActivityPageQuery) ([]T, error) {
	f.calls[repo+"/"+source]++
	f.queries = append(f.queries, query)
	if err := f.errs[repo+"/"+source]; err != nil {
		return nil, err
	}
	if query.Page < 1 || query.Page > len(pages[repo]) {
		return nil, nil
	}
	return append([]T(nil), pages[repo][query.Page-1]...), nil
}

func (f *fakeDevlogActivityReader) ListCommitActivity(_ context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaCommitActivity, error) {
	return fakeDevlogPage(f, "commits", repo, f.commits, query)
}

func (f *fakeDevlogActivityReader) ListPullRequestActivity(_ context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaPullRequestActivity, error) {
	return fakeDevlogPage(f, "pulls", repo, f.pulls, query)
}

func (f *fakeDevlogActivityReader) ListReleaseActivity(_ context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaReleaseActivity, error) {
	return fakeDevlogPage(f, "releases", repo, f.releases, query)
}

func (f *fakeDevlogActivityReader) ListTagActivity(_ context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaTagActivity, error) {
	return fakeDevlogPage(f, "tags", repo, f.tags, query)
}

func devlogTestTime(day int, hour int) time.Time {
	return time.Date(2026, 9, day, hour, 0, 0, 0, time.UTC)
}

func devlogTimePtr(value time.Time) *time.Time { return &value }

func devlogTestCommit(repo, sha, message string, at time.Time) GiteaCommitActivity {
	var commit GiteaCommitActivity
	commit.SHA = sha
	commit.HTMLURL = "https://git.example.test/" + repo + "/commit/" + sha
	commit.Commit.Message = message
	commit.Commit.Author.Name = "Dev"
	commit.Commit.Author.Date = at
	commit.Commit.Committer.Date = at
	return commit
}

func devlogTestPull(repo string, number int, title, state string, merged bool, created, updated time.Time, mergedAt, closedAt *time.Time, mergeSHA, headSHA string) GiteaPullRequestActivity {
	pr := GiteaPullRequestActivity{CreatedAt: devlogTimePtr(created), UpdatedAt: devlogTimePtr(updated), MergedAt: mergedAt, ClosedAt: closedAt}
	pr.Number = number
	pr.ID = 1000 + number
	pr.Title = title
	pr.State = state
	pr.Merged = merged
	pr.HTMLURL = fmt.Sprintf("https://git.example.test/%s/pulls/%d", repo, number)
	pr.MergeCommitSHA = mergeSHA
	pr.Head.SHA = headSHA
	pr.Base.Ref = "main"
	return pr
}

func devlogTestTag(name, sha string, created time.Time) GiteaTagActivity {
	var tag GiteaTagActivity
	tag.Name = name
	tag.ID = "tagobj-" + name
	tag.Commit.SHA = sha
	tag.Commit.URL = "https://git.example.test/api/v1/repos/owner/alpha/git/commits/" + sha
	tag.Commit.Created = devlogTimePtr(created)
	return tag
}

// seedDevlogAlphaFixture builds owner/alpha activity for the window
// 2026-09-10T00:00Z .. 2026-09-17T00:00Z.
func seedDevlogAlphaFixture(reader *fakeDevlogActivityReader) {
	repo := "owner/alpha"
	reader.commits[repo] = [][]GiteaCommitActivity{{
		devlogTestCommit(repo, "c1merge", "Merge pull request 'Add feature' (#10)", devlogTestTime(12, 9)),
		devlogTestCommit(repo, "c2direct", "Fix typo in README\n\nlong body", devlogTestTime(13, 9)),
		devlogTestCommit(repo, "c3plain", "Refactor token=supersecretvalue handling", devlogTestTime(11, 9)),
		devlogTestCommit(repo, "c0old", "Before the window", devlogTestTime(9, 23)),
	}}
	reader.pulls[repo] = [][]GiteaPullRequestActivity{{
		devlogTestPull(repo, 10, "Add feature", "closed", true, devlogTestTime(10, 8), devlogTestTime(12, 9), devlogTimePtr(devlogTestTime(12, 9)), devlogTimePtr(devlogTestTime(12, 9)), "c1merge", "c1head"),
		devlogTestPull(repo, 11, "Draft idea", "open", false, devlogTestTime(14, 8), devlogTestTime(15, 8), nil, nil, "", "c11head"),
		devlogTestPull(repo, 12, "Abandoned change", "closed", false, devlogTestTime(9, 8), devlogTestTime(12, 8), nil, devlogTimePtr(devlogTestTime(12, 8)), "", "c12head"),
		devlogTestPull(repo, 9, "Old merge", "closed", true, devlogTestTime(1, 8), devlogTestTime(2, 8), devlogTimePtr(devlogTestTime(2, 8)), devlogTimePtr(devlogTestTime(2, 8)), "c9merge", "c9head"),
	}}
	reader.releases[repo] = [][]GiteaReleaseActivity{{
		{ID: 1, TagName: "v1.0.0", TargetCommitish: "main", Name: "Version 1.0.0", HTMLURL: "https://git.example.test/owner/alpha/releases/tag/v1.0.0", PublishedAt: devlogTimePtr(devlogTestTime(14, 12)), CreatedAt: devlogTimePtr(devlogTestTime(14, 12))},
		{ID: 2, TagName: "v1.1.0-draft", TargetCommitish: "main", Name: "Draft", Draft: true, HTMLURL: "https://git.example.test/owner/alpha/releases/tag/v1.1.0-draft", CreatedAt: devlogTimePtr(devlogTestTime(15, 12))},
	}}
	reader.tags[repo] = [][]GiteaTagActivity{{
		devlogTestTag("v1.0.0", "c2direct", devlogTestTime(13, 9)),
		devlogTestTag("nightly-2026-09-11", "c3plain", devlogTestTime(11, 9)),
	}}
}

func devlogTestRequest(repos ...string) devlogEvidenceRequest {
	return devlogEvidenceRequest{Repositories: repos, Since: devlogTestTime(10, 0), Until: devlogTestTime(17, 0)}
}

func devlogItemByID(t *testing.T, report DevlogEvidenceReport, id string) DevlogEvidenceItem {
	t.Helper()
	for _, item := range report.Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("item %s missing from %+v", id, report.Items)
	return DevlogEvidenceItem{}
}

func devlogHasItem(report DevlogEvidenceReport, id string) bool {
	for _, item := range report.Items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func TestDevlogEvidenceClassifiesDeduplicatesAndLinksFixedFixtures(t *testing.T) {
	reader := newFakeDevlogActivityReader()
	seedDevlogAlphaFixture(reader)
	report, err := collectDevlogEvidence(t.Context(), reader, devlogTestRequest("owner/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Missing) != 0 || report.Version != 1 || report.Provider != "gitea" {
		t.Fatalf("report header = %+v", report)
	}
	// PR 10 absorbs its merge commit; only an unpublished draft release exists
	// after it, which is not deployment evidence.
	pr10 := devlogItemByID(t, report, "owner/alpha#pr/10")
	if pr10.Status != DevlogEvidenceMerged || pr10.Deployment != nil || pr10.SHA != "c1merge" {
		t.Fatalf("pr10 = %+v", pr10)
	}
	if len(pr10.Links) != 2 || pr10.Links[1].URL != "https://git.example.test/owner/alpha/commit/c1merge" || strings.Join(pr10.Sources, ",") != "pull_requests,commits" {
		t.Fatalf("pr10 links/sources = %+v %+v", pr10.Links, pr10.Sources)
	}
	if devlogHasItem(report, "owner/alpha@c1merge") {
		t.Fatal("merge commit must be deduplicated into its pull request")
	}
	// The direct commit is the exact target of published release v1.0.0.
	direct := devlogItemByID(t, report, "owner/alpha@c2direct")
	if direct.Status != DevlogEvidenceDeployed || direct.Deployment == nil || direct.Deployment.Kind != "release" || direct.Deployment.Ref != "v1.0.0" || direct.Title != "Fix typo in README" {
		t.Fatalf("direct = %+v deployment=%+v", direct, direct.Deployment)
	}
	// A lightweight tag is deployment evidence too, but only for its exact commit.
	plain := devlogItemByID(t, report, "owner/alpha@c3plain")
	if plain.Status != DevlogEvidenceDeployed || plain.Deployment == nil || plain.Deployment.Kind != "tag" || strings.Contains(plain.Title, "supersecretvalue") {
		t.Fatalf("plain = %+v", plain)
	}
	if got := devlogItemByID(t, report, "owner/alpha#pr/11").Status; got != DevlogEvidenceProposed {
		t.Fatalf("open PR status = %s", got)
	}
	if got := devlogItemByID(t, report, "owner/alpha#pr/12").Status; got != DevlogEvidenceClosed {
		t.Fatalf("closed PR status = %s", got)
	}
	release := devlogItemByID(t, report, "owner/alpha@release/v1.0.0")
	if release.Status != DevlogEvidenceReleased || release.SHA != "c2direct" || len(release.Links) != 2 || release.Links[1].Kind != "tag" {
		t.Fatalf("release = %+v", release)
	}
	if devlogHasItem(report, "owner/alpha@tag/v1.0.0") || devlogHasItem(report, "owner/alpha@release/v1.1.0-draft") {
		t.Fatal("release tag must fold into its release and draft releases must be skipped")
	}
	if !devlogHasItem(report, "owner/alpha@tag/nightly-2026-09-11") {
		t.Fatal("in-window tag without release is reported")
	}
	for _, excluded := range []string{"owner/alpha#pr/9", "owner/alpha@c0old"} {
		if devlogHasItem(report, excluded) {
			t.Fatalf("%s is outside the window", excluded)
		}
	}
	for index := 1; index < len(report.Items); index++ {
		if report.Items[index-1].OccurredAt < report.Items[index].OccurredAt {
			t.Fatalf("items are not newest first: %+v", report.Items)
		}
	}
}

func TestDevlogEvidenceProvenanceOnEveryItemAndOutput(t *testing.T) {
	reader := newFakeDevlogActivityReader()
	seedDevlogAlphaFixture(reader)
	report, err := collectDevlogEvidence(t.Context(), reader, devlogTestRequest("owner/alpha"))
	if err != nil {
		t.Fatal(err)
	}
	want := DevlogEvidenceWindow{Since: "2026-09-10T00:00:00Z", Until: "2026-09-17T00:00:00Z"}
	if report.Window != want {
		t.Fatalf("window = %+v", report.Window)
	}
	var decoded DevlogEvidenceReport
	if err := json.Unmarshal([]byte(report.JSON()), &decoded); err != nil {
		t.Fatal(err)
	}
	markdown := report.Markdown()
	for _, item := range decoded.Items {
		if item.SourceURL == "" || !strings.HasPrefix(item.SourceURL, "https://git.example.test/") || item.Window != want || len(item.Links) == 0 || item.Links[0].URL != item.SourceURL {
			t.Fatalf("item provenance = %+v", item)
		}
		if !strings.Contains(markdown, "[source]("+item.SourceURL+")") {
			t.Fatalf("markdown omits source for %s:\n%s", item.ID, markdown)
		}
	}
	for _, line := range strings.Split(markdown, "\n") {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, "[source](") && !strings.Contains(line, "window "+want.Since+"/"+want.Until) {
			t.Fatalf("markdown item line lacks window: %q", line)
		}
	}
	outputs := devlogEvidenceOutputs(report)
	if outputs["evidence_since"] != want.Since || outputs["evidence_until"] != want.Until || outputs["evidence_items"] != fmt.Sprint(len(report.Items)) || outputs["evidence_complete"] != "true" || outputs["artifact_name"] != "devlog-evidence.json" || len(outputs["evidence_sha256"]) != 64 {
		t.Fatalf("outputs = %+v", outputs)
	}
	if _, _, err := boundStepOutputs("collect-history", outputs); err != nil {
		t.Fatalf("outputs violate the step output rules: %v", err)
	}
	artifact := devlogEvidenceArtifact(report)
	if artifact.Name != "devlog-evidence.json" || artifact.Kind != "json" {
		t.Fatalf("artifact = %+v", artifact)
	}
	if err := validateArtifactInput(withRunID(artifact, "run-1")); err != nil {
		t.Fatalf("artifact invalid: %v", err)
	}
}

func TestDevlogEvidenceOverlappingWindowsAreStableAndHalfOpen(t *testing.T) {
	reader := newFakeDevlogActivityReader()
	repo := "owner/alpha"
	boundary := devlogTestTime(13, 0)
	reader.commits[repo] = [][]GiteaCommitActivity{{
		devlogTestCommit(repo, "early", "Early", devlogTestTime(11, 0)),
		devlogTestCommit(repo, "overlap", "Overlap", devlogTestTime(12, 12)),
		devlogTestCommit(repo, "boundary", "Boundary", boundary),
		devlogTestCommit(repo, "late", "Late", devlogTestTime(14, 0)),
	}}
	// A full first page followed by a page that repeats pull request 5: the
	// listing shifted between requests and must not produce two items.
	first := make([]GiteaPullRequestActivity, 0, devlogEvidencePageSize)
	for number := 1; number <= devlogEvidencePageSize; number++ {
		first = append(first, devlogTestPull(repo, number, fmt.Sprintf("PR %d", number), "open", false, devlogTestTime(12, 1), devlogTestTime(12, 1), nil, nil, "", ""))
	}
	reader.pulls[repo] = [][]GiteaPullRequestActivity{first, {first[4]}}

	a, err := collectDevlogEvidence(t.Context(), reader, devlogEvidenceRequest{Repositories: []string{repo}, Since: devlogTestTime(10, 0), Until: boundary})
	if err != nil {
		t.Fatal(err)
	}
	b, err := collectDevlogEvidence(t.Context(), reader, devlogEvidenceRequest{Repositories: []string{repo}, Since: devlogTestTime(12, 0), Until: devlogTestTime(15, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if !devlogHasItem(a, repo+"@early") || devlogHasItem(a, repo+"@boundary") || devlogHasItem(a, repo+"@late") {
		t.Fatalf("window A items = %+v", a.Items)
	}
	if devlogHasItem(b, repo+"@early") || !devlogHasItem(b, repo+"@boundary") || !devlogHasItem(b, repo+"@late") {
		t.Fatalf("window B items = %+v", b.Items)
	}
	overlapA := devlogItemByID(t, a, repo+"@overlap")
	overlapB := devlogItemByID(t, b, repo+"@overlap")
	if overlapA.SourceURL != overlapB.SourceURL || overlapA.Status != overlapB.Status || overlapA.OccurredAt != overlapB.OccurredAt || overlapA.Window == overlapB.Window {
		t.Fatalf("overlap items differ unexpectedly: %+v vs %+v", overlapA, overlapB)
	}
	count := 0
	for _, item := range a.Items {
		if item.ID == repo+"#pr/5" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("pull request 5 appears %d times", count)
	}
}

func TestDevlogEvidenceAbsentCredentialsReportMissingWithoutCalls(t *testing.T) {
	for name, access := range map[string]GiteaAccess{
		"no token":    {BaseURLConfigured: true},
		"no base URL": {TokenConfigured: true},
	} {
		t.Run(name, func(t *testing.T) {
			reader := newFakeDevlogActivityReader()
			seedDevlogAlphaFixture(reader)
			reader.access = &access
			report, err := collectDevlogEvidence(t.Context(), reader, devlogTestRequest("owner/alpha"))
			if err != nil {
				t.Fatal(err)
			}
			if len(reader.calls) != 0 {
				t.Fatalf("reader was called without configuration: %+v", reader.calls)
			}
			if report.Complete || len(report.Items) != 0 || len(report.Missing) != 1 {
				t.Fatalf("report = %+v", report)
			}
			for _, source := range report.Repositories[0].Sources {
				if source.Status != DevlogSourceMissing || source.Error == "" {
					t.Fatalf("source = %+v", source)
				}
			}
			markdown := report.Markdown()
			if !strings.Contains(markdown, "Missing or incomplete sources") || !strings.Contains(markdown, "No activity was found") {
				t.Fatalf("markdown = %s", markdown)
			}
		})
	}
	report, err := collectDevlogEvidence(t.Context(), nil, devlogTestRequest("owner/alpha"))
	if err != nil || report.Complete || !strings.Contains(report.Missing[0], "no Gitea activity reader") {
		t.Fatalf("nil reader report = %+v err = %v", report, err)
	}
}

func TestDevlogEvidencePartialSourceFailureKeepsOtherSources(t *testing.T) {
	reader := newFakeDevlogActivityReader()
	seedDevlogAlphaFixture(reader)
	reader.errs["owner/alpha/pulls"] = errors.New("gitea pulls list failed: 502 Bad Gateway token=leaked-value")
	reader.errs["owner/beta/commits"] = errors.New("gitea commits list failed: 404 Not Found")
	report, err := collectDevlogEvidence(t.Context(), reader, devlogTestRequest("owner/alpha", "owner/beta"))
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete || len(report.Missing) != 2 {
		t.Fatalf("missing = %+v", report.Missing)
	}
	alpha := report.Repositories[0]
	if alpha.Sources[0].Status != DevlogSourceCollected || alpha.Sources[1].Status != DevlogSourceFailed || strings.Contains(alpha.Sources[1].Error, "leaked-value") {
		t.Fatalf("alpha sources = %+v", alpha.Sources)
	}
	for _, item := range report.Items {
		if item.Kind == DevlogEvidenceKindPullRequest {
			t.Fatalf("failed source must not produce items: %+v", item)
		}
	}
	// Without the PR source the merge commit stands on its own.
	if !devlogHasItem(report, "owner/alpha@c1merge") || !devlogHasItem(report, "owner/alpha@release/v1.0.0") {
		t.Fatalf("items = %+v", report.Items)
	}
	if report.Repositories[1].Sources[0].Status != DevlogSourceFailed || report.Repositories[1].Sources[1].Status != DevlogSourceCollected {
		t.Fatalf("beta sources = %+v", report.Repositories[1].Sources)
	}
	if !strings.Contains(report.Markdown(), "owner/alpha: pull_requests failed") {
		t.Fatalf("markdown = %s", report.Markdown())
	}
}

func TestDevlogEvidencePaginationIsCapped(t *testing.T) {
	reader := newFakeDevlogActivityReader()
	repo := "owner/alpha"
	var pages [][]GiteaCommitActivity
	for page := 0; page < 10; page++ {
		var commits []GiteaCommitActivity
		for index := 0; index < devlogEvidencePageSize; index++ {
			commits = append(commits, devlogTestCommit(repo, fmt.Sprintf("p%di%d", page, index), "Commit", devlogTestTime(12, 0)))
		}
		pages = append(pages, commits)
	}
	reader.commits[repo] = pages
	report, err := collectDevlogEvidence(t.Context(), reader, devlogTestRequest(repo))
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls[repo+"/commits"] != devlogEvidenceMaxPages {
		t.Fatalf("commit pages read = %d", reader.calls[repo+"/commits"])
	}
	source := report.Repositories[0].Sources[0]
	if source.Status != DevlogSourceTruncated || source.Pages != devlogEvidenceMaxPages || source.Items != devlogEvidenceMaxPages*devlogEvidencePageSize || report.Complete {
		t.Fatalf("source = %+v complete = %v", source, report.Complete)
	}
	for _, query := range reader.queries {
		if query.Limit != devlogEvidencePageSize || !query.Since.Equal(devlogTestTime(10, 0)) || !query.Until.Equal(devlogTestTime(17, 0)) {
			t.Fatalf("query = %+v", query)
		}
	}
}

func TestDevlogEvidenceRequestValidation(t *testing.T) {
	allow := devlogRepositoryAllowlist{byName: map[string]string{"owner/alpha": "owner/alpha", "legacy/alpha": "owner/alpha", "owner/beta": "owner/beta"}}
	request, ok, err := parseDevlogEvidenceRequest(map[string]string{"repositories": "Legacy/Alpha, owner/alpha,owner/beta", "since": "2026-09-10", "until": "2026-09-17T00:00:00Z"}, allow)
	if err != nil || !ok || strings.Join(request.Repositories, ",") != "owner/alpha,owner/beta" || !request.Since.Equal(devlogTestTime(10, 0)) {
		t.Fatalf("request = %+v ok=%v err=%v", request, ok, err)
	}
	if _, ok, err := parseDevlogEvidenceRequest(map[string]string{"model": "x"}, allow); ok || err != nil {
		t.Fatalf("no evidence inputs: ok=%v err=%v", ok, err)
	}
	cases := map[string]struct {
		inputs map[string]string
		want   string
	}{
		"disallowed":    {map[string]string{"repositories": "owner/alpha,evil/repo", "since": "2026-09-10", "until": "2026-09-11"}, `repository "evil/repo" is not allowed by configuration; allowed repositories: owner/alpha, owner/beta`},
		"no repos":      {map[string]string{"since": "2026-09-10", "until": "2026-09-11"}, "repositories is required"},
		"no since":      {map[string]string{"repositories": "owner/alpha", "until": "2026-09-11"}, "since is required"},
		"bad until":     {map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "next week"}, "until must be an RFC 3339"},
		"inverted":      {map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-10"}, "since must be earlier than until"},
		"window length": {map[string]string{"repositories": "owner/alpha", "since": "2026-01-01", "until": "2026-09-10"}, "must not exceed 93 days"},
	}
	for name, tc := range cases {
		_, _, err := parseDevlogEvidenceRequest(tc.inputs, allow)
		var inputErr devlogEvidenceInputError
		if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.As(err, &inputErr) || inputErr.Retryable() {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	_, _, err = parseDevlogEvidenceRequest(map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-11"}, devlogRepositoryAllowlist{byName: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "no repositories are configured") {
		t.Fatalf("empty allowlist err = %v", err)
	}
	many := devlogRepositoryAllowlist{byName: map[string]string{}}
	var names []string
	for index := 0; index <= devlogMaxEvidenceRepositories; index++ {
		name := fmt.Sprintf("owner/r%d", index)
		many.byName[name] = name
		names = append(names, name)
	}
	if _, _, err := parseDevlogEvidenceRequest(map[string]string{"repositories": strings.Join(names, ","), "since": "2026-09-10", "until": "2026-09-11"}, many); err == nil || !strings.Contains(err.Error(), "at most 8 repositories") {
		t.Fatalf("too many err = %v", err)
	}
}

func TestDevlogRepositoryAllowlistUsesOpenPilotRegistryAndOperatorList(t *testing.T) {
	_, manifestPath := testMultiRepositoryRuntimeManifest(t)
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "isolated-test")
	t.Setenv("DEVLOG_ALLOWED_REPOS", "extra/docs, not a slug")
	runner := NewRunner(NewStore(nil))
	if runner.openPilotGateError != nil {
		t.Fatal(runner.openPilotGateError)
	}
	env := runner.executors.(*EnvWorkflowExecutors)
	allow := env.devlogRepositoryAllowlist()
	if strings.Join(allow.names(), ",") != "extra/docs,owner/alpha,owner/beta" {
		t.Fatalf("allowlist = %v", allow.names())
	}
	if slug, ok := allow.resolve("LEGACY/beta"); !ok || slug != "owner/beta" {
		t.Fatalf("alias resolution = %q %v", slug, ok)
	}
	if _, ok := allow.resolve("other/repo"); ok {
		t.Fatal("unregistered repository resolved")
	}
}

func newDevlogEvidenceRunner(t *testing.T) (*Runner, *MemoryStore, *EnvWorkflowExecutors, *fakeDevlogActivityReader) {
	t.Helper()
	t.Setenv("LLAMA_LINE_BASE_URL", "")
	t.Setenv("OCQ_BASE_URL", "")
	t.Setenv("OPENCODE_EXECUTOR_BACKEND", "ocq")
	t.Setenv("DEVLOG_ALLOWED_REPOS", "owner/alpha")
	store := NewStore(func() time.Time { return devlogTestTime(20, 0) })
	if err := SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(store)
	env := runner.executors.(*EnvWorkflowExecutors)
	reader := newFakeDevlogActivityReader()
	seedDevlogAlphaFixture(reader)
	env.devlogEvidenceReader = reader
	return runner, store, env, reader
}

func devlogArtifactText(t *testing.T, detail RunDetail, name string) string {
	t.Helper()
	for _, artifact := range detail.Artifacts {
		if artifact.Name != name {
			continue
		}
		comma := strings.IndexByte(artifact.URI, ',')
		text, err := url.PathUnescape(artifact.URI[comma+1:])
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	t.Fatalf("artifact %s missing: %+v", name, detail.Artifacts)
	return ""
}

func TestDevlogPipelineAttachesRepositoryEvidence(t *testing.T) {
	runner, store, _, _ := newDevlogEvidenceRunner(t)
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-17"}})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != RunSucceeded || len(detail.Steps) != 3 || detail.Steps[1].OutputSummary != "Used deterministic Markdown fallback" {
		t.Fatalf("run = %+v steps = %+v", run, detail.Steps)
	}
	collect := detail.Steps[0]
	if !strings.Contains(collect.OutputSummary, "repository evidence items") || collect.Outputs["evidence_complete"] != "true" || collect.Outputs["evidence_repositories"] != "owner/alpha" || collect.Outputs["artifact_name"] != "devlog-evidence.json" {
		t.Fatalf("collect step = %+v", collect)
	}
	var report DevlogEvidenceReport
	if err := json.Unmarshal([]byte(devlogArtifactText(t, detail, "devlog-evidence.json")), &report); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(len(report.Items)) != collect.Outputs["evidence_items"] || len(report.Items) == 0 {
		t.Fatalf("report items = %d outputs = %+v", len(report.Items), collect.Outputs)
	}
	markdown := devlogArtifactText(t, detail, "devlog.md")
	for _, want := range []string{"# Switchyard Devlog", "## Repository evidence", "https://git.example.test/owner/alpha/pulls/10", "deployed via release v1.0.0"} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("devlog.md missing %q:\n%s", want, markdown)
		}
	}
	events := map[string]bool{}
	for _, event := range detail.Events {
		events[event.Type] = true
	}
	if !events["devlog.evidence.collected"] || !events["devlog.generated"] {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestDevlogPipelineWithoutEvidenceInputsIsUnchanged(t *testing.T) {
	runner, store, _, reader := newDevlogEvidenceRunner(t)
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog)})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := store.RunDetail(run.ID)
	if run.Status != RunSucceeded || len(detail.Artifacts) != 1 || detail.Artifacts[0].Name != "devlog.md" || len(reader.calls) != 0 {
		t.Fatalf("run = %+v artifacts = %+v calls = %+v", run, detail.Artifacts, reader.calls)
	}
	if detail.Steps[0].OutputSummary != "Collected recent Switchyard run history" || len(detail.Steps[0].Outputs) != 0 {
		t.Fatalf("collect step = %+v", detail.Steps[0])
	}
	if markdown := devlogArtifactText(t, detail, "devlog.md"); strings.Contains(markdown, "Repository evidence") {
		t.Fatalf("run-history devlog gained evidence:\n%s", markdown)
	}
}

func TestDevlogPipelineRejectsDisallowedRepository(t *testing.T) {
	runner, store, _, reader := newDevlogEvidenceRunner(t)
	run, _ := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"repositories": "owner/secret", "since": "2026-09-10", "until": "2026-09-17"}})
	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Run.Status != RunPaused || !strings.Contains(detail.Run.Summary, `repository "owner/secret" is not allowed by configuration; allowed repositories: owner/alpha`) {
		t.Fatalf("run = %+v", detail.Run)
	}
	if len(detail.Steps) != 1 || detail.Steps[0].Status != StepFailed || len(detail.Artifacts) != 0 || len(reader.calls) != 0 {
		t.Fatalf("steps = %+v artifacts = %+v calls = %+v", detail.Steps, detail.Artifacts, reader.calls)
	}
}

func TestDevlogPipelineReportsAbsentCredentialsAndStillSucceeds(t *testing.T) {
	runner, store, _, reader := newDevlogEvidenceRunner(t)
	reader.access = &GiteaAccess{BaseURLConfigured: true}
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-17"}})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := store.RunDetail(run.ID)
	if run.Status != RunSucceeded || detail.Steps[0].Outputs["evidence_complete"] != "false" || detail.Steps[0].Outputs["evidence_items"] != "0" {
		t.Fatalf("run = %+v collect = %+v", run, detail.Steps[0])
	}
	missing := false
	for _, event := range detail.Events {
		if event.Type == "devlog.evidence.missing" && strings.Contains(event.Message, "credentials are absent") {
			missing = true
		}
	}
	if !missing || !strings.Contains(devlogArtifactText(t, detail, "devlog.md"), "Gitea credentials are absent") {
		t.Fatalf("events = %+v", detail.Events)
	}
}

func TestDevlogModelOutputKeepsEvidenceSourceRecord(t *testing.T) {
	runner, store, env, _ := newDevlogEvidenceRunner(t)
	model := &captureModelExecutor{response: ModelResponse{Content: "# Weekly devlog\n\nShipped things."}}
	env.modelExecutor = model
	run, err := runner.RunOnce(t.Context(), CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-17"}})
	if err != nil {
		t.Fatal(err)
	}
	detail, _ := store.RunDetail(run.ID)
	markdown := devlogArtifactText(t, detail, "devlog.md")
	if !strings.HasPrefix(markdown, "# Weekly devlog") || !strings.Contains(markdown, "Source record:") || !strings.Contains(markdown, "https://git.example.test/owner/alpha/pulls/10") {
		t.Fatalf("devlog.md = %s", markdown)
	}
	if len(model.requests) != 1 || !strings.Contains(model.requests[0].Messages[0].Content, "only when the evidence marks it deployed") {
		t.Fatalf("model requests = %+v", model.requests)
	}
}

func TestDevlogStepwiseAndSingleExecutionAttachEvidence(t *testing.T) {
	_, store, env, _ := newDevlogEvidenceRunner(t)
	inputs := map[string]string{"repositories": "owner/alpha", "since": "2026-09-10", "until": "2026-09-17"}
	run, err := store.CreateRun(CreateRunInput{WorkflowID: string(WorkflowDevlog), Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (devlogExecutor{env: env}).ExecuteSteps(t.Context(), run, store); err != nil {
		t.Fatal(err)
	}
	detail, _ := store.RunDetail(run.ID)
	if len(detail.Artifacts) != 2 || !strings.Contains(devlogArtifactText(t, detail, "devlog.md"), "## Repository evidence") {
		t.Fatalf("stepwise artifacts = %+v", detail.Artifacts)
	}
	execution, err := (devlogExecutor{env: env}).Execute(t.Context(), Run{ID: "single", WorkflowID: string(WorkflowDevlog), Inputs: inputs})
	if err != nil || len(execution.Artifacts) != 2 || execution.Artifacts[1].Name != "devlog-evidence.json" {
		t.Fatalf("execution = %+v err = %v", execution, err)
	}
	if _, err := (devlogExecutor{env: env}).Execute(t.Context(), Run{ID: "bad", WorkflowID: string(WorkflowDevlog), Inputs: map[string]string{"repositories": "owner/other", "since": "2026-09-10", "until": "2026-09-17"}}); err == nil {
		t.Fatal("disallowed repository accepted by single execution")
	}
}

func TestEnvGiteaClientListsRepositoryActivityPages(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token activity-token" {
			t.Errorf("Authorization = %q", got)
		}
		query := r.URL.Query()
		if query.Get("page") != "2" || query.Get("limit") != "50" {
			t.Errorf("paging query = %s", r.URL.RawQuery)
		}
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		switch r.URL.Path {
		case "/api/v1/repos/owner/alpha/commits":
			if query.Get("since") != "2026-09-10T00:00:00Z" || query.Get("until") != "2026-09-17T00:00:00Z" || query.Get("files") != "false" {
				t.Errorf("commit query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"sha":"abc","html_url":"https://git/owner/alpha/commit/abc","commit":{"message":"m","author":{"name":"a","date":"2026-09-11T00:00:00Z"},"committer":{"date":"2026-09-11T00:00:00Z"}}}]`))
		case "/api/v1/repos/owner/alpha/pulls":
			if query.Get("state") != "all" || query.Get("sort") != "recentupdate" {
				t.Errorf("pull query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`[{"id":1,"number":7,"title":"t","state":"closed","merged":true,"merged_at":"2026-09-12T00:00:00Z","merge_commit_sha":"abc","html_url":"https://git/owner/alpha/pulls/7","user":{"login":"dev"}}]`))
		case "/api/v1/repos/owner/alpha/releases":
			_, _ = w.Write([]byte(`[{"id":3,"tag_name":"v1","html_url":"https://git/owner/alpha/releases/tag/v1","published_at":"2026-09-13T00:00:00Z"}]`))
		case "/api/v1/repos/owner/alpha/tags":
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("GITEA_BASE_URL", server.URL+"/")
	t.Setenv("GITEA_TOKEN", "activity-token")
	client := NewEnvGiteaClient()
	if access := client.GiteaAccess(); !access.BaseURLConfigured || !access.TokenConfigured || access.WebBaseURL != server.URL {
		t.Fatalf("access = %+v", access)
	}
	query := GiteaActivityPageQuery{Page: 2, Limit: 50, Since: devlogTestTime(10, 0), Until: devlogTestTime(17, 0)}
	commits, err := client.ListCommitActivity(t.Context(), "owner/alpha", query)
	if err != nil || len(commits) != 1 || commits[0].SHA != "abc" || !commits[0].Commit.Committer.Date.Equal(devlogTestTime(11, 0)) {
		t.Fatalf("commits = %+v err = %v", commits, err)
	}
	pulls, err := client.ListPullRequestActivity(t.Context(), "owner/alpha", query)
	if err != nil || len(pulls) != 1 || pulls[0].Number != 7 || !pulls[0].Merged || pulls[0].MergedAt == nil || pulls[0].User == nil || pulls[0].User.Login != "dev" {
		t.Fatalf("pulls = %+v err = %v", pulls, err)
	}
	releases, err := client.ListReleaseActivity(t.Context(), "owner/alpha", query)
	if err != nil || len(releases) != 1 || releases[0].TagName != "v1" {
		t.Fatalf("releases = %+v err = %v", releases, err)
	}
	if _, err := client.ListTagActivity(t.Context(), "owner/alpha", query); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("tags err = %v", err)
	}
	if _, err := client.ListCommitActivity(t.Context(), "not-a-slug", query); err == nil {
		t.Fatal("invalid repository accepted")
	}
	if len(paths) != 4 {
		t.Fatalf("requests = %v", paths)
	}
	t.Setenv("GITEA_BASE_URL", "")
	t.Setenv("GITEA_TOKEN", "")
	unconfigured := NewEnvGiteaClient()
	if access := unconfigured.GiteaAccess(); access.BaseURLConfigured || access.TokenConfigured || access.WebBaseURL != "" {
		t.Fatalf("unconfigured access = %+v", access)
	}
	if _, err := unconfigured.ListCommitActivity(t.Context(), "owner/alpha", query); err == nil || !strings.Contains(err.Error(), "GITEA_BASE_URL") {
		t.Fatalf("unconfigured err = %v", err)
	}
}
