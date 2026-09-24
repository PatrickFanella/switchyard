package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// CurrentUserLogin returns the login of the account behind GITEA_TOKEN.
func (c *EnvGiteaClient) CurrentUserLogin(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return "", fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/user")
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	resp, err := c.doJSON(ctx, http.MethodGet, endpoint, nil, &user)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("gitea user lookup failed: %s", resp.Status)
	}
	if strings.TrimSpace(user.Login) == "" {
		return "", fmt.Errorf("gitea user lookup returned no login")
	}
	return user.Login, nil
}

// ListIssueComments reads one page of comments on an issue or pull request.
func (c *EnvGiteaClient) ListIssueComments(ctx context.Context, repo string, issue, page, limit int) ([]GiteaIssueComment, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return nil, err
	}
	if issue < 1 {
		return nil, fmt.Errorf("issue must be a positive number")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", strconv.Itoa(issue), "comments")
	if err != nil {
		return nil, err
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}
	var comments []GiteaIssueComment
	resp, err := c.doJSON(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil, &comments)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("gitea issue comment list failed: %s", resp.Status)
	}
	return comments, nil
}

// CreateIssueComment posts one comment. Callers own idempotency.
func (c *EnvGiteaClient) CreateIssueComment(ctx context.Context, repo string, issue int, body string) (GiteaIssueComment, error) {
	if strings.TrimSpace(c.baseURL) == "" {
		return GiteaIssueComment{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return GiteaIssueComment{}, err
	}
	if issue < 1 {
		return GiteaIssueComment{}, fmt.Errorf("issue must be a positive number")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", strconv.Itoa(issue), "comments")
	if err != nil {
		return GiteaIssueComment{}, err
	}
	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return GiteaIssueComment{}, err
	}
	var comment GiteaIssueComment
	resp, err := c.doJSON(ctx, http.MethodPost, endpoint, payload, &comment)
	if err != nil {
		return GiteaIssueComment{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaIssueComment{}, fmt.Errorf("gitea issue comment create failed: %s", resp.Status)
	}
	return comment, nil
}
