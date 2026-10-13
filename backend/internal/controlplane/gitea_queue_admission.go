package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type OpenPilotQueueIssue struct {
	Repo      string               `json:"repo"`
	ID        int                  `json:"id"`
	Number    int                  `json:"number"`
	Title     string               `json:"title"`
	Body      string               `json:"-"`
	State     string               `json:"state"`
	Labels    []string             `json:"labels"`
	Assignees []GiteaIssueAssignee `json:"assignees"`
	CreatedAt time.Time            `json:"created_at"`
	UpdatedAt time.Time            `json:"updated_at"`
	Milestone string               `json:"milestone"`
	DueAt     time.Time            `json:"due_at,omitempty"`
	IsPR      bool                 `json:"is_pr"`
	Author    string               `json:"author"`
}

type rawQueueIssue struct {
	ID      int    `json:"id"`
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
	Labels  []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"labels"`
	Assignees  []GiteaIssueAssignee `json:"assignees"`
	CreatedAt  time.Time            `json:"created_at"`
	UpdatedAt  time.Time            `json:"updated_at"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Milestone *struct {
		Title string     `json:"title"`
		DueAt *time.Time `json:"due_on"`
	} `json:"milestone"`
	PR   *struct{}          `json:"pull_request"`
	User GiteaIssueAssignee `json:"user"`
}

func (r rawQueueIssue) issue(repo string) (OpenPilotQueueIssue, error) {
	if r.Repository.FullName != "" {
		repo = r.Repository.FullName
	}
	if repo == "" {
		parsed, _ := url.Parse(r.HTMLURL)
		if parsed != nil {
			parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(parts) >= 4 {
				repo = parts[0] + "/" + parts[1]
			}
		}
	}
	if !validOpenPilotRepositorySlug(repo) || r.Number < 1 || r.ID < 1 {
		return OpenPilotQueueIssue{}, fmt.Errorf("invalid queue issue identity")
	}
	issue := OpenPilotQueueIssue{Repo: repo, ID: r.ID, Number: r.Number, Title: r.Title, Body: r.Body, State: r.State, Assignees: r.Assignees, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, IsPR: r.PR != nil, Author: r.User.Login, Labels: []string{}}
	for _, label := range r.Labels {
		issue.Labels = append(issue.Labels, label.Name)
	}
	if r.Milestone != nil {
		issue.Milestone = r.Milestone.Title
		if r.Milestone.DueAt != nil {
			issue.DueAt = *r.Milestone.DueAt
		}
	}
	return issue, nil
}

type OpenPilotQueueClient interface {
	GiteaRepositoryLister
	GiteaPullRequestGetter
	IssueFetcher
	ListQueueIssues(context.Context, string, bool) ([]OpenPilotQueueIssue, error)
	FetchQueueIssue(context.Context, string, int) (OpenPilotQueueIssue, error)
	QueueDependencies(context.Context, string, int) ([]OpenPilotQueueIssue, error)
	QueueAdmissionBlockers(context.Context, string) ([]string, error)
	AddQueueLabel(context.Context, string, int) error
}

func (c *EnvGiteaClient) queuePages(ctx context.Context, path string, values url.Values, repo string) ([]OpenPilotQueueIssue, error) {
	out := []OpenPilotQueueIssue{}
	seen := map[int]bool{}
	for page := 1; page <= 200; page++ {
		values.Set("limit", "50")
		values.Set("page", strconv.Itoa(page))
		endpoint, err := url.JoinPath(c.baseURL, path)
		if err != nil || c.baseURL == "" {
			return nil, ErrInvalid
		}
		var rows []rawQueueIssue
		response, err := c.doJSON(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil, &rows)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Gitea queue scan returned HTTP %d", response.StatusCode)
		}
		for _, row := range rows {
			issue, err := row.issue(repo)
			if err != nil {
				return nil, err
			}
			if seen[issue.ID] {
				return nil, fmt.Errorf("Gitea queue scan is unstable or duplicated")
			}
			seen[issue.ID] = true
			out = append(out, issue)
		}
		if len(rows) < 50 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("Gitea queue scan exceeds 10000 issues")
}

