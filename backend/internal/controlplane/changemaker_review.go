package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"
)

// Changemaker review, revision, and opt-in publication.
//
// A grounded Changemaker run produces a draft (changemaker-digest.md) and the
// SHA-256 of the source evidence it was drafted from. Review happens per
// thread: version 1 is the original run and each revision request appends a
// new version backed by a new run. Every decision is bound to the exact draft
// digest and evidence digest it was made against. A revision drafted from the
// same sources restores the collect-sources outputs of the run that collected
// them, so the evidence digest is unchanged; a re-collected revision queries
// the same window again and records whatever evidence digest results.
//
// Publication is off unless an operator configures one destination and its
// allowlist. It needs an unexpired admin approval of the latest version's
// exact draft and evidence digests, and a receipt keyed by (adapter,
// destination, draft digest) makes retries reconcile instead of posting
// twice. No run, scheduled or manual, publishes by itself: the pipeline has no
// publication step, and publication is only an admin control-plane request.

const (
	changemakerDraftArtifactName    = "changemaker-digest.md"
	changemakerEvidenceArtifactName = "changemaker-sources.json"
	changemakerDraftStepID          = "changemaker-draft"
	changemakerCollectStepID        = "collect-sources"
	changemakerAttachStepID         = "attach"

	ChangemakerDecisionApproved          = "approved"
	ChangemakerDecisionRejected          = "rejected"
	ChangemakerDecisionRevisionRequested = "revision-requested"

	ChangemakerSourceModeOriginal    = "original"
	ChangemakerSourceModeSame        = "same"
	ChangemakerSourceModeRecollected = "recollected"

	changemakerApprovalTTL            = 24 * time.Hour
	changemakerPublicationClaimTTL    = 2 * time.Minute
	changemakerPublicationTimeout     = 30 * time.Second
	changemakerPublicationMaxBytes    = 60000
	changemakerPublicationMarkerStart = "<!-- switchyard-changemaker-publication:"
	changemakerMaxDraftVersions       = 10
	changemakerNotesMaxBytes          = 2000
	changemakerPreviousDraftMaxBytes  = 16000
	changemakerReviewListLimit        = 200
	changemakerReceiptListLimit       = 50

	changemakerRevisionThreadInput  = "changemaker_revision_thread"
	changemakerRevisionVersionInput = "changemaker_revision_version"
	changemakerRevisionSourcesInput = "changemaker_revision_sources"
)

