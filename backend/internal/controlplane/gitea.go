package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type GiteaIssue struct {
	ID      int      `json:"id"`
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	State   string   `json:"state"`
	URL     string   `json:"url"`
	HTMLURL string   `json:"html_url"`
	Labels  []string `json:"labels"`
}

type IssueFetcher interface {
	FetchIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error)
}

type EnvGiteaClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewEnvGiteaClient() *EnvGiteaClient {
	return &EnvGiteaClient{baseURL: strings.TrimRight(os.Getenv("GITEA_BASE_URL"), "/"), token: os.Getenv("GITEA_TOKEN"), client: &http.Client{Timeout: 10 * time.Second}}
}

func (c *EnvGiteaClient) FetchIssue(ctx context.Context, repo string, issue string) (GiteaIssue, error) {
	if c.baseURL == "" {
		return GiteaIssue{}, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return GiteaIssue{}, fmt.Errorf("repo must be OWNER/REPO")
	}
	issueNumber, err := strconv.Atoi(issue)
	if err != nil || issueNumber < 1 {
		return GiteaIssue{}, fmt.Errorf("issue must be a positive number")
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues", issue)
	if err != nil {
		return GiteaIssue{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return GiteaIssue{}, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "token "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return GiteaIssue{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return GiteaIssue{}, fmt.Errorf("gitea issue fetch failed: %s", resp.Status)
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
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return GiteaIssue{}, err
	}
	labels := make([]string, 0, len(raw.Labels))
	for _, label := range raw.Labels {
		if label.Name != "" {
			labels = append(labels, label.Name)
		}
	}
	return GiteaIssue{ID: raw.ID, Number: raw.Number, Title: raw.Title, Body: raw.Body, State: raw.State, URL: raw.URL, HTMLURL: raw.HTMLURL, Labels: labels}, nil
}
