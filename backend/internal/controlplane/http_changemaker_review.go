package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Changemaker draft review, revision, and publication routes. They are
// registered on the control listener only; the model listener allowlist does
// not include them. GET needs the read role; decisions, revisions, and
// publications are POSTs and therefore need the admin role.

// ChangemakerDraftVersionView is one version with the digests a reviewer
// decides against and its review state.
type ChangemakerDraftVersionView struct {
	ChangemakerDraftVersion
	RunStatus          RunStatus                  `json:"run_status"`
	RunSummary         string                     `json:"run_summary,omitempty"`
	TriggerID          string                     `json:"trigger_id,omitempty"`
	Draft              *Artifact                  `json:"draft"`
	DraftSHA256        string                     `json:"draft_sha256,omitempty"`
	EvidenceSHA256     string                     `json:"evidence_sha256,omitempty"`
	EvidenceArtifactID string                     `json:"evidence_artifact_id,omitempty"`
	GroundingStatus    string                     `json:"grounding_status,omitempty"`
	DraftGenerated     bool                       `json:"draft_generated"`
	CitedSources       string                     `json:"cited_sources,omitempty"`
	UnknownCitations   string                     `json:"unknown_citations,omitempty"`
	Ready              bool                       `json:"ready"`
	NotReadyReason     string                     `json:"not_ready_reason,omitempty"`
	State              string                     `json:"state"`
	IdempotencyKey     string                     `json:"idempotency_key,omitempty"`
	CurrentDecision    *ChangemakerReviewDecision `json:"current_decision,omitempty"`
	CurrentReceipt     *DeliveryReceipt           `json:"current_receipt,omitempty"`

	run           Run
	previousDraft string
	collectInputs map[string]string
	receipts      []DeliveryReceipt
}

// ChangemakerDraftReview is a thread's revision history and review state,
// with the version selected by the request path.
type ChangemakerDraftReview struct {
	ThreadID        string `json:"thread_id"`
	SelectedVersion int    `json:"selected_version"`
	LatestVersion   int    `json:"latest_version"`
	// CurrentVersion is the version open for review and publication: the
	// highest version whose run has not failed or been canceled.
	CurrentVersion   int                           `json:"current_version"`
	PublishedVersion int                           `json:"published_version,omitempty"`
	State            string                        `json:"state"`
	Versions         []ChangemakerDraftVersionView `json:"versions"`
	Decisions        []ChangemakerReviewDecision   `json:"decisions"`
	Receipts         []DeliveryReceipt             `json:"receipts"`
	Publication      ChangemakerPublicationStatus  `json:"publication"`
	// Access is advisory for the dashboard; the auth middleware enforces it.
	Access ControlAccess `json:"access"`
}

// ChangemakerPublicationResponse reports the outcome of one publication
// request.
type ChangemakerPublicationResponse struct {
	Outcome string          `json:"outcome"`
	Receipt DeliveryReceipt `json:"receipt"`
}

// changemakerRevisionMu serializes revision requests in this process. The
// API value is copied per request, so the lock cannot live on it.
var changemakerRevisionMu sync.Mutex

type changemakerThread struct {
	review   ChangemakerDraftReview
	selected int // index into review.Versions
	root     ChangemakerDraftVersion
}

func (t changemakerThread) view() *ChangemakerDraftVersionView {
	return &t.review.Versions[t.selected]
}

// SetChangemakerPublication replaces the configured publisher.
func (a *API) SetChangemakerPublication(publication *ChangemakerPublication) {
	a.changemakerPublication = publication
}

func (a *API) changemakerPublicationState() *ChangemakerPublication {
	if a.changemakerPublication == nil {
		return NewChangemakerPublication(nil, "")
	}
	return a.changemakerPublication
}

func (a *API) changemakerNow() time.Time {
	if a.clock != nil {
		return a.clock().UTC()
	}
	return time.Now().UTC()
}

func (a *API) isChangemakerRun(run Run) bool {
	if run.WorkflowID == changemakerDailyDigestID {
		return true
	}
	workflow, ok := findWorkflow(a.store, run.WorkflowID)
	return ok && workflow.Kind == WorkflowChangemaker
}

