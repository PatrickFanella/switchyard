package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GiteaActivityPageQuery selects one page of read-only repository activity.
// Since and Until are forwarded where the Gitea endpoint supports them; the
// caller still filters every item against its own window.
type GiteaActivityPageQuery struct {
	Page  int
	Limit int
	Since time.Time
	Until time.Time
}

// GiteaCommitActivity is the commit metadata Devlog evidence needs.
type GiteaCommitActivity struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Name string    `json:"name"`
			Date time.Time `json:"date"`
		} `json:"author"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author,omitempty"`
}

// GiteaPullRequestActivity extends the shared pull request payload with the
// lifecycle timestamps needed to place a pull request in a time window.
type GiteaPullRequestActivity struct {
	GiteaPullRequest
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
	MergedAt  *time.Time `json:"merged_at,omitempty"`
	User      *struct {
		Login string `json:"login"`
	} `json:"user,omitempty"`
}

// GiteaReleaseActivity is release metadata. TargetCommitish is often a branch
// name, so exact commit identity comes from the matching tag.
type GiteaReleaseActivity struct {
	ID              int        `json:"id"`
	TagName         string     `json:"tag_name"`
	TargetCommitish string     `json:"target_commitish"`
	Name            string     `json:"name"`
	HTMLURL         string     `json:"html_url"`
	Draft           bool       `json:"draft"`
	Prerelease      bool       `json:"prerelease"`
	CreatedAt       *time.Time `json:"created_at,omitempty"`
	PublishedAt     *time.Time `json:"published_at,omitempty"`
}

// GiteaTagActivity is tag metadata with the exact tagged commit.
type GiteaTagActivity struct {
	Name   string `json:"name"`
	ID     string `json:"id"`
	Commit struct {
		SHA     string     `json:"sha"`
		URL     string     `json:"url"`
		Created *time.Time `json:"created,omitempty"`
	} `json:"commit"`
}

// GiteaRepositoryActivityReader lists one page of repository activity at a
// time so callers own pagination caps.
type GiteaRepositoryActivityReader interface {
	ListCommitActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaCommitActivity, error)
	ListPullRequestActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaPullRequestActivity, error)
	ListReleaseActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaReleaseActivity, error)
	ListTagActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaTagActivity, error)
}

// GiteaAccess reports whether the Gitea origin and credential are configured,
// without exposing either value.
type GiteaAccess struct {
	BaseURLConfigured bool
	TokenConfigured   bool
	// WebBaseURL is the sanitized Gitea origin (scheme, host, path) used to
	// build browser links when a payload omits one. It never carries userinfo.
	WebBaseURL string
}

// GiteaAccessReporter is implemented by readers that can report configuration.
type GiteaAccessReporter interface {
	GiteaAccess() GiteaAccess
}

// GiteaAccess reports configuration presence only.
func (c *EnvGiteaClient) GiteaAccess() GiteaAccess {
	access := GiteaAccess{BaseURLConfigured: strings.TrimSpace(c.baseURL) != "", TokenConfigured: strings.TrimSpace(c.token) != ""}
	if base, ok := sanitizeCorrelationBaseURL(c.baseURL); ok {
		access.WebBaseURL = base
	}
	return access
}

func (c *EnvGiteaClient) ListCommitActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaCommitActivity, error) {
	values := url.Values{}
	values.Set("stat", "false")
	values.Set("verification", "false")
	values.Set("files", "false")
	if !query.Since.IsZero() {
		values.Set("since", query.Since.UTC().Format(time.RFC3339))
	}
	if !query.Until.IsZero() {
		values.Set("until", query.Until.UTC().Format(time.RFC3339))
	}
	var out []GiteaCommitActivity
	err := c.getActivityPage(ctx, repo, "commits", values, query, &out)
	return out, err
}

func (c *EnvGiteaClient) ListPullRequestActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaPullRequestActivity, error) {
	values := url.Values{}
	values.Set("state", "all")
	values.Set("sort", "recentupdate")
	var out []GiteaPullRequestActivity
	err := c.getActivityPage(ctx, repo, "pulls", values, query, &out)
	return out, err
}

func (c *EnvGiteaClient) ListReleaseActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaReleaseActivity, error) {
	var out []GiteaReleaseActivity
	err := c.getActivityPage(ctx, repo, "releases", url.Values{}, query, &out)
	return out, err
}

func (c *EnvGiteaClient) ListTagActivity(ctx context.Context, repo string, query GiteaActivityPageQuery) ([]GiteaTagActivity, error) {
	var out []GiteaTagActivity
	err := c.getActivityPage(ctx, repo, "tags", url.Values{}, query, &out)
	return out, err
}

func (c *EnvGiteaClient) getActivityPage(ctx context.Context, repo, resource string, values url.Values, query GiteaActivityPageQuery, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("GITEA_BASE_URL is not configured")
	}
	owner, name, err := c.repoParts(repo)
	if err != nil {
		return err
	}
	endpoint, err := url.JoinPath(c.baseURL, "/api/v1/repos", owner, name, resource)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	page := query.Page
	if page < 1 {
		page = 1
	}
	limit := query.Limit
	if limit < 1 || limit > 50 {
		limit = 50
	}
	values.Set("page", strconv.Itoa(page))
	values.Set("limit", strconv.Itoa(limit))
	parsed.RawQuery = values.Encode()
	resp, err := c.doJSON(ctx, http.MethodGet, parsed.String(), nil, out)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("gitea %s list failed: %s", resource, resp.Status)
	}
	return nil
}
