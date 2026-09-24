package controlplane

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Generic reviewed delivery: a durable receipt per idempotency key, claimed
// for one attempt at a time, and adapters that publish to exactly one
// configured destination and can find an earlier post by its marker. Devlog
// delivery and Changemaker publication share this contract; each keeps its
// own approval policy, receipt table, and marker namespace.

const (
	DeliveryAdapterDisabled          = "disabled"
	DeliveryAdapterGiteaIssueComment = "gitea-issue-comment"

	DeliverySending   = "sending"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"

	deliveryErrorMaxBytes    = 1000
	giteaCommentScanPageSize = 50
	giteaCommentScanMaxPages = 20
)

var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DeliveryReceipt is the durable record of one delivery identity. The
// idempotency key is unique, so every attempt for the same content digest and
// destination updates the same receipt.
type DeliveryReceipt struct {
	ID             string     `json:"id"`
	IdempotencyKey string     `json:"idempotency_key"`
	RunID          string     `json:"run_id"`
	ArtifactID     string     `json:"artifact_id"`
	ArtifactSHA256 string     `json:"artifact_sha256"`
	Adapter        string     `json:"adapter"`
	Destination    string     `json:"destination"`
	ApprovalID     string     `json:"approval_id"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	Reconciled     bool       `json:"reconciled"`
	ExternalID     string     `json:"external_id,omitempty"`
	ExternalURL    string     `json:"external_url,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	RequestedBy    string     `json:"requested_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"`

	claimToken string
}

// BeginDeliveryInput claims the delivery identity for one attempt.
type BeginDeliveryInput struct {
	IdempotencyKey string
	RunID          string
	ArtifactID     string
	ArtifactSHA256 string
	Adapter        string
	Destination    string
	ApprovalID     string
	Actor          string
	StaleAfter     time.Duration
}

// FinishDeliveryInput records the outcome of a claimed attempt.
type FinishDeliveryInput struct {
	IdempotencyKey string
	ClaimToken     string
	Delivered      bool
	Reconciled     bool
	ExternalID     string
	ExternalURL    string
	Error          string
}

func validateBeginDelivery(input BeginDeliveryInput) error {
	if input.IdempotencyKey == "" || input.RunID == "" || input.ArtifactID == "" || !sha256HexPattern.MatchString(input.ArtifactSHA256) ||
		input.Adapter == "" || input.Destination == "" || input.ApprovalID == "" || input.Actor == "" {
		return ErrInvalid
	}
	return nil
}

func finishedDeliveryStatus(input FinishDeliveryInput) string {
	if input.Delivered {
		return DeliveryDelivered
	}
	return DeliveryFailed
}

func boundedDeliveryError(message string) string {
	message = RedactForLog(strings.TrimSpace(message))
	if len(message) > deliveryErrorMaxBytes {
		message = strings.ToValidUTF8(message[:deliveryErrorMaxBytes], "")
	}
	return message
}

func deliveryMarker(prefix, key string) string {
	return prefix + key + " -->"
}

// DeliveryMessage is what an adapter publishes: the exact content bytes
// followed by a provenance footer and the idempotency marker.
type DeliveryMessage struct {
	IdempotencyKey string
	RunID          string
	ArtifactSHA256 string
	Body           string
}

// DeliveryResult identifies the published object at the destination.
type DeliveryResult struct {
	ExternalID  string
	ExternalURL string
}

// DeliveryAdapter publishes to exactly one configured destination.
// FindDelivery must report whether a message carrying the idempotency key is
// already present, so a retry after a lost response does not post again.
type DeliveryAdapter interface {
	Name() string
	Destination() string
	FindDelivery(ctx context.Context, idempotencyKey string) (DeliveryResult, bool, error)
	Deliver(ctx context.Context, message DeliveryMessage) (DeliveryResult, error)
}

// deliveryAttempt is the outcome of reconciling and, when needed, posting one
// claimed delivery.
type deliveryAttempt struct {
	Outcome string // reconciled, delivered, or failed
	Finish  FinishDeliveryInput
}

// attemptClaimedDelivery searches the destination for the idempotency marker
// and posts only when it is absent. A destination that cannot be searched is
// never posted to.
func attemptClaimedDelivery(ctx context.Context, adapter DeliveryAdapter, message DeliveryMessage) deliveryAttempt {
	existing, found, err := adapter.FindDelivery(ctx, message.IdempotencyKey)
	if err != nil {
		return deliveryAttempt{Outcome: "failed", Finish: FinishDeliveryInput{Error: "reconcile with destination: " + err.Error()}}
	}
	if found {
		return deliveryAttempt{Outcome: "reconciled", Finish: FinishDeliveryInput{Delivered: true, Reconciled: true, ExternalID: existing.ExternalID, ExternalURL: existing.ExternalURL}}
	}
	result, err := adapter.Deliver(ctx, message)
	if err != nil {
		return deliveryAttempt{Outcome: "failed", Finish: FinishDeliveryInput{Error: "deliver: " + err.Error()}}
	}
	return deliveryAttempt{Outcome: "delivered", Finish: FinishDeliveryInput{Delivered: true, ExternalID: result.ExternalID, ExternalURL: result.ExternalURL}}
}

// issueCommentSettingNames names the environment settings of one Gitea issue
// comment destination, so configuration errors cite the right keys.
type issueCommentSettingNames struct {
	Adapter      string
	Issue        string
	AllowedRepos string
}