// changemakerStepByID returns the latest succeeded record of a pipeline step,
// matched through the run's pipeline snapshot when present.
func changemakerStepByID(detail RunDetail, stepID, fallbackName string) (RunStep, bool) {
	name, position := fallbackName, 0
	if detail.Run.PipelineSnapshot != nil {
		for _, step := range detail.Run.PipelineSnapshot.Steps {
			if step.ID == stepID {
				name, position = step.Name, step.Position
				break
			}
		}
	}
	var found RunStep
	ok := false
	for _, step := range detail.Steps {
		if step.Status != StepSucceeded || step.Name != name || (position > 0 && step.Position != position) {
			continue
		}
		if !ok || !step.CreatedAt.Before(found.CreatedAt) {
			found, ok = step, true
		}
	}
	return found, ok
}

func latestArtifactNamed(artifacts []Artifact, name string) *Artifact {
	var latest *Artifact
	for index := range artifacts {
		artifact := artifacts[index]
		if artifact.Name == name && (latest == nil || !artifact.CreatedAt.Before(latest.CreatedAt)) {
			latest = &artifact
		}
	}
	return latest
}

func (a *API) buildChangemakerVersionView(ctx context.Context, version ChangemakerDraftVersion, details map[string]*RunDetail) ChangemakerDraftVersionView {
	view := ChangemakerDraftVersionView{ChangemakerDraftVersion: version}
	load := func(runID string) *RunDetail {
		if detail, ok := details[runID]; ok {
			return detail
		}
		detail, err := a.store.RunDetailContext(ctx, runID)
		if err != nil {
			details[runID] = nil
			return nil
		}
		details[runID] = &detail
		return &detail
	}
	detail := load(version.RunID)
	if detail == nil {
		view.NotReadyReason = "the run for this version no longer exists"
		return view
	}
	view.run = detail.Run
	view.RunStatus, view.RunSummary, view.TriggerID = detail.Run.Status, detail.Run.Summary, detail.Run.TriggerID
	view.Draft = latestArtifactNamed(detail.Artifacts, changemakerDraftArtifactName)
	if view.Draft != nil {
		view.DraftSHA256 = view.Draft.SHA256
	}
	if attach, ok := changemakerStepByID(*detail, changemakerAttachStepID, "changemaker.attach-draft"); ok {
		view.EvidenceSHA256 = strings.TrimSpace(attach.Outputs["evidence_sha256"])
		view.GroundingStatus = attach.Outputs["grounding_status"]
		view.DraftGenerated = attach.Outputs["draft_generated"] == "true"
	}
	if draft, ok := changemakerStepByID(*detail, changemakerDraftStepID, "changemaker.generate"); ok {
		view.CitedSources, view.UnknownCitations, view.previousDraft = draft.Outputs["cited_sources"], draft.Outputs["unknown_citations"], draft.Outputs["content"]
	}
	if sources := load(version.SourcesRunID); sources != nil {
		if evidence := latestArtifactNamed(sources.Artifacts, changemakerEvidenceArtifactName); evidence != nil {
			view.EvidenceArtifactID = evidence.ID
			if view.EvidenceSHA256 != "" && evidence.SHA256 != view.EvidenceSHA256 {
				view.NotReadyReason = "the attached evidence digest does not match " + changemakerEvidenceArtifactName
				return view
			}
		}
		if collect, ok := changemakerStepByID(*sources, changemakerCollectStepID, "changemaker.collect-sources"); ok {
			view.collectInputs = collect.Inputs
		}
	}
	switch {
	case detail.Run.Status != RunSucceeded:
		view.NotReadyReason = "the run has not succeeded"
	case view.Draft == nil || !sha256HexPattern.MatchString(view.DraftSHA256):
		view.NotReadyReason = "the run has no " + changemakerDraftArtifactName + " draft"
	case !sha256HexPattern.MatchString(view.EvidenceSHA256):
		view.NotReadyReason = "the draft has no source evidence digest (not a grounded Changemaker run)"
	default:
		view.Ready = true
	}
	return view
}

// latestChangemakerDecision returns the newest decision made against the
// version's current draft and evidence digests. decisions are newest first.
func latestChangemakerDecision(decisions []ChangemakerReviewDecision, view ChangemakerDraftVersionView) *ChangemakerReviewDecision {
	for index := range decisions {
		decision := decisions[index]
		if decision.RunID == view.RunID && decision.Version == view.Version && decision.DraftSHA256 == view.DraftSHA256 && decision.EvidenceSHA256 == view.EvidenceSHA256 {
			return &decision
		}
	}
	return nil
}

