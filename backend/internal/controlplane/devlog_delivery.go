package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Devlog delivery publishes one reviewed Devlog report to one explicitly
// configured destination. It is disabled unless the operator configures an
// adapter, a destination, and an allowlist that contains the destination.
// Every delivery needs an admin approval bound to the exact report artifact
// SHA-256; a durable receipt keyed by (adapter, destination, digest) makes
// retries reconcile instead of posting twice.

const (
	devlogReportArtifactName = "devlog.md"

	DevlogDeliveryAdapterDisabled          = "disabled"
	DevlogDeliveryAdapterGiteaIssueComment = "gitea-issue-comment"

	DevlogApprovalApproved = "approved"
	DevlogApprovalRejected = "rejected"

	DevlogDeliverySending   = "sending"
	DevlogDeliveryDelivered = "delivered"
	DevlogDeliveryFailed    = "failed"

	devlogApprovalTTL           = 24 * time.Hour
	devlogDeliveryClaimTTL      = 2 * time.Minute
	devlogDeliveryTimeout       = 30 * time.Second
	devlogDeliveryMaxBodyBytes  = 60000
	devlogDeliveryListLimit     = 50
	devlogDeliveryMarkerPrefix  = "<!-- switchyard-devlog-delivery:"
	devlogDeliveryErrorMaxBytes = 1000
	devlogGiteaCommentPageSize  = 50
	devlogGiteaCommentMaxPages  = 20
)

var devlogSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DevlogDeliveryApproval is one admin decision about delivering the exact
// report artifact digest to the exact destination.
type DevlogDeliveryApproval struct {
	ID             string    `json:"id"`
	RunID          string    `json:"run_id"`
	ArtifactID     string    `json:"artifact_id"`
	ArtifactSHA256 string    `json:"artifact_sha256"`
	Adapter        string    `json:"adapter"`
	Destination    string    `json:"destination"`
	Decision       string    `json:"decision"`
	Actor          string    `json:"actor"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// DevlogDeliveryReceipt is the durable record of one delivery identity. The
// idempotency key is unique, so every attempt for the same report digest and
// destination updates the same receipt.
type DevlogDeliveryReceipt struct {
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

// BeginDevlogDeliveryInput claims the delivery identity for one attempt.
type BeginDevlogDeliveryInput struct {
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

// FinishDevlogDeliveryInput records the outcome of a claimed attempt.
type FinishDevlogDeliveryInput struct {
	IdempotencyKey string
	ClaimToken     string
	Delivered      bool
	Reconciled     bool
	ExternalID     string
	ExternalURL    string
	Error          string
}

func validateDevlogDeliveryApproval(approval DevlogDeliveryApproval) error {
	if approval.ID == "" || approval.RunID == "" || approval.ArtifactID == "" || !devlogSHA256Pattern.MatchString(approval.ArtifactSHA256) ||
		approval.Adapter == "" || approval.Destination == "" || approval.Actor == "" || approval.CreatedAt.IsZero() || !approval.ExpiresAt.After(approval.CreatedAt) {
		return ErrInvalid
	}
	if approval.Decision != DevlogApprovalApproved && approval.Decision != DevlogApprovalRejected {
		return ErrInvalid
	}
	return nil
}

func validateBeginDevlogDelivery(input BeginDevlogDeliveryInput) error {
	if input.IdempotencyKey == "" || input.RunID == "" || input.ArtifactID == "" || !devlogSHA256Pattern.MatchString(input.ArtifactSHA256) ||
		input.Adapter == "" || input.Destination == "" || input.ApprovalID == "" || input.Actor == "" {
		return ErrInvalid
	}
	return nil
}

func finishedDevlogDeliveryStatus(input FinishDevlogDeliveryInput) string {
	if input.Delivered {
		return DevlogDeliveryDelivered
	}
	return DevlogDeliveryFailed
}

func boundedDevlogDeliveryError(message string) string {
	message = RedactForLog(strings.TrimSpace(message))
	if len(message) > devlogDeliveryErrorMaxBytes {
		message = strings.ToValidUTF8(message[:devlogDeliveryErrorMaxBytes], "")
	}
	return message
}

// devlogDeliveryIdempotencyKey binds a delivery to the adapter, destination,
// and exact report digest. The same bytes are never delivered twice to the
// same destination, whichever run produced them.
func devlogDeliveryIdempotencyKey(adapter, destination, artifactSHA256 string) string {
	sum := sha256.Sum256([]byte("switchyard.devlog-delivery.v1\n" + adapter + "\n" + destination + "\n" + artifactSHA256))
	return hex.EncodeToString(sum[:])
}

func devlogDeliveryMarker(key string) string {
	return devlogDeliveryMarkerPrefix + key + " -->"
}

// DevlogDeliveryMessage is what an adapter publishes: the exact report bytes
// followed by a provenance footer and the idempotency marker.
type DevlogDeliveryMessage struct {
	IdempotencyKey string
	RunID          string
	ArtifactSHA256 string
	Body           string
}

// DevlogDeliveryResult identifies the published object at the destination.
type DevlogDeliveryResult struct {
	ExternalID  string
	ExternalURL string
}

// DevlogDeliveryAdapter publishes to exactly one configured destination.
// FindDelivery must report whether a message carrying the idempotency key is
// already present, so a retry after a lost response does not post again.
type DevlogDeliveryAdapter interface {
	Name() string
	Destination() string
	FindDelivery(ctx context.Context, idempotencyKey string) (DevlogDeliveryResult, bool, error)
	Deliver(ctx context.Context, message DevlogDeliveryMessage) (DevlogDeliveryResult, error)
}

// DevlogDelivery holds the configured adapter. A nil adapter means delivery is
// disabled; Reason explains why.
type DevlogDelivery struct {
	adapter DevlogDeliveryAdapter
	reason  string
	now     func() time.Time
}

// NewDevlogDelivery wraps an adapter. Pass a nil adapter and a reason to
// represent disabled delivery.
func NewDevlogDelivery(adapter DevlogDeliveryAdapter, disabledReason string) *DevlogDelivery {
	if adapter == nil && strings.TrimSpace(disabledReason) == "" {
		disabledReason = "Devlog delivery is not configured"
	}
	return &DevlogDelivery{adapter: adapter, reason: disabledReason, now: time.Now}
}

func (d *DevlogDelivery) enabled() bool { return d != nil && d.adapter != nil }

// DevlogDeliveryStatus is the public, secret-free view of the configuration.
type DevlogDeliveryStatus struct {
	Enabled            bool   `json:"enabled"`
	Adapter            string `json:"adapter"`
	Destination        string `json:"destination,omitempty"`
	Reason             string `json:"reason,omitempty"`
	ApprovalTTLSeconds int    `json:"approval_ttl_seconds"`
	Policy             string `json:"policy"`
}

const devlogDeliveryPolicy = "An admin approval bound to the exact report SHA-256 and destination is required for every delivery; approvals expire, a later rejection blocks delivery, and a retry reconciles with the destination before posting."

func (d *DevlogDelivery) status() DevlogDeliveryStatus {
	status := DevlogDeliveryStatus{Adapter: DevlogDeliveryAdapterDisabled, ApprovalTTLSeconds: int(devlogApprovalTTL / time.Second), Policy: devlogDeliveryPolicy}
	if !d.enabled() {
		if d != nil {
			status.Reason = d.reason
		} else {
			status.Reason = "Devlog delivery is not configured"
		}
		return status
	}
	status.Enabled = true
	status.Adapter = d.adapter.Name()
	status.Destination = d.adapter.Destination()
	return status
}

// NewEnvDevlogDelivery reads DEVLOG_DELIVERY_ADAPTER, DEVLOG_DELIVERY_GITEA_ISSUE,
// and DEVLOG_DELIVERY_ALLOWED_REPOS. Any incomplete or inconsistent setting
// leaves delivery disabled with a reason; it never falls back to a default
// destination.
func NewEnvDevlogDelivery() *DevlogDelivery {
	adapter, reason := devlogDeliveryAdapterFromConfig(
		os.Getenv("DEVLOG_DELIVERY_ADAPTER"),
		os.Getenv("DEVLOG_DELIVERY_GITEA_ISSUE"),
		os.Getenv("DEVLOG_DELIVERY_ALLOWED_REPOS"),
		strings.TrimSpace(os.Getenv("GITEA_BASE_URL")) != "" && strings.TrimSpace(os.Getenv("GITEA_TOKEN")) != "",
		func() GiteaIssueCommentClient { return NewEnvGiteaClient() },
	)
	return NewDevlogDelivery(adapter, reason)
}

func devlogDeliveryAdapterFromConfig(adapterName, issue, allowedRepos string, giteaConfigured bool, client func() GiteaIssueCommentClient) (DevlogDeliveryAdapter, string) {
	switch strings.TrimSpace(adapterName) {
	case "", DevlogDeliveryAdapterDisabled:
		return nil, "DEVLOG_DELIVERY_ADAPTER is disabled"
	case DevlogDeliveryAdapterGiteaIssueComment:
	default:
		return nil, fmt.Sprintf("DEVLOG_DELIVERY_ADAPTER %q is not supported; use %s or %s", strings.TrimSpace(adapterName), DevlogDeliveryAdapterDisabled, DevlogDeliveryAdapterGiteaIssueComment)
	}
	repo, number, err := parseDevlogGiteaIssueDestination(issue)
	if err != nil {
		return nil, "DEVLOG_DELIVERY_GITEA_ISSUE " + err.Error()
	}
	allowed := false
	for _, slug := range splitCSV(allowedRepos) {
		if strings.EqualFold(slug, repo) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Sprintf("destination repository %s is not listed in DEVLOG_DELIVERY_ALLOWED_REPOS", repo)
	}
	if !giteaConfigured {
		return nil, "GITEA_BASE_URL and GITEA_TOKEN are required for the gitea-issue-comment adapter"
	}
	return &giteaIssueCommentDelivery{client: client(), repo: repo, issue: number}, ""
}

func parseDevlogGiteaIssueDestination(value string) (string, int, error) {
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
}

func (g *giteaIssueCommentDelivery) Name() string { return DevlogDeliveryAdapterGiteaIssueComment }
func (g *giteaIssueCommentDelivery) Destination() string {
	return g.repo + "#" + strconv.Itoa(g.issue)
}

// FindDelivery scans the issue comments for the marker. It refuses to report
// absence when the scan hit its page cap, so an unconfirmed state never leads
// to a second post.
func (g *giteaIssueCommentDelivery) FindDelivery(ctx context.Context, key string) (DevlogDeliveryResult, bool, error) {
	login, err := g.client.CurrentUserLogin(ctx)
	if err != nil {
		return DevlogDeliveryResult{}, false, fmt.Errorf("resolve delivery identity: %w", err)
	}
	marker := devlogDeliveryMarker(key)
	seen := map[int64]bool{}
	for page := 1; page <= devlogGiteaCommentMaxPages; page++ {
		comments, err := g.client.ListIssueComments(ctx, g.repo, g.issue, page, devlogGiteaCommentPageSize)
		if err != nil {
			return DevlogDeliveryResult{}, false, fmt.Errorf("list destination comments: %w", err)
		}
		fresh := 0
		for _, comment := range comments {
			if seen[comment.ID] {
				continue
			}
			seen[comment.ID] = true
			fresh++
			if comment.User != nil && strings.EqualFold(comment.User.Login, login) && strings.Contains(comment.Body, marker) {
				return DevlogDeliveryResult{ExternalID: strconv.FormatInt(comment.ID, 10), ExternalURL: comment.HTMLURL}, true, nil
			}
		}
		// A short page, or a page with nothing new (the endpoint ignored
		// paging and returned the full list), ends the scan.
		if len(comments) < devlogGiteaCommentPageSize || fresh == 0 {
			return DevlogDeliveryResult{}, false, nil
		}
	}
	return DevlogDeliveryResult{}, false, fmt.Errorf("destination has more than %d comments; cannot confirm the report was not already delivered", devlogGiteaCommentPageSize*devlogGiteaCommentMaxPages)
}

func (g *giteaIssueCommentDelivery) Deliver(ctx context.Context, message DevlogDeliveryMessage) (DevlogDeliveryResult, error) {
	comment, err := g.client.CreateIssueComment(ctx, g.repo, g.issue, message.Body)
	if err != nil {
		return DevlogDeliveryResult{}, err
	}
	return DevlogDeliveryResult{ExternalID: strconv.FormatInt(comment.ID, 10), ExternalURL: comment.HTMLURL}, nil
}

// DevlogReportSource describes what the report was generated from, shown to
// the reviewer before any approval.
type DevlogReportSource struct {
	Mode         string   `json:"mode"`
	Description  string   `json:"description"`
	Since        string   `json:"since,omitempty"`
	Until        string   `json:"until,omitempty"`
	Repositories []string `json:"repositories"`
	Complete     *bool    `json:"complete,omitempty"`
	Items        *int     `json:"items,omitempty"`
	Missing      []string `json:"missing"`
}

func devlogReportSource(run Run, evidence *Artifact) DevlogReportSource {
	if evidence != nil {
		if _, raw, err := decodeInlineArtifactURI(evidence.URI); err == nil {
			var report DevlogEvidenceReport
			if json.Unmarshal(raw, &report) == nil && report.Version == 1 {
				complete := report.Complete
				items := len(report.Items)
				source := DevlogReportSource{Mode: "repository-evidence", Since: report.Window.Since, Until: report.Window.Until, Repositories: report.repositoryNames(), Complete: &complete, Items: &items, Missing: report.Missing}
				source.Description = fmt.Sprintf("Repository evidence from %s to %s (half-open window)", report.Window.Since, report.Window.Until)
				if source.Missing == nil {
					source.Missing = []string{}
				}
				if source.Repositories == nil {
					source.Repositories = []string{}
				}
				return source
			}
		}
		return DevlogReportSource{Mode: "repository-evidence", Description: devlogEvidenceArtifactName + " could not be decoded; review it before approving", Repositories: []string{}, Missing: []string{devlogEvidenceArtifactName + " unreadable"}}
	}
	if devlogEvidenceRequested(run.Inputs) {
		return DevlogReportSource{Mode: "repository-evidence", Description: "Repository evidence was requested but no " + devlogEvidenceArtifactName + " artifact is attached", Since: strings.TrimSpace(run.Inputs[devlogEvidenceSinceInput]), Until: strings.TrimSpace(run.Inputs[devlogEvidenceUntilInput]), Repositories: splitCSV(run.Inputs[devlogEvidenceRepositoriesInput]), Missing: []string{devlogEvidenceArtifactName + " missing"}}
	}
	source := DevlogReportSource{Mode: "run-history", Description: "Recent Switchyard run history (up to 8 non-Devlog runs) recorded before this run", Repositories: []string{}, Missing: []string{}}
	if !run.CreatedAt.IsZero() {
		source.Until = run.CreatedAt.UTC().Format(time.RFC3339)
	}
	return source
}

func devlogDeliveryBody(report []byte, run Run, sha string, source DevlogReportSource, key string) string {
	window := source.Description
	if source.Since != "" && source.Until != "" {
		window = source.Since + " to " + source.Until
	} else if source.Until != "" {
		window = "run history as of " + source.Until
	}
	var builder strings.Builder
	builder.Write(report)
	builder.WriteString("\n\n---\n\n")
	builder.WriteString("_Delivered by Switchyard Devlog from run `" + run.ID + "`; `" + devlogReportArtifactName + "` sha256 `" + sha + "`; source window: " + window + "._\n\n")
	builder.WriteString(devlogDeliveryMarker(key) + "\n")
	return builder.String()
}