func (c *EnvGiteaClient) ListQueueIssues(ctx context.Context, label string, assigned bool) ([]OpenPilotQueueIssue, error) {
	values := url.Values{"state": {"open"}, "type": {"issues"}}
	if label == "__pull_requests__" {
		values.Set("type", "pulls")
	} else if label != "" {
		values.Set("labels", label)
	}
	if assigned {
		values.Set("assigned", "true")
	}
	return c.queuePages(ctx, "/api/v1/repos/issues/search", values, "")
}

func (c *EnvGiteaClient) FetchQueueIssue(ctx context.Context, repo string, number int) (OpenPilotQueueIssue, error) {
	if !validOpenPilotRepositorySlug(repo) || number < 1 {
		return OpenPilotQueueIssue{}, ErrInvalid
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", repo, "issues", strconv.Itoa(number))
	if err != nil {
		return OpenPilotQueueIssue{}, err
	}
	var raw rawQueueIssue
	response, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &raw)
	if err != nil {
		return OpenPilotQueueIssue{}, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return OpenPilotQueueIssue{}, fmt.Errorf("Gitea issue read returned HTTP %d", response.StatusCode)
	}
	issue, err := raw.issue(repo)
	if err == nil && (issue.Number != number || !strings.EqualFold(issue.Repo, repo)) {
		return issue, ErrConflict
	}
	return issue, err
}

func (c *EnvGiteaClient) QueueDependencies(ctx context.Context, repo string, number int) ([]OpenPilotQueueIssue, error) {
	if !validOpenPilotRepositorySlug(repo) || number < 1 {
		return nil, ErrInvalid
	}
	return c.queuePages(ctx, "/api/v1/repos/"+repo+"/issues/"+strconv.Itoa(number)+"/dependencies", url.Values{}, "")
}

func (c *EnvGiteaClient) AddQueueLabel(ctx context.Context, repo string, number int) error {
	if !validOpenPilotRepositorySlug(repo) || number < 1 {
		return ErrInvalid
	}
	labelID, err := c.queueLabelID(ctx, repo)
	if err != nil {
		return err
	}
	if labelID < 1 {
		return fmt.Errorf("standard agent:queued label is missing; bootstrap repository labels")
	}
	endpoint, _ := url.JoinPath(c.baseURL, "/api/v1/repos", repo, "issues", strconv.Itoa(number), "labels")
	body, err := json.Marshal(map[string]any{"labels": []int{labelID}})
	if err != nil {
		return err
	}
	response, err := c.doJSON(ctx, http.MethodPost, endpoint, body, nil)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Gitea additive label write returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (c *EnvGiteaClient) queueLabelID(ctx context.Context, repo string) (int, error) {
	var labelID int
	for page := 1; page <= 200; page++ {
		endpoint, _ := url.JoinPath(c.baseURL, "/api/v1/repos", repo, "labels")
		var labels []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		response, err := c.doJSON(ctx, http.MethodGet, endpoint+"?limit=50&page="+strconv.Itoa(page), nil, &labels)
		if err != nil {
			return 0, err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("Gitea label listing returned HTTP %d", response.StatusCode)
		}
		for _, label := range labels {
			if label.Name == "agent:queued" {
				labelID = label.ID
			}
		}
		if len(labels) < 50 {
			break
		}
		if page == 200 {
			return 0, fmt.Errorf("Gitea label scan incomplete")
		}
	}
	return labelID, nil
}

func (c *EnvGiteaClient) QueueAdmissionBlockers(ctx context.Context, repo string) ([]string, error) {
	if !validOpenPilotRepositorySlug(repo) {
		return nil, ErrInvalid
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", repo)
	if err != nil {
		return nil, err
	}
	var repository struct {
		FullName    string `json:"full_name"`
		Archived    bool   `json:"archived"`
		Empty       bool   `json:"empty"`
		HasIssues   bool   `json:"has_issues"`
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	response, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &repository)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gitea repository qualification returned HTTP %d", response.StatusCode)
	}
	if !strings.EqualFold(repository.FullName, repo) {
		return nil, ErrConflict
	}
	blockers := []string{}
	if repository.Archived || repository.Empty || !repository.HasIssues || !repository.Permissions.Push {
		blockers = append(blockers, "repository is archived, empty, has no issues, or runtime account lacks write permission")
	}
	labelID, err := c.queueLabelID(ctx, repo)
	if err != nil {
		return nil, err
	}
	if labelID < 1 {
		blockers = append(blockers, "standard agent:queued label is missing; repository bootstrap required")
	}
	return blockers, nil
}