func changemakerVersionState(view ChangemakerDraftVersionView, current, published int, publication ChangemakerPublicationStatus, now time.Time) string {
	if !view.Ready {
		return "not-ready"
	}
	if view.CurrentReceipt != nil && view.CurrentReceipt.Status == DeliveryDelivered {
		return "published"
	}
	if published > 0 || view.Version < current {
		return "superseded"
	}
	if view.CurrentReceipt != nil && view.CurrentReceipt.Status == DeliverySending {
		return "publishing"
	}
	decision := view.CurrentDecision
	switch {
	case decision == nil && !view.DraftGenerated:
		return "no-draft"
	case decision == nil:
		return "awaiting-review"
	case decision.Decision == ChangemakerDecisionRejected:
		return "rejected"
	case decision.Decision == ChangemakerDecisionRevisionRequested:
		return "revision-requested"
	case !now.Before(decision.ExpiresAt):
		return "approval-expired"
	case publication.Enabled && (decision.Adapter != publication.Adapter || decision.Destination != publication.Destination):
		return "destination-changed"
	case view.CurrentReceipt != nil && view.CurrentReceipt.Status == DeliveryFailed:
		return "publication-failed"
	default:
		return "approved"
	}
}

// loadChangemakerThread resolves the run in the request path to its thread
// and builds every version's review view.
func (a *API) loadChangemakerThread(w http.ResponseWriter, r *http.Request) (changemakerThread, bool) {
	ctx := r.Context()
	runID := r.PathValue("id")
	detail, err := a.store.RunDetailContext(ctx, runID)
	if errors.Is(err, ErrNotFound) {
		writeCodedError(w, http.StatusNotFound, "run-not-found", "run not found")
		return changemakerThread{}, false
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load run: "+RedactForLog(err.Error()))
		return changemakerThread{}, false
	}
	if !a.isChangemakerRun(detail.Run) {
		writeCodedError(w, http.StatusNotFound, "not-changemaker", "run is not a Changemaker draft")
		return changemakerThread{}, false
	}
	threadID := runID
	if version, err := a.store.ChangemakerDraftVersionByRun(runID); err == nil {
		threadID = version.ThreadID
	} else if !errors.Is(err, ErrNotFound) {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load draft version: "+RedactForLog(err.Error()))
		return changemakerThread{}, false
	} else if detail.Run.Inputs[changemakerRevisionThreadInput] != "" {
		writeCodedError(w, http.StatusConflict, "revision-not-recorded", "this run was created for a revision request that was not recorded; review the thread it names instead")
		return changemakerThread{}, false
	}
	versions, err := a.store.ListChangemakerDraftVersions(threadID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "list draft versions: "+RedactForLog(err.Error()))
		return changemakerThread{}, false
	}
	root := ChangemakerDraftVersion{ThreadID: threadID, Version: 1, RunID: threadID, SourcesRunID: threadID, SourceMode: ChangemakerSourceModeOriginal, CreatedAt: detail.Run.CreatedAt.UTC()}
	if len(versions) == 0 {
		versions = []ChangemakerDraftVersion{root}
	} else {
		root = versions[0]
	}
	decisions, err := a.store.ListChangemakerReviewDecisions(threadID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "list decisions: "+RedactForLog(err.Error()))
		return changemakerThread{}, false
	}
	publication := a.changemakerPublicationState().status()
	thread := changemakerThread{root: root, review: ChangemakerDraftReview{ThreadID: threadID, Decisions: decisions, Receipts: []DeliveryReceipt{}, Publication: publication, Access: controlAccessFromRequest(r)}}
	details := map[string]*RunDetail{runID: &detail}
	for _, version := range versions {
		view := a.buildChangemakerVersionView(ctx, version, details)
		receipts, err := a.store.ListChangemakerPublicationReceipts(version.RunID)
		if err != nil {
			writeCodedError(w, http.StatusInternalServerError, "store-error", "list receipts: "+RedactForLog(err.Error()))
			return changemakerThread{}, false
		}
		view.receipts = receipts
		thread.review.Receipts = append(thread.review.Receipts, receipts...)
		if view.Ready && publication.Enabled {
			view.IdempotencyKey = changemakerPublicationIdempotencyKey(publication.Adapter, publication.Destination, view.DraftSHA256)
		}
		for index := range receipts {
			receipt := receipts[index]
			if receipt.ArtifactSHA256 != view.DraftSHA256 {
				continue
			}
			if receipt.IdempotencyKey == view.IdempotencyKey || (receipt.Status == DeliveryDelivered && (view.CurrentReceipt == nil || view.CurrentReceipt.Status != DeliveryDelivered)) {
				view.CurrentReceipt = &receipt
			}
			if receipt.Status == DeliveryDelivered && thread.review.PublishedVersion == 0 {
				thread.review.PublishedVersion = version.Version
			}
		}
		view.CurrentDecision = latestChangemakerDecision(decisions, view)
		if version.RunID == runID {
			thread.selected = len(thread.review.Versions)
		}
		thread.review.Versions = append(thread.review.Versions, view)
	}
	thread.review.LatestVersion = thread.review.Versions[len(thread.review.Versions)-1].Version
	thread.review.CurrentVersion = thread.review.Versions[0].Version
	for _, view := range thread.review.Versions {
		if view.run.ID != "" && view.run.Status != RunFailed && view.run.Status != RunCanceled {
			thread.review.CurrentVersion = view.Version
		}
	}
	now := a.changemakerNow()
	for index := range thread.review.Versions {
		view := &thread.review.Versions[index]
		published := thread.review.PublishedVersion
		if published == view.Version {
			published = 0
		}
		view.State = changemakerVersionState(*view, thread.review.CurrentVersion, published, publication, now)
	}
	thread.review.SelectedVersion = thread.view().Version
	thread.review.State = thread.view().State
	return thread, true
}

