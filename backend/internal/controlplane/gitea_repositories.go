package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
)

type GiteaRepositoryLister interface {
	ListOpenPilotRepositories(context.Context) ([]string, error)
}

// Re-query on every poll so additions and access changes take effect without
// retaining a stale inventory. Execution admission is checked separately.
func (c *EnvGiteaClient) ListOpenPilotRepositories(ctx context.Context) ([]string, error) {
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos/search")
	if err != nil || c.baseURL == "" {
		return nil, fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	seen := map[string]bool{}
	for page := 1; page <= 200; page++ {
		query, _ := url.Parse(endpoint)
		values := query.Query()
		values.Set("limit", "50")
		values.Set("page", strconv.Itoa(page))
		query.RawQuery = values.Encode()
		var result struct {
			Data []struct {
				FullName  string `json:"full_name"`
				HasIssues bool   `json:"has_issues"`
				Archived  bool   `json:"archived"`
			} `json:"data"`
		}
		response, err := c.doJSON(ctx, http.MethodGet, query.String(), nil, &result)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("gitea repository discovery failed: %s", response.Status)
		}
		for _, repository := range result.Data {
			if !validOpenPilotRepositorySlug(repository.FullName) {
				return nil, fmt.Errorf("gitea repository discovery returned an invalid slug")
			}
			if repository.HasIssues && !repository.Archived {
				seen[repository.FullName] = true
			}
		}
		if len(result.Data) < 50 {
			out := make([]string, 0, len(seen))
			for slug := range seen {
				out = append(out, slug)
			}
			sort.Strings(out)
			return out, nil
		}
	}
	return nil, fmt.Errorf("gitea repository discovery exceeds 10000 repositories")
}