// ChangemakerDraftVersion is one entry in a thread's revision history.
// Version 1 is the original run (thread ID = its run ID); it is persisted when
// the first revision is requested.
type ChangemakerDraftVersion struct {
	ID            string    `json:"id"`
	ThreadID      string    `json:"thread_id"`
	Version       int       `json:"version"`
	RunID         string    `json:"run_id"`
	ParentVersion int       `json:"parent_version,omitempty"`
	SourcesRunID  string    `json:"sources_run_id"`
	SourceMode    string    `json:"source_mode"`
	RevisionNotes string    `json:"revision_notes,omitempty"`
	RequestedBy   string    `json:"requested_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ChangemakerReviewDecision is one reviewer decision bound to the exact draft
// and evidence digests of one version. Adapter and destination record the
// publication target configured when the decision was made; an approval only
// authorizes publication to that same target, before ExpiresAt.
type ChangemakerReviewDecision struct {
	ID             string    `json:"id"`
	ThreadID       string    `json:"thread_id"`
	Version        int       `json:"version"`
	RunID          string    `json:"run_id"`
	ArtifactID     string    `json:"artifact_id"`
	DraftSHA256    string    `json:"draft_sha256"`
	EvidenceSHA256 string    `json:"evidence_sha256"`
	Decision       string    `json:"decision"`
	Notes          string    `json:"notes"`
	Actor          string    `json:"actor"`
	Adapter        string    `json:"adapter"`
	Destination    string    `json:"destination"`
	RevisionRunID  string    `json:"revision_run_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// ChangemakerRevisionInput atomically records a revision request and the new
// version it created. BaseVersion is the version being revised; it is the
// latest version unless every later version's run failed or was canceled.
// ExpectedLatestVersion must still be the thread's highest version, so two
// concurrent requests cannot both append.
type ChangemakerRevisionInput struct {
	Root                  ChangemakerDraftVersion
	BaseVersion           int
	ExpectedLatestVersion int
	Version               ChangemakerDraftVersion
	Decision              ChangemakerReviewDecision
}

// ChangemakerPublicationReceipt is a generic delivery receipt in the
// Changemaker receipt table.
type ChangemakerPublicationReceipt = DeliveryReceipt

func validChangemakerSourceMode(mode string) bool {
	switch mode {
	case ChangemakerSourceModeOriginal, ChangemakerSourceModeSame, ChangemakerSourceModeRecollected:
		return true
	}
	return false
}

func validateChangemakerDraftVersion(version ChangemakerDraftVersion) error {
	if version.ID == "" || version.ThreadID == "" || version.Version < 1 || version.RunID == "" || version.SourcesRunID == "" || !validChangemakerSourceMode(version.SourceMode) || version.CreatedAt.IsZero() {
		return ErrInvalid
	}
	if (version.Version == 1) != (version.ParentVersion == 0) || version.ParentVersion >= version.Version || version.ParentVersion < 0 {
		return ErrInvalid
	}
	if (version.Version == 1) != (version.SourceMode == ChangemakerSourceModeOriginal) {
		return ErrInvalid
	}
	return nil
}

func validateChangemakerReviewDecision(decision ChangemakerReviewDecision) error {
	if decision.ID == "" || decision.ThreadID == "" || decision.Version < 1 || decision.RunID == "" || decision.ArtifactID == "" ||
		!sha256HexPattern.MatchString(decision.DraftSHA256) || !sha256HexPattern.MatchString(decision.EvidenceSHA256) ||
		decision.Actor == "" || decision.Adapter == "" || decision.CreatedAt.IsZero() || !decision.ExpiresAt.After(decision.CreatedAt) {
		return ErrInvalid
	}
	switch decision.Decision {
	case ChangemakerDecisionApproved, ChangemakerDecisionRejected:
		if decision.RevisionRunID != "" {
			return ErrInvalid
		}
	case ChangemakerDecisionRevisionRequested:
		if decision.RevisionRunID == "" || strings.TrimSpace(decision.Notes) == "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateChangemakerRevisionInput(input ChangemakerRevisionInput) error {
	if err := validateChangemakerDraftVersion(input.Root); err != nil || input.Root.Version != 1 || input.Root.ThreadID != input.Root.RunID {
		return ErrInvalid
	}
	if err := validateChangemakerDraftVersion(input.Version); err != nil {
		return err
	}
	if err := validateChangemakerReviewDecision(input.Decision); err != nil {
		return err
	}
	if input.Version.ThreadID != input.Root.ThreadID || input.Decision.ThreadID != input.Root.ThreadID || input.BaseVersion < 1 || input.BaseVersion > input.ExpectedLatestVersion ||
		input.Version.Version != input.ExpectedLatestVersion+1 || input.Version.ParentVersion != input.BaseVersion || input.Version.Version > changemakerMaxDraftVersions ||
		input.Decision.Version != input.BaseVersion || input.Decision.Decision != ChangemakerDecisionRevisionRequested || input.Decision.RevisionRunID != input.Version.RunID {
		return ErrInvalid
	}
	return nil
}

// changemakerPublicationIdempotencyKey binds a publication to the adapter,
// destination, and exact draft digest.
func changemakerPublicationIdempotencyKey(adapter, destination, draftSHA256 string) string {
	sum := sha256.Sum256([]byte("switchyard.changemaker-publication.v1\n" + adapter + "\n" + destination + "\n" + draftSHA256))
	return hex.EncodeToString(sum[:])
}

func changemakerPublicationMarker(key string) string {
	return deliveryMarker(changemakerPublicationMarkerStart, key)
}

// ChangemakerPublication holds the configured publisher. A nil adapter means
// publication is disabled; reason explains why.
type ChangemakerPublication struct {
	adapter DeliveryAdapter
	reason  string
}

// NewChangemakerPublication wraps an adapter. Pass nil and a reason to
// represent disabled publication.
func NewChangemakerPublication(adapter DeliveryAdapter, disabledReason string) *ChangemakerPublication {
	if adapter == nil && strings.TrimSpace(disabledReason) == "" {
		disabledReason = "Changemaker publication is not configured"
	}
	return &ChangemakerPublication{adapter: adapter, reason: disabledReason}
}

func (p *ChangemakerPublication) enabled() bool { return p != nil && p.adapter != nil }

// ChangemakerPublicationStatus is the secret-free view of the configuration.
type ChangemakerPublicationStatus struct {
	Enabled            bool   `json:"enabled"`
	Adapter            string `json:"adapter"`
	Destination        string `json:"destination,omitempty"`
	Reason             string `json:"reason,omitempty"`
	ApprovalTTLSeconds int    `json:"approval_ttl_seconds"`
	MaxDraftVersions   int    `json:"max_draft_versions"`
	Policy             string `json:"policy"`
}

const changemakerPublicationPolicy = "Scheduled and manual runs never publish. An admin approval bound to the latest version's exact draft and source evidence SHA-256 and to this destination is required; approvals expire, a later decision replaces them, and a retry reconciles with the destination before posting."

func (p *ChangemakerPublication) status() ChangemakerPublicationStatus {
	status := ChangemakerPublicationStatus{Adapter: DeliveryAdapterDisabled, ApprovalTTLSeconds: int(changemakerApprovalTTL / time.Second), MaxDraftVersions: changemakerMaxDraftVersions, Policy: changemakerPublicationPolicy}
	if !p.enabled() {
		if p != nil {
			status.Reason = p.reason
		} else {
			status.Reason = "Changemaker publication is not configured"
		}
		return status
	}
	status.Enabled = true
	status.Adapter = p.adapter.Name()
	status.Destination = p.adapter.Destination()
	return status
}

var changemakerPublicationSettingNames = issueCommentSettingNames{Adapter: "CHANGEMAKER_PUBLICATION_ADAPTER", Issue: "CHANGEMAKER_PUBLICATION_GITEA_ISSUE", AllowedRepos: "CHANGEMAKER_PUBLICATION_ALLOWED_REPOS"}

// NewEnvChangemakerPublication reads CHANGEMAKER_PUBLICATION_ADAPTER,
// CHANGEMAKER_PUBLICATION_GITEA_ISSUE, and
// CHANGEMAKER_PUBLICATION_ALLOWED_REPOS. Incomplete or inconsistent settings
// leave publication disabled with a reason.
func NewEnvChangemakerPublication() *ChangemakerPublication {
	adapter, reason := changemakerPublicationAdapterFromConfig(
		os.Getenv("CHANGEMAKER_PUBLICATION_ADAPTER"),
		os.Getenv("CHANGEMAKER_PUBLICATION_GITEA_ISSUE"),
		os.Getenv("CHANGEMAKER_PUBLICATION_ALLOWED_REPOS"),
		strings.TrimSpace(os.Getenv("GITEA_BASE_URL")) != "" && strings.TrimSpace(os.Getenv("GITEA_TOKEN")) != "",
		func() GiteaIssueCommentClient { return NewEnvGiteaClient() },
	)
	return NewChangemakerPublication(adapter, reason)
}

func changemakerPublicationAdapterFromConfig(adapterName, issue, allowedRepos string, giteaConfigured bool, client func() GiteaIssueCommentClient) (DeliveryAdapter, string) {
	return issueCommentAdapterFromConfig(changemakerPublicationSettingNames, changemakerPublicationMarkerStart, adapterName, issue, allowedRepos, giteaConfigured, client)
}

// changemakerPublicationBody is the exact draft followed by a provenance
// footer and the idempotency marker.
func changemakerPublicationBody(draft []byte, version ChangemakerDraftVersion, draftSHA, evidenceSHA, key string) string {
	var builder strings.Builder
	builder.Write(draft)
	builder.WriteString("\n\n---\n\n")
	builder.WriteString("_Published by Switchyard Changemaker from run `" + version.RunID + "` (draft version " + strconv.Itoa(version.Version) + " of thread `" + version.ThreadID + "`); `" + changemakerDraftArtifactName + "` sha256 `" + draftSHA + "`; source evidence sha256 `" + evidenceSHA + "`._\n\n")
	builder.WriteString(changemakerPublicationMarker(key) + "\n")
	return builder.String()
}

// neutralizeChangemakerRevisionText keeps reviewer notes and earlier drafts
// from being read as template references or closing the prompt's tags.
func neutralizeChangemakerRevisionText(value string, maxBytes int) string {
	value = strings.ToValidUTF8(value, "")
	value = strings.ReplaceAll(value, "{{", "{ {")
	value = strings.ReplaceAll(value, "}}", "} }")
	for _, tag := range []string{"</reviewer-notes>", "</previous-draft>", "</collected-sources>", "<collected-sources>", "</writer-request>", "<writer-request>"} {
		value = strings.ReplaceAll(value, tag, "[removed tag]")
	}
	if len(value) > maxBytes {
		value = strings.ToValidUTF8(value[:maxBytes], "") + "\n[truncated]"
	}
	return strings.TrimSpace(value)
}

// changemakerRevisionPrompt appends the revision request to the thread's
// original writer prompt. The drafting step still receives the collected
// sources and citation rules from the pipeline snapshot.
func changemakerRevisionPrompt(basePrompt string, base int, draftSHA, notes, previousDraft string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimRight(basePrompt, "\n"))
	builder.WriteString("\n\nRevision request: revise draft version " + strconv.Itoa(base) + " (" + changemakerDraftArtifactName + " sha256 " + draftSHA + ") to address the reviewer notes. Keep every citation rule below; do not add claims the collected sources do not support.\n")
	builder.WriteString("<reviewer-notes>\n" + neutralizeChangemakerRevisionText(notes, changemakerNotesMaxBytes) + "\n</reviewer-notes>\n")
	builder.WriteString("<previous-draft>\n" + neutralizeChangemakerRevisionText(previousDraft, changemakerPreviousDraftMaxBytes) + "\n</previous-draft>")
	return builder.String()
}