func (a *API) changemakerDraftReview(w http.ResponseWriter, r *http.Request) {
	thread, ok := a.loadChangemakerThread(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, thread.review)
}

// checkChangemakerDecisionTarget enforces the preconditions shared by review
// decisions and revision requests: the request names the version and exact
// digests the reviewer saw, and that version is still open for review.
func checkChangemakerDecisionTarget(w http.ResponseWriter, thread changemakerThread, version int, draftSHA, evidenceSHA string) bool {
	view := thread.view()
	switch {
	case version != view.Version:
		writeCodedError(w, http.StatusConflict, "version-mismatch", "version does not match the draft version of this run")
	case !view.Ready:
		writeCodedError(w, http.StatusConflict, "draft-not-ready", "draft is not ready for review: "+view.NotReadyReason)
	case thread.review.PublishedVersion > 0:
		writeCodedError(w, http.StatusConflict, "already-published", "version "+strconv.Itoa(thread.review.PublishedVersion)+" of this thread was published; the thread is closed to further review")
	case view.Version < thread.review.CurrentVersion:
		writeCodedError(w, http.StatusConflict, "superseded", "a newer version of this draft exists; review version "+strconv.Itoa(thread.review.CurrentVersion))
	case draftSHA != view.DraftSHA256:
		writeCodedError(w, http.StatusConflict, "stale-draft", "draft_sha256 does not match the current draft; reload and review it again")
	case evidenceSHA != view.EvidenceSHA256:
		writeCodedError(w, http.StatusConflict, "stale-evidence", "evidence_sha256 does not match the draft's source evidence; reload and review it again")
	case view.CurrentReceipt != nil && view.CurrentReceipt.Status == DeliverySending:
		writeCodedError(w, http.StatusConflict, "publication-in-progress", "a publication attempt for this draft is in progress")
	default:
		return true
	}
	return false
}

