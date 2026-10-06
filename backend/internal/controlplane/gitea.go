package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type GiteaIssueAssignee struct {
	Login string `json:"login"`
}

// GiteaIssue models the issue payload used by the control plane.
type GiteaIssue struct {
	ID         int                  `json:"id"`
	Number     int                  `json:"number"`
	Repository string               `json:"-"`
	Title      string               `json:"title"`
	Body       string               `json:"body"`
	State      string               `json:"state"`
	URL        string               `json:"url"`
	HTMLURL    string               `json:"html_url"`
	Labels     []string             `json:"labels"`
	Assignees  []GiteaIssueAssignee `json:"assignees,omitempty"`
}

// IssueFetcher fetches issues from Gitea.
type IssueFetcher interface {
	FetchIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error)
}

type GiteaIssueReader interface {
	IssueFetcher
	ListIssues(ctx context.Context, repo string, labels []string) ([]GiteaIssue, error)
	ListAssignedIssues(ctx context.Context, repo, assignee string) ([]GiteaIssue, error)
}

// GiteaPullRequestRef models a repository or pull request reference.
type GiteaPullRequestRef struct {
	Ref   string `json:"ref,omitempty"`
	Label string `json:"label,omitempty"`
	SHA   string `json:"sha,omitempty"`
	Repo  *struct {
		Name     string `json:"name,omitempty"`
		FullName string `json:"full_name,omitempty"`
	} `json:"repo,omitempty"`
}

// GiteaPullRequest models the pull request payload used by the control plane.
type GiteaPullRequest struct {
	ID             int                 `json:"id"`
	Number         int                 `json:"number"`
	Title          string              `json:"title"`
	State          string              `json:"state"`
	URL            string              `json:"url"`
	HTMLURL        string              `json:"html_url"`
	MergeCommitSHA string              `json:"merge_commit_sha,omitempty"`
	Merged         bool                `json:"merged,omitempty"`
	Head           GiteaPullRequestRef `json:"head,omitempty"`
	Base           GiteaPullRequestRef `json:"base,omitempty"`
}

// GiteaPullRequestInput describes a pull request create request.
type GiteaPullRequestInput struct {
	Repo  string
	Base  string
	Head  string
	Title string
	Body  string
	Draft bool
}

// GiteaCommitStatus models a single commit status entry.
type GiteaCommitStatus struct {
	Context     string `json:"context,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
	URL         string `json:"url,omitempty"`
}

// GiteaCheckRun models a single CI check entry.
type GiteaCheckRun struct {
	Name       string `json:"name,omitempty"`
	Status     string `json:"status,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
	URL        string `json:"url,omitempty"`
}

// GiteaCheckSummary summarizes commit status and checks for a pull request.
type GiteaCheckSummary struct {
	State      string              `json:"state,omitempty"`
	SHA        string              `json:"sha,omitempty"`
	TotalCount int                 `json:"total_count,omitempty"`
	Statuses   []GiteaCommitStatus `json:"statuses,omitempty"`
	Checks     []GiteaCheckRun     `json:"checks,omitempty"`
}

// GiteaMergeResult captures the result of a merge request.
type GiteaMergeResult struct {
	Merged  bool   `json:"merged"`
	SHA     string `json:"sha,omitempty"`
	Message string `json:"message,omitempty"`
}

// GiteaPullRequestReader reads pull requests and their checks from Gitea.
type GiteaPullRequestReader interface {
	FindPullRequestByHead(ctx context.Context, repo string, head string) (GiteaPullRequest, bool, error)
	PullRequestChecks(ctx context.Context, repo string, pr GiteaPullRequest) (GiteaCheckSummary, error)
}

// GiteaPullRequestMerger merges pull requests and closes issues in Gitea.
type GiteaPullRequestMerger interface {
	MergePullRequest(ctx context.Context, repo string, expected GiteaPullRequest) (GiteaMergeResult, error)
	CloseIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error)
}

// GiteaWriter creates pull requests in Gitea.
type GiteaWriter interface {
	CreatePullRequest(ctx context.Context, input GiteaPullRequestInput) (GiteaPullRequest, error)
}

type EnvGiteaClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewEnvGiteaClient() *EnvGiteaClient {
	return &EnvGiteaClient{baseURL: strings.TrimRight(os.Getenv("GITEA_BASE_URL"), "/"), token: os.Getenv("GITEA_TOKEN"), client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *EnvGiteaClient) setAuth(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
}

func (c *EnvGiteaClient) repoParts(repo string) (string, string, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo must be OWNER/REPO")
	}
	return parts[0], parts[1], nil
}

func (c *EnvGiteaClient) doJSON(ctx context.Context, method, endpoint string, body []byte, out any) (*http.Response, error) {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp, nil
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			resp.Body.Close()
			return nil, err
		}
	}
	return resp, nil
}

func (c *EnvGiteaClient) FetchIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error) {
	if c.baseURL == "" {
		return GiteaIssue{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaIssue{}, err
	}
	issueNumber, err := strconv.Atoi(issue)
	if err != nil || issueNumber < 1 {
		return GiteaIssue{}, fmt.Errorf("issue must be a positive number")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", issue)
	if err != nil {
		return GiteaIssue{}, err
	}
	resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return GiteaIssue{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaIssue{}, fmt.Errorf("gitea issue fetch failed: %s", resp.Status)
	}
	var raw struct {
		ID        int                  `json:"id"`
		Number    int                  `json:"number"`
		Title     string               `json:"title"`
		Body      string               `json:"body"`
		State     string               `json:"state"`
		URL       string               `json:"url"`
		HTMLURL   string               `json:"html_url"`
		Assignees []GiteaIssueAssignee `json:"assignees"`
		Labels    []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return GiteaIssue{}, err
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, label := range raw.Labels {
		if label.Name != "" {
			labels = append(labels, label.Name)
		}
	}
	return GiteaIssue{ID: raw.ID, Number: raw.Number, Repository: raw.Repository.FullName, Title: raw.Title, Body: raw.Body, State: raw.State, URL: raw.URL, HTMLURL: raw.HTMLURL, Labels: labels, Assignees: raw.Assignees}, nil
}

func (c *EnvGiteaClient) ListIssues(ctx context.Context, repo string, labels []string) ([]GiteaIssue, error) {
	return c.listIssues(ctx, repo, labels, "")
}

func (c *EnvGiteaClient) ListAssignedIssues(ctx context.Context, repo, assignee string) ([]GiteaIssue, error) {
	if strings.TrimSpace(assignee) == "" {
		return nil, fmt.Errorf("issue assignee is required")
	}
	return c.listIssues(ctx, repo, nil, strings.TrimSpace(assignee))
}

func (c *EnvGiteaClient) listIssues(ctx context.Context, repo string, labels []string, assignee string) ([]GiteaIssue, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return nil, err
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues")
	if err != nil {
		return nil, err
	}
	result := []GiteaIssue{}
	for page := 1; page <= 10; page++ {
		query, parseErr := url.Parse(endpoint)
		if parseErr != nil {
			return nil, parseErr
		}
		values := query.Query()
		values.Set("state", "open")
		values.Set("type", "issues")
		values.Set("limit", "50")
		values.Set("page", strconv.Itoa(page))
		if assignee != "" {
			values.Set("assigned_by", assignee)
		}
		if len(labels) > 0 {
			values.Set("labels", strings.Join(labels, ","))
		}
		query.RawQuery = values.Encode()
		var raw []struct {
			ID        int                  `json:"id"`
			Number    int                  `json:"number"`
			Title     string               `json:"title"`
			Body      string               `json:"body"`
			State     string               `json:"state"`
			URL       string               `json:"url"`
			HTMLURL   string               `json:"html_url"`
			Assignees []GiteaIssueAssignee `json:"assignees"`
			Labels    []struct {
				Name string `json:"name"`
			} `json:"labels"`
			PullRequest *json.RawMessage `json:"pull_request"`
		}
		resp, requestErr := c.doJSON(ctx, http.MethodGet, query.String(), nil, &raw)
		if requestErr != nil {
			return nil, requestErr
		}
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode > 299 {
				return nil, fmt.Errorf("gitea issue list failed: %s", resp.Status)
			}
		}
		for _, item := range raw {
			if item.PullRequest != nil || item.Number < 1 || item.State != "open" {
				continue
			}
			itemLabels := make([]string, 0, len(item.Labels))
			for _, label := range item.Labels {
				if label.Name != "" {
					itemLabels = append(itemLabels, label.Name)
				}
			}
			result = append(result, GiteaIssue{ID: item.ID, Number: item.Number, Title: item.Title, Body: item.Body, State: item.State, URL: item.URL, HTMLURL: item.HTMLURL, Labels: itemLabels, Assignees: item.Assignees})
		}
		if len(raw) < 50 {
			break
		}
	}
	return result, nil
}

// FindPullRequestByHead looks up an open pull request by head branch.
func (c *EnvGiteaClient) FindPullRequestByHead(ctx context.Context, repo string, head string) (GiteaPullRequest, bool, error) {
	if c.baseURL == "" {
		return GiteaPullRequest{}, false, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaPullRequest{}, false, err
	}
	queryHead := head
	if queryHead == "" {
		return GiteaPullRequest{}, false, fmt.Errorf("head must not be empty")
	}
	if !strings.Contains(queryHead, ":") {
		queryHead = owner + ":" + queryHead
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "pulls")
	if err != nil {
		return GiteaPullRequest{}, false, err
	}
	const pageSize = 50
	for page := 1; page <= 5; page++ {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return GiteaPullRequest{}, false, err
		}
		q := parsed.Query()
		q.Set("state", "open")
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("page", strconv.Itoa(page))
		parsed.RawQuery = q.Encode()
		var pulls []GiteaPullRequest
		resp, err := c.doJSON(ctx, http.MethodGet, parsed.String(), nil, &pulls)
		if err != nil {
			return GiteaPullRequest{}, false, err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			resp.Body.Close()
			return GiteaPullRequest{}, false, fmt.Errorf("gitea pull request lookup failed: %s", resp.Status)
		}
		for _, pr := range pulls {
			if pullRequestMatchesHead(pr, queryHead) {
				resp.Body.Close()
				return pr, true, nil
			}
		}
		resp.Body.Close()
		if len(pulls) < pageSize {
			break
		}
	}
	return GiteaPullRequest{}, false, nil
}

func pullRequestMatchesHead(pr GiteaPullRequest, head string) bool {
	if head == "" {
		return false
	}
	owner, branch, ok := strings.Cut(head, ":")
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(branch) == "" {
		return pr.Head.Label == head || (pr.Head.Label == "" && pr.Head.Ref == head)
	}
	if pr.Head.Label != "" {
		return pr.Head.Label == owner+":"+branch || pr.Head.Label == branch
	}
	if pr.Head.Ref != branch {
		return false
	}
	if pr.Head.Repo != nil && pr.Head.Repo.FullName != "" {
		return strings.HasPrefix(pr.Head.Repo.FullName, owner+"/")
	}
	return true
}

// PullRequestChecks retrieves commit status information for a pull request.
func (c *EnvGiteaClient) PullRequestChecks(ctx context.Context, repo string, pr GiteaPullRequest) (GiteaCheckSummary, error) {
	if c.baseURL == "" {
		return GiteaCheckSummary{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaCheckSummary{}, err
	}
	sha := pr.Head.SHA
	if sha == "" {
		return GiteaCheckSummary{}, fmt.Errorf("pull request head sha is required")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "commits", sha, "status")
	if err != nil {
		return GiteaCheckSummary{}, err
	}
	var raw struct {
		State      string              `json:"state"`
		SHA        string              `json:"sha"`
		TotalCount int                 `json:"total_count"`
		Statuses   []GiteaCommitStatus `json:"statuses"`
		Checks     []GiteaCheckRun     `json:"checks"`
		CheckRuns  []GiteaCheckRun     `json:"check_runs"`
	}
	resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &raw)
	if err != nil {
		return GiteaCheckSummary{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaCheckSummary{}, fmt.Errorf("gitea pull request checks failed: %s", resp.Status)
	}
	checks := append([]GiteaCheckRun{}, raw.Checks...)
	checks = append(checks, raw.CheckRuns...)
	return GiteaCheckSummary{State: raw.State, SHA: raw.SHA, TotalCount: raw.TotalCount, Statuses: raw.Statuses, Checks: checks}, nil
}

// MergePullRequest merges the specified pull request.
func (c *EnvGiteaClient) MergePullRequest(ctx context.Context, repo string, expected GiteaPullRequest) (GiteaMergeResult, error) {
	if c.baseURL == "" {
		return GiteaMergeResult{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaMergeResult{}, err
	}
	if err := validateGiteaPullRequestMergeIdentity(repo, expected); err != nil {
		return GiteaMergeResult{}, err
	}
	pullEndpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "pulls", strconv.Itoa(expected.Number))
	if err != nil {
		return GiteaMergeResult{}, err
	}
	var current GiteaPullRequest
	preflight, err := c.doJSON(ctx, http.MethodGet, pullEndpoint, nil, &current)
	if err != nil {
		return GiteaMergeResult{}, err
	}
	defer preflight.Body.Close()
	if preflight.StatusCode < 200 || preflight.StatusCode > 299 {
		return GiteaMergeResult{}, fmt.Errorf("gitea pull request preflight failed: %s", preflight.Status)
	}
	if !strings.EqualFold(strings.TrimSpace(current.State), "open") || current.Merged || !sameGiteaPullRequestMergeIdentity(current, expected) {
		return GiteaMergeResult{}, fmt.Errorf("gitea pull request changed before merge")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "pulls", strconv.Itoa(expected.Number), "merge")
	if err != nil {
		return GiteaMergeResult{}, err
	}
	body, err := json.Marshal(struct {
		Do                     string `json:"do"`
		HeadCommitID           string `json:"head_commit_id"`
		ForceMerge             bool   `json:"force_merge"`
		MergeWhenChecksSucceed bool   `json:"merge_when_checks_succeed"`
		DeleteBranchAfterMerge bool   `json:"delete_branch_after_merge"`
	}{Do: "merge", HeadCommitID: expected.Head.SHA})
	if err != nil {
		return GiteaMergeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return GiteaMergeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return GiteaMergeResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaMergeResult{}, fmt.Errorf("gitea pull request merge failed: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return GiteaMergeResult{}, err
	}
	resp.Body.Close()
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		result := GiteaMergeResult{Merged: true, Message: "merged; merge commit SHA unavailable"}
		var pr GiteaPullRequest
		prResp, getErr := c.doJSON(ctx, http.MethodGet, pullEndpoint, nil, &pr)
		if prResp != nil {
			prResp.Body.Close()
		}
		if getErr == nil && prResp != nil && prResp.StatusCode >= 200 && prResp.StatusCode <= 299 && pr.MergeCommitSHA != "" {
			result.SHA = pr.MergeCommitSHA
			result.Message = "merged"
		}
		return result, nil
	}
	var raw GiteaMergeResult
	if err := json.Unmarshal(data, &raw); err != nil {
		return GiteaMergeResult{}, err
	}
	if raw.Message == "" {
		raw.Message = "merged"
	}
	return raw, nil
}

func validateGiteaPullRequestMergeIdentity(repo string, pr GiteaPullRequest) error {
	if pr.ID < 1 || pr.Number < 1 || pr.Head.Repo == nil || strings.TrimSpace(pr.Head.Repo.FullName) == "" || strings.TrimSpace(pr.Head.Ref) == "" || strings.TrimSpace(pr.Head.SHA) == "" || pr.Base.Repo == nil || strings.TrimSpace(pr.Base.Repo.FullName) == "" || strings.TrimSpace(pr.Base.Ref) == "" || strings.TrimSpace(pr.Base.SHA) == "" {
		return fmt.Errorf("complete expected pull request identity is required")
	}
	if strings.TrimSpace(pr.Base.Repo.FullName) != strings.TrimSpace(repo) {
		return fmt.Errorf("expected pull request base repository does not match merge repository")
	}
	return nil
}

func sameGiteaPullRequestMergeIdentity(left, right GiteaPullRequest) bool {
	return left.ID == right.ID &&
		left.Number == right.Number &&
		left.Head.Repo != nil && right.Head.Repo != nil && left.Head.Repo.FullName == right.Head.Repo.FullName &&
		left.Head.Ref == right.Head.Ref &&
		left.Head.SHA == right.Head.SHA &&
		left.Base.Repo != nil && right.Base.Repo != nil && left.Base.Repo.FullName == right.Base.Repo.FullName &&
		left.Base.Ref == right.Base.Ref &&
		left.Base.SHA == right.Base.SHA
}

// CloseIssue closes a Gitea issue and returns the updated issue.
func (c *EnvGiteaClient) CloseIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error) {
	if c.baseURL == "" {
		return GiteaIssue{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaIssue{}, err
	}
	issueNumber, err := strconv.Atoi(issue)
	if err != nil || issueNumber < 1 {
		return GiteaIssue{}, fmt.Errorf("issue must be a positive number")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", issue)
	if err != nil {
		return GiteaIssue{}, err
	}
	body, err := json.Marshal(map[string]string{"state": "closed"})
	if err != nil {
		return GiteaIssue{}, err
	}
	var raw struct {
		ID      int    `json:"id"`
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		State   string `json:"state"`
		URL     string `json:"url"`
		HTMLURL string `json:"html_url"`
		Labels  []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	resp, err := c.doJSON(ctx, http.MethodPatch, endpoint, body, &raw)
	if err != nil {
		return GiteaIssue{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaIssue{}, fmt.Errorf("gitea issue close failed: %s", resp.Status)
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, label := range raw.Labels {
		if label.Name != "" {
			labels = append(labels, label.Name)
		}
	}
	return GiteaIssue{ID: raw.ID, Number: raw.Number, Title: raw.Title, Body: raw.Body, State: raw.State, URL: raw.URL, HTMLURL: raw.HTMLURL, Labels: labels}, nil
}

func (c *EnvGiteaClient) CreatePullRequest(ctx context.Context, input GiteaPullRequestInput) (GiteaPullRequest, error) {
	if c.baseURL == "" {
		return GiteaPullRequest{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(input.Repo)
	if err != nil {
		return GiteaPullRequest{}, err
	}
	body, err := json.Marshal(map[string]any{"base": input.Base, "head": input.Head, "title": input.Title, "body": input.Body, "draft": input.Draft})
	if err != nil {
		return GiteaPullRequest{}, err
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "pulls")
	if err != nil {
		return GiteaPullRequest{}, err
	}
	resp, err := c.doJSON(ctx, http.MethodPost, endpoint, body, nil)
	if err != nil {
		return GiteaPullRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaPullRequest{}, fmt.Errorf("gitea pull request create failed: %s", resp.Status)
	}
	var raw GiteaPullRequest
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return GiteaPullRequest{}, err
	}
	return raw, nil
}

// AssignIssue adds the bot to the freshly fetched assignee list. Gitea's edit API
// replaces the list, so retain every existing assignee in the request.
func (c *EnvGiteaClient) AssignIssue(ctx context.Context, repo string, issue GiteaIssue, assignee string) error {
	if strings.TrimSpace(c.baseURL) == "" {
		return fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	if issue.Number < 1 || strings.TrimSpace(assignee) == "" {
		return fmt.Errorf("issue number and assignee are required")
	}
	if openPilotIssueAssignedTo(issue, assignee) {
		return nil
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return err
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", strconv.Itoa(issue.Number))
	if err != nil {
		return err
	}
	logins := make([]string, 0, len(issue.Assignees)+1)
	for _, user := range issue.Assignees {
		if user.Login != "" {
			logins = append(logins, user.Login)
		}
	}
	logins = append(logins, strings.TrimSpace(assignee))
	var updated struct {
		Assignees []GiteaIssueAssignee `json:"assignees"`
	}
	payload, err := json.Marshal(map[string]any{"assignees": logins})
	if err != nil {
		return err
	}
	resp, err := c.doJSON(ctx, http.MethodPatch, endpoint, payload, &updated)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("gitea issue assignment failed: %s", resp.Status)
	}
	if !openPilotIssueAssignedTo(GiteaIssue{Assignees: updated.Assignees}, assignee) {
		return fmt.Errorf("gitea issue assignment was not applied")
	}
	return nil
}
