package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
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

	DevlogDeliveryAdapterDisabled          = DeliveryAdapterDisabled
	DevlogDeliveryAdapterGiteaIssueComment = DeliveryAdapterGiteaIssueComment

	DevlogApprovalApproved = "approved"
	DevlogApprovalRejected = "rejected"

	DevlogDeliverySending   = DeliverySending
	DevlogDeliveryDelivered = DeliveryDelivered
	DevlogDeliveryFailed    = DeliveryFailed

	devlogApprovalTTL           = 24 * time.Hour
	devlogDeliveryClaimTTL      = 2 * time.Minute
	devlogDeliveryTimeout       = 30 * time.Second
	devlogDeliveryMaxBodyBytes  = 60000
	devlogDeliveryListLimit     = 50
	devlogDeliveryMarkerPrefix  = "<!-- switchyard-devlog-delivery:"
	devlogDeliveryErrorMaxBytes = deliveryErrorMaxBytes
	devlogGiteaCommentPageSize  = giteaCommentScanPageSize
	devlogGiteaCommentMaxPages  = giteaCommentScanMaxPages
)

var devlogSHA256Pattern = sha256HexPattern

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

// Devlog delivery reuses the generic delivery receipt, claim, and adapter
// contract in delivery.go.
type (
	DevlogDeliveryReceipt     = DeliveryReceipt
	BeginDevlogDeliveryInput  = BeginDeliveryInput
	FinishDevlogDeliveryInput = FinishDeliveryInput
	DevlogDeliveryMessage     = DeliveryMessage
	DevlogDeliveryResult      = DeliveryResult
	DevlogDeliveryAdapter     = DeliveryAdapter
)

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
	return validateBeginDelivery(input)
}

func finishedDevlogDeliveryStatus(input FinishDevlogDeliveryInput) string {
	return finishedDeliveryStatus(input)
}

func boundedDevlogDeliveryError(message string) string {
	return boundedDeliveryError(message)
}

// devlogDeliveryIdempotencyKey binds a delivery to the adapter, destination,
// and exact report digest. The same bytes are never delivered twice to the
// same destination, whichever run produced them.
func devlogDeliveryIdempotencyKey(adapter, destination, artifactSHA256 string) string {
	sum := sha256.Sum256([]byte("switchyard.devlog-delivery.v1\n" + adapter + "\n" + destination + "\n" + artifactSHA256))
	return hex.EncodeToString(sum[:])
}

func devlogDeliveryMarker(key string) string {
	return deliveryMarker(devlogDeliveryMarkerPrefix, key)
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

var devlogDeliverySettingNames = issueCommentSettingNames{Adapter: "DEVLOG_DELIVERY_ADAPTER", Issue: "DEVLOG_DELIVERY_GITEA_ISSUE", AllowedRepos: "DEVLOG_DELIVERY_ALLOWED_REPOS"}

func devlogDeliveryAdapterFromConfig(adapterName, issue, allowedRepos string, giteaConfigured bool, client func() GiteaIssueCommentClient) (DevlogDeliveryAdapter, string) {
	return issueCommentAdapterFromConfig(devlogDeliverySettingNames, devlogDeliveryMarkerPrefix, adapterName, issue, allowedRepos, giteaConfigured, client)
}

func parseDevlogGiteaIssueDestination(value string) (string, int, error) {
	return parseGiteaIssueDestination(value)
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
