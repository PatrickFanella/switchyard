package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GiteaActivityItem is one issue or pull request returned by the read-only
// repository activity listing used by source collection.
type GiteaActivityItem struct {
	Number        int
	Title         string
	Body          string
	State         string
	HTMLURL       string
	Author        string
	Labels        []string
	IsPullRequest bool
	Merged        bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedAt      *time.Time
}

// GiteaRepositoryActivityReader lists issues and pull requests updated inside
// a window. Implementations only issue GET requests.
type GiteaRepositoryActivityReader interface {
	// ListRepositoryActivity returns items updated in [since, until]. It reads
	// at most maxPages pages; truncated reports that another page may exist.
	ListRepositoryActivity(ctx context.Context, repo string, since, until time.Time, maxPages int) (items []GiteaActivityItem, truncated bool, err error)
}

const (
	giteaActivityPageSize     = 50
	giteaActivityMaxPageBytes = 4 << 20
)

// ListRepositoryActivity reads /repos/{owner}/{repo}/issues with state=all and
// the since/before window. The response body of each page is size-bounded.
func (c *EnvGiteaClient) ListRepositoryActivity(ctx context.Context, repo string, since, until time.Time, maxPages int) ([]GiteaActivityItem, bool, error) {
	if c.baseURL == "" {
		return nil, false, sourceCollectionFailure{reason: "GITEA_BASE_URL is not configured", retryable: false}
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return nil, false, sourceCollectionFailure{reason: err.Error(), retryable: false}
	}
	if maxPages < 1 {
		maxPages = 1
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, "issues")
	if err != nil {
		return nil, false, err
	}
	var items []GiteaActivityItem
	for page := 1; page <= maxPages; page++ {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, false, err
		}
		query := parsed.Query()
		query.Set("state", "all")
		query.Set("since", since.UTC().Format(time.RFC3339))
		query.Set("before", until.UTC().Format(time.RFC3339))
		query.Set("limit", strconv.Itoa(giteaActivityPageSize))
		query.Set("page", strconv.Itoa(page))
		parsed.RawQuery = query.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			return nil, false, err
		}
		request.Header.Set("Accept", "application/json")
		c.setAuth(request)
		response, err := c.client.Do(request)
		if err != nil {
			return nil, false, sourceCollectionFailure{reason: "gitea activity request failed: " + RedactForLog(err.Error()), retryable: true}
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, giteaActivityMaxPageBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, false, sourceCollectionFailure{reason: "gitea activity response could not be read: " + readErr.Error(), retryable: true}
		}
		if response.StatusCode < 200 || response.StatusCode > 299 {
			// 4xx other than rate limiting will not change on retry.
			retryable := response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests
			return nil, false, sourceCollectionFailure{reason: "gitea activity listing failed: " + response.Status, retryable: retryable}
		}
		if len(body) > giteaActivityMaxPageBytes {
			return nil, false, sourceCollectionFailure{reason: "gitea activity page exceeds the response size limit", retryable: false}
		}
		var raw []struct {
			Number    int    `json:"number"`
			Title     string `json:"title"`
			Body      string `json:"body"`
			State     string `json:"state"`
			HTMLURL   string `json:"html_url"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
			ClosedAt  string `json:"closed_at"`
			User      struct {
				Login string `json:"login"`
			} `json:"user"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
			PullRequest *struct {
				Merged bool `json:"merged"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, false, sourceCollectionFailure{reason: "gitea activity response is not a JSON list: " + err.Error(), retryable: false}
		}
		for _, entry := range raw {
			if entry.Number < 1 {
				continue
			}
			item := GiteaActivityItem{Number: entry.Number, Title: entry.Title, Body: entry.Body, State: entry.State, HTMLURL: entry.HTMLURL, Author: entry.User.Login, IsPullRequest: entry.PullRequest != nil}
			if entry.PullRequest != nil {
				item.Merged = entry.PullRequest.Merged
			}
			for _, label := range entry.Labels {
				if strings.TrimSpace(label.Name) != "" {
					item.Labels = append(item.Labels, label.Name)
				}
			}
			item.CreatedAt, _ = time.Parse(time.RFC3339, entry.CreatedAt)
			item.UpdatedAt, _ = time.Parse(time.RFC3339, entry.UpdatedAt)
			if closed, parseErr := time.Parse(time.RFC3339, entry.ClosedAt); parseErr == nil {
				closed = closed.UTC()
				item.ClosedAt = &closed
			}
			items = append(items, item)
		}
		if len(raw) < giteaActivityPageSize {
			return items, false, nil
		}
	}
	return items, true, nil
}

// sourceCollectionFailure classifies a source failure for retry decisions.
type sourceCollectionFailure struct {
	reason    string
	retryable bool
}

func (e sourceCollectionFailure) Error() string   { return e.reason }
func (e sourceCollectionFailure) Retryable() bool { return e.retryable }

func sourceFailureRetryable(err error) bool {
	var classified interface{ Retryable() bool }
	if errors.As(err, &classified) {
		return classified.Retryable()
	}
	return true
}