func (a *API) decideChangemakerDraft(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version        int    `json:"version"`
		DraftSHA256    string `json:"draft_sha256"`
		EvidenceSHA256 string `json:"evidence_sha256"`
		Decision       string `json:"decision"`
		Notes          string `json:"notes"`
	}
	if !decodeDevlogBody(w, r, &input) {
		return
	}
	if input.Decision != ChangemakerDecisionApproved && input.Decision != ChangemakerDecisionRejected {
		writeCodedError(w, http.StatusBadRequest, "invalid-decision", "decision must be approved or rejected; use the revisions route to request a revision")
		return
	}
	if !sha256HexPattern.MatchString(input.DraftSHA256) || !sha256HexPattern.MatchString(input.EvidenceSHA256) {
		writeCodedError(w, http.StatusBadRequest, "invalid-digest", "draft_sha256 and evidence_sha256 must be 64-character lowercase SHA-256 digests")
		return
	}
	if len(input.Notes) > changemakerNotesMaxBytes {
		writeCodedError(w, http.StatusBadRequest, "invalid-notes", "notes must be at most "+strconv.Itoa(changemakerNotesMaxBytes)+" bytes")
		return
	}
	thread, ok := a.loadChangemakerThread(w, r)
	if !ok || !checkChangemakerDecisionTarget(w, thread, input.Version, input.DraftSHA256, input.EvidenceSHA256) {
		return
	}
	view := thread.view()
	if input.Decision == ChangemakerDecisionApproved && !view.DraftGenerated {
		writeCodedError(w, http.StatusConflict, "draft-not-generated", "no draft was generated (no sources were collected); request a revision with re-collected sources or reject it")
		return
	}
	publication := thread.review.Publication
	now := a.changemakerNow()
	decision, err := a.store.RecordChangemakerReviewDecision(ChangemakerReviewDecision{
		ID: newID("changemaker-decision"), ThreadID: thread.review.ThreadID, Version: view.Version, RunID: view.RunID, ArtifactID: view.Draft.ID,
		DraftSHA256: view.DraftSHA256, EvidenceSHA256: view.EvidenceSHA256, Decision: input.Decision, Notes: strings.TrimSpace(RedactForLog(input.Notes)),
		Actor: boundedAuditValue(requestActor(r)), Adapter: publication.Adapter, Destination: publication.Destination, CreatedAt: now, ExpiresAt: now.Add(changemakerApprovalTTL),
	})
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "record decision: "+RedactForLog(err.Error()))
		return
	}
	slog.Info("changemaker review decision recorded", "thread_id", decision.ThreadID, "version", decision.Version, "run_id", decision.RunID, "decision_id", decision.ID, "decision", decision.Decision, "draft_sha256", decision.DraftSHA256, "evidence_sha256", decision.EvidenceSHA256, "destination", decision.Destination, "actor", decision.Actor)
	writeJSON(w, http.StatusCreated, map[string]any{"decision": decision})
}

func changemakerRevisionRunInputs(parent ChangemakerDraftVersionView, notes string) map[string]string {
	inputs := map[string]string{}
	for key, value := range parent.run.Inputs {
		if strings.HasPrefix(key, "retry_from_") || strings.HasPrefix(key, "resume.") || strings.HasPrefix(key, "changemaker_revision_") {
			continue
		}
		inputs[key] = value
	}
	base := parent.run.Inputs[changemakerBasePromptInput]
	if base == "" {
		base = parent.run.Inputs["prompt"]
	}
	inputs[changemakerBasePromptInput] = base
	inputs["prompt"] = changemakerRevisionPrompt(base, parent.Version, parent.DraftSHA256, notes, parent.previousDraft)
	return inputs
}

const changemakerBasePromptInput = "changemaker_base_prompt"