// issueCommentAdapterFromConfig builds the gitea-issue-comment adapter only
// when the adapter is selected, the destination parses, and its repository is
// allowlisted. Anything else leaves delivery disabled with a reason; it never
// falls back to a default destination.
func issueCommentAdapterFromConfig(names issueCommentSettingNames, markerPrefix, adapterName, issue, allowedRepos string, giteaConfigured bool, client func() GiteaIssueCommentClient) (DeliveryAdapter, string) {
	switch strings.TrimSpace(adapterName) {
	case "", DeliveryAdapterDisabled:
		return nil, names.Adapter + " is disabled"
	case DeliveryAdapterGiteaIssueComment:
	default:
		return nil, fmt.Sprintf("%s %q is not supported; use %s or %s", names.Adapter, strings.TrimSpace(adapterName), DeliveryAdapterDisabled, DeliveryAdapterGiteaIssueComment)
	}
	repo, number, err := parseGiteaIssueDestination(issue)
	if err != nil {
		return nil, names.Issue + " " + err.Error()
	}
	allowed := false
	for _, slug := range splitCSV(allowedRepos) {
		if strings.EqualFold(slug, repo) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Sprintf("destination repository %s is not listed in %s", repo, names.AllowedRepos)
	}
	if !giteaConfigured {
		return nil, "GITEA_BASE_URL and GITEA_TOKEN are required for the gitea-issue-comment adapter"
	}
	return &giteaIssueCommentDelivery{client: client(), repo: repo, issue: number, markerPrefix: markerPrefix}, ""
}

func parseGiteaIssueDestination(value string) (string, int, error) {
	value = strings.TrimSpace(value)
	repo, rawNumber, ok := strings.Cut(value, "#")
	if value == "" {
		return "", 0, errors.New("is not set")
	}
	number, err := strconv.Atoi(rawNumber)
	if !ok || err != nil || number < 1 || !validOpenPilotRepositorySlug(repo) {
		return "", 0, errors.New("must be owner/repo#number")
	}
	return repo, number, nil
}

// GiteaIssueComment is the subset of a Gitea issue comment the delivery
// adapter needs.
type GiteaIssueComment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	User    *struct {
		Login string `json:"login"`
	} `json:"user,omitempty"`
}

// GiteaIssueCommentClient lists and creates comments on one issue and reports
// the authenticated account, so reconciliation only trusts markers written by
// the delivery identity.
type GiteaIssueCommentClient interface {
	CurrentUserLogin(ctx context.Context) (string, error)
	ListIssueComments(ctx context.Context, repo string, issue, page, limit int) ([]GiteaIssueComment, error)
	CreateIssueComment(ctx context.Context, repo string, issue int, body string) (GiteaIssueComment, error)
}

type giteaIssueCommentDelivery struct {
	client GiteaIssueCommentClient
	repo   string
	issue  int
	// markerPrefix namespaces the idempotency marker; empty means Devlog.
	markerPrefix string
}

func (g *giteaIssueCommentDelivery) Name() string { return DeliveryAdapterGiteaIssueComment }
func (g *giteaIssueCommentDelivery) Destination() string {
	return g.repo + "#" + strconv.Itoa(g.issue)
}

func (g *giteaIssueCommentDelivery) marker(key string) string {
	return deliveryMarker(firstNonEmpty(g.markerPrefix, devlogDeliveryMarkerPrefix), key)
}

// FindDelivery scans the issue comments for the marker. It refuses to report
// absence when the scan hit its page cap, so an unconfirmed state never leads
// to a second post.
func (g *giteaIssueCommentDelivery) FindDelivery(ctx context.Context, key string) (DeliveryResult, bool, error) {
	login, err := g.client.CurrentUserLogin(ctx)
	if err != nil {
		return DeliveryResult{}, false, fmt.Errorf("resolve delivery identity: %w", err)
	}
	marker := g.marker(key)
	seen := map[int64]bool{}
	for page := 1; page <= giteaCommentScanMaxPages; page++ {
		comments, err := g.client.ListIssueComments(ctx, g.repo, g.issue, page, giteaCommentScanPageSize)
		if err != nil {
			return DeliveryResult{}, false, fmt.Errorf("list destination comments: %w", err)
		}
		fresh := 0
		for _, comment := range comments {
			if seen[comment.ID] {
				continue
			}
			seen[comment.ID] = true
			fresh++
			if comment.User != nil && strings.EqualFold(comment.User.Login, login) && strings.Contains(comment.Body, marker) {
				return DeliveryResult{ExternalID: strconv.FormatInt(comment.ID, 10), ExternalURL: comment.HTMLURL}, true, nil
			}
		}
		// A short page, or a page with nothing new (the endpoint ignored
		// paging and returned the full list), ends the scan.
		if len(comments) < giteaCommentScanPageSize || fresh == 0 {
			return DeliveryResult{}, false, nil
		}
	}
	return DeliveryResult{}, false, fmt.Errorf("destination has more than %d comments; cannot confirm the report was not already delivered", giteaCommentScanPageSize*giteaCommentScanMaxPages)
}

func (g *giteaIssueCommentDelivery) Deliver(ctx context.Context, message DeliveryMessage) (DeliveryResult, error) {
	comment, err := g.client.CreateIssueComment(ctx, g.repo, g.issue, message.Body)
	if err != nil {
		return DeliveryResult{}, err
	}
	return DeliveryResult{ExternalID: strconv.FormatInt(comment.ID, 10), ExternalURL: comment.HTMLURL}, nil
}