func (a *API) requestChangemakerRevision(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version        int    `json:"version"`
		DraftSHA256    string `json:"draft_sha256"`
		EvidenceSHA256 string `json:"evidence_sha256"`
		Notes          string `json:"notes"`
		Sources        string `json:"sources"`
	}
	if !decodeDevlogBody(w, r, &input) {
		return
	}
	if !sha256HexPattern.MatchString(input.DraftSHA256) || !sha256HexPattern.MatchString(input.EvidenceSHA256) {
		writeCodedError(w, http.StatusBadRequest, "invalid-digest", "draft_sha256 and evidence_sha256 must be 64-character lowercase SHA-256 digests")
		return
	}
	notes := strings.TrimSpace(input.Notes)
	if notes == "" || len(notes) > changemakerNotesMaxBytes {
		writeCodedError(w, http.StatusBadRequest, "invalid-notes", "notes are required and must be at most "+strconv.Itoa(changemakerNotesMaxBytes)+" bytes")
		return
	}
	switch input.Sources {
	case "":
		input.Sources = ChangemakerSourceModeSame
	case ChangemakerSourceModeSame:
	case "recollect", ChangemakerSourceModeRecollected:
		input.Sources = ChangemakerSourceModeRecollected
	default:
		writeCodedError(w, http.StatusBadRequest, "invalid-sources", "sources must be same or recollect")
		return
	}
	// One revision request at a time per process; the store's version
	// precondition rejects a concurrent request from another process.
	changemakerRevisionMu.Lock()
	defer changemakerRevisionMu.Unlock()
	thread, ok := a.loadChangemakerThread(w, r)
	if !ok || !checkChangemakerDecisionTarget(w, thread, input.Version, input.DraftSHA256, input.EvidenceSHA256) {
		return
	}
	parent := *thread.view()
	if thread.review.LatestVersion >= changemakerMaxDraftVersions {
		writeCodedError(w, http.StatusConflict, "revision-limit", "this thread already has "+strconv.Itoa(changemakerMaxDraftVersions)+" versions")
		return
	}
	if parent.run.PipelineSnapshot == nil {
		writeCodedError(w, http.StatusConflict, "pipeline-unavailable", "the draft's run has no pipeline snapshot; start a new run instead")
		return
	}
	inputs := changemakerRevisionRunInputs(parent, RedactForLog(notes))
	inputs[changemakerRevisionThreadInput] = thread.review.ThreadID
	inputs[changemakerRevisionVersionInput] = strconv.Itoa(thread.review.LatestVersion + 1)
	inputs[changemakerRevisionSourcesInput] = input.Sources
	sourcesRunID := ""
	switch input.Sources {
	case ChangemakerSourceModeSame:
		sources, err := a.store.RunDetailContext(r.Context(), parent.SourcesRunID)
		if err != nil {
			writeCodedError(w, http.StatusConflict, "sources-unavailable", "the run that collected this draft's sources no longer exists; request a revision with re-collected sources")
			return
		}
		collect, ok := changemakerStepByID(sources, changemakerCollectStepID, "changemaker.collect-sources")
		if !ok || collect.Outputs["evidence_sha256"] != parent.EvidenceSHA256 || sources.Run.PipelineVersion != parent.run.PipelineVersion {
			writeCodedError(w, http.StatusConflict, "sources-unavailable", "the collected sources cannot be restored exactly; request a revision with re-collected sources")
			return
		}
		sourcesRunID = parent.SourcesRunID
		inputs["retry_from_run"] = parent.SourcesRunID
		inputs["retry_from_step"] = changemakerDraftStepID
	case ChangemakerSourceModeRecollected:
		start, errStart := time.Parse(time.RFC3339, parent.collectInputs["window_start"])
		end, errEnd := time.Parse(time.RFC3339, parent.collectInputs["window_end"])
		if errStart != nil || errEnd != nil || !end.After(start) {
			writeCodedError(w, http.StatusConflict, "sources-unavailable", "the original collection window is not recorded; start a new run instead")
			return
		}
		inputs["source_until"] = end.UTC().Format(time.RFC3339)
		inputs["source_window"] = end.Sub(start).String()
	}
	run, err := a.runner.EnqueueContext(r.Context(), CreateRunInput{WorkflowID: parent.run.WorkflowID, Inputs: inputs, PipelineSnapshot: parent.run.PipelineSnapshot})
	if err != nil {
		writeControlError(w, err)
		return
	}
	if sourcesRunID == "" {
		sourcesRunID = run.ID
	}
	now := a.changemakerNow()
	actor := boundedAuditValue(requestActor(r))
	root := thread.root
	if root.ID == "" {
		root.ID = newID("changemaker-version")
	}
	publication := thread.review.Publication
	version, decision, err := a.store.RecordChangemakerRevision(ChangemakerRevisionInput{
		Root:                  root,
		BaseVersion:           parent.Version,
		ExpectedLatestVersion: thread.review.LatestVersion,
		Version: ChangemakerDraftVersion{
			ID: newID("changemaker-version"), ThreadID: thread.review.ThreadID, Version: thread.review.LatestVersion + 1, RunID: run.ID, ParentVersion: parent.Version,
			SourcesRunID: sourcesRunID, SourceMode: input.Sources, RevisionNotes: RedactForLog(notes), RequestedBy: actor, CreatedAt: now,
		},
		Decision: ChangemakerReviewDecision{
			ID: newID("changemaker-decision"), ThreadID: thread.review.ThreadID, Version: parent.Version, RunID: parent.RunID, ArtifactID: parent.Draft.ID,
			DraftSHA256: parent.DraftSHA256, EvidenceSHA256: parent.EvidenceSHA256, Decision: ChangemakerDecisionRevisionRequested, Notes: RedactForLog(notes),
			Actor: actor, Adapter: publication.Adapter, Destination: publication.Destination, RevisionRunID: run.ID, CreatedAt: now, ExpiresAt: now.Add(changemakerApprovalTTL),
		},
	})
	if err != nil {
		_, _ = a.store.FinishRun(run.ID, RunCanceled, "Canceled: the Changemaker revision request was not recorded")
		if errors.Is(err, ErrConflict) {
			writeCodedError(w, http.StatusConflict, "superseded", "another revision of this draft was recorded first; reload the thread")
			return
		}
		writeCodedError(w, http.StatusInternalServerError, "store-error", "record revision: "+RedactForLog(err.Error()))
		return
	}
	_, _ = a.store.AddEvent(run.ID, "changemaker.revision.requested", "Revision "+strconv.Itoa(version.Version)+" of thread "+version.ThreadID+" requested by "+actor+" ("+version.SourceMode+" sources)")
	slog.Info("changemaker revision requested", "thread_id", version.ThreadID, "version", version.Version, "run_id", run.ID, "parent_version", version.ParentVersion, "source_mode", version.SourceMode, "draft_sha256", decision.DraftSHA256, "evidence_sha256", decision.EvidenceSHA256, "actor", actor)
	status := http.StatusCreated
	if a.asyncDispatch {
		w.Header().Set("Location", "/api/runs/"+run.ID)
		status = http.StatusAccepted
	} else if started, err := a.store.StartRun(run.ID); err == nil {
		if finished, err := a.runner.ExecuteRun(r.Context(), started); err == nil || finished.ID != "" {
			run = finished
		}
	}
	writeJSON(w, status, map[string]any{"version": version, "decision": decision, "run": run})
}

func (a *API) publishChangemakerDraft(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ApprovalID  string `json:"approval_id"`
		DraftSHA256 string `json:"draft_sha256"`
	}
	if !decodeDevlogBody(w, r, &input) {
		return
	}
	thread, ok := a.loadChangemakerThread(w, r)
	if !ok {
		return
	}
	service := a.changemakerPublicationState()
	publication := thread.review.Publication
	view := thread.view()
	switch {
	case !publication.Enabled:
		writeCodedError(w, http.StatusConflict, "publication-disabled", publication.Reason)
		return
	case !view.Ready:
		writeCodedError(w, http.StatusConflict, "draft-not-ready", "draft is not ready: "+view.NotReadyReason)
		return
	case input.DraftSHA256 != view.DraftSHA256:
		writeCodedError(w, http.StatusConflict, "stale-draft", "draft_sha256 does not match the current draft; reload and review it again")
		return
	}
	key := changemakerPublicationIdempotencyKey(publication.Adapter, publication.Destination, view.DraftSHA256)
	if existing, err := a.store.ChangemakerPublicationReceipt(key); err == nil && existing.Status == DeliveryDelivered {
		writeJSON(w, http.StatusOK, ChangemakerPublicationResponse{Outcome: "already-published", Receipt: existing})
		return
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load receipt: "+RedactForLog(err.Error()))
		return
	}
	switch {
	case thread.review.PublishedVersion > 0:
		writeCodedError(w, http.StatusConflict, "already-published", "version "+strconv.Itoa(thread.review.PublishedVersion)+" of this thread was already published")
		return
	case view.Version < thread.review.CurrentVersion:
		writeCodedError(w, http.StatusConflict, "superseded", "only the current version ("+strconv.Itoa(thread.review.CurrentVersion)+") can be published")
		return
	case !view.DraftGenerated:
		writeCodedError(w, http.StatusConflict, "draft-not-generated", "no draft was generated for this version")
		return
	case strings.TrimSpace(input.ApprovalID) == "":
		writeCodedError(w, http.StatusConflict, "approval-required", "an admin approval of this exact draft is required before publication")
		return
	}
	approval, err := a.store.ChangemakerReviewDecision(input.ApprovalID)
	if errors.Is(err, ErrNotFound) || (err == nil && (approval.RunID != view.RunID || approval.ThreadID != thread.review.ThreadID)) {
		writeCodedError(w, http.StatusConflict, "approval-required", "approval not found for this draft version")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load approval: "+RedactForLog(err.Error()))
		return
	}
	now := a.changemakerNow()
	switch {
	case approval.Decision != ChangemakerDecisionApproved:
		writeCodedError(w, http.StatusConflict, "approval-rejected", "the referenced decision did not approve publication")
		return
	case approval.DraftSHA256 != view.DraftSHA256 || approval.ArtifactID != view.Draft.ID || approval.EvidenceSHA256 != view.EvidenceSHA256 || approval.Version != view.Version:
		writeCodedError(w, http.StatusConflict, "stale-approval", "the approval is bound to a different draft or evidence digest; review and approve the current draft")
		return
	case approval.Adapter != publication.Adapter || approval.Destination != publication.Destination:
		writeCodedError(w, http.StatusConflict, "destination-changed", "the approval names a different publication target than the one configured now")
		return
	case !now.Before(approval.ExpiresAt):
		writeCodedError(w, http.StatusConflict, "approval-expired", "the approval has expired; approve the draft again")
		return
	case view.CurrentDecision == nil || view.CurrentDecision.ID != approval.ID:
		writeCodedError(w, http.StatusConflict, "approval-superseded", "a later decision replaced this approval")
		return
	}
	_, raw, err := decodeInlineArtifactURI(view.Draft.URI)
	if err != nil {
		writeCodedError(w, http.StatusUnprocessableEntity, "artifact-unreadable", "draft artifact is not a readable inline artifact")
		return
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != view.DraftSHA256 {
		writeCodedError(w, http.StatusConflict, "digest-mismatch", "draft bytes do not match the recorded SHA-256")
		return
	}
	body := changemakerPublicationBody(raw, view.ChangemakerDraftVersion, view.DraftSHA256, view.EvidenceSHA256, key)
	if len(body) > changemakerPublicationMaxBytes {
		writeCodedError(w, http.StatusConflict, "draft-too-large", "draft exceeds the "+strconv.Itoa(changemakerPublicationMaxBytes)+"-byte destination limit")
		return
	}

	// Record outcomes even if the client disconnects after the send.
	store := a.store.WithContext(context.WithoutCancel(r.Context()))
	actor := boundedAuditValue(requestActor(r))
	receipt, claimed, err := store.BeginChangemakerPublication(BeginDeliveryInput{
		IdempotencyKey: key, RunID: view.RunID, ArtifactID: view.Draft.ID, ArtifactSHA256: view.DraftSHA256,
		Adapter: publication.Adapter, Destination: publication.Destination, ApprovalID: approval.ID, Actor: actor, StaleAfter: changemakerPublicationClaimTTL,
	})
	if errors.Is(err, ErrConflict) {
		writeCodedError(w, http.StatusConflict, "publication-in-progress", "another publication attempt for this draft is in progress; retry after it finishes")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "claim publication: "+RedactForLog(err.Error()))
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, ChangemakerPublicationResponse{Outcome: "already-published", Receipt: receipt})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), changemakerPublicationTimeout)
	defer cancel()
	attempt := attemptClaimedDelivery(ctx, service.adapter, DeliveryMessage{IdempotencyKey: key, RunID: view.RunID, ArtifactSHA256: view.DraftSHA256, Body: body})
	finishInput := attempt.Finish
	finishInput.IdempotencyKey, finishInput.ClaimToken = key, receipt.claimToken
	finished, err := store.FinishChangemakerPublication(finishInput)
	if err != nil {
		writeCodedError(w, http.StatusConflict, "publication-claim-lost", "publication outcome could not be recorded: "+RedactForLog(err.Error()))
		return
	}
	slog.Info("changemaker publication attempt finished", "thread_id", thread.review.ThreadID, "version", view.Version, "run_id", finished.RunID, "receipt_id", finished.ID, "status", finished.Status, "reconciled", finished.Reconciled, "attempts", finished.Attempts, "destination", finished.Destination, "draft_sha256", finished.ArtifactSHA256, "actor", actor)
	outcome, status := attempt.Outcome, http.StatusCreated
	switch attempt.Outcome {
	case "failed":
		status = http.StatusBadGateway
	case "reconciled":
		status = http.StatusOK
	default:
		outcome = "published"
	}
	writeJSON(w, status, ChangemakerPublicationResponse{Outcome: outcome, Receipt: finished})
}
