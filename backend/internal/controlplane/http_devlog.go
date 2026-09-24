package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Devlog report review, export, approval, and delivery. These routes are
// registered on the control listener only; the model listener allowlist does
// not include them. Reads need the read role; approval and delivery are POSTs
// and therefore need the admin role.

// DevlogReportExport links one exact-bytes download.
type DevlogReportExport struct {
	Format   string `json:"format"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	ByteSize int    `json:"byte_size"`
}

// DevlogReportReview is everything a reviewer needs before approving delivery.
type DevlogReportReview struct {
	Run             Run                      `json:"run"`
	State           string                   `json:"state"`
	Report          *Artifact                `json:"report"`
	Evidence        *Artifact                `json:"evidence"`
	Source          DevlogReportSource       `json:"source"`
	Exports         []DevlogReportExport     `json:"exports"`
	Delivery        DevlogDeliveryStatus     `json:"delivery"`
	IdempotencyKey  string                   `json:"idempotency_key,omitempty"`
	CurrentApproval *DevlogDeliveryApproval  `json:"current_approval,omitempty"`
	CurrentReceipt  *DevlogDeliveryReceipt   `json:"current_receipt,omitempty"`
	Approvals       []DevlogDeliveryApproval `json:"approvals"`
	Receipts        []DevlogDeliveryReceipt  `json:"receipts"`
}

// DevlogDeliveryResponse reports the outcome of one delivery request.
type DevlogDeliveryResponse struct {
	Outcome string                `json:"outcome"`
	Receipt DevlogDeliveryReceipt `json:"receipt"`
}

type devlogReport struct {
	run      Run
	report   *Artifact
	evidence *Artifact
}

func writeCodedError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]map[string]string{"error": {"code": code, "message": message}})
}

// SetDevlogDelivery replaces the configured Devlog delivery adapter.
func (a *API) SetDevlogDelivery(delivery *DevlogDelivery) {
	a.devlogDelivery = delivery
}

func (a *API) devlogDeliveryState() *DevlogDelivery {
	if a.devlogDelivery == nil {
		return NewDevlogDelivery(nil, "")
	}
	return a.devlogDelivery
}

func (a *API) loadDevlogReport(w http.ResponseWriter, r *http.Request) (devlogReport, bool) {
	detail, err := a.store.RunDetailContext(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		writeCodedError(w, http.StatusNotFound, "run-not-found", "run not found")
		return devlogReport{}, false
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load run: "+RedactForLog(err.Error()))
		return devlogReport{}, false
	}
	if detail.Run.WorkflowID != string(WorkflowDevlog) {
		writeCodedError(w, http.StatusNotFound, "not-devlog", "run is not a Devlog report")
		return devlogReport{}, false
	}
	report := devlogReport{run: detail.Run}
	for index := range detail.Artifacts {
		artifact := detail.Artifacts[index]
		switch artifact.Name {
		case devlogReportArtifactName:
			if report.report == nil || !artifact.CreatedAt.Before(report.report.CreatedAt) {
				report.report = &artifact
			}
		case devlogEvidenceArtifactName:
			if report.evidence == nil || !artifact.CreatedAt.Before(report.evidence.CreatedAt) {
				report.evidence = &artifact
			}
		}
	}
	return report, true
}

func (report devlogReport) ready() bool {
	return report.run.Status == RunSucceeded && report.report != nil && devlogSHA256Pattern.MatchString(report.report.SHA256)
}

func devlogExportPath(runID, format string) string {
	return "/api/devlog/reports/" + runID + "/export/" + format
}

// latestDevlogDecision returns the newest decision for the report digest and
// destination. approvals must be sorted newest first.
func latestDevlogDecision(approvals []DevlogDeliveryApproval, sha string, delivery DevlogDeliveryStatus) *DevlogDeliveryApproval {
	for index := range approvals {
		approval := approvals[index]
		if approval.ArtifactSHA256 == sha && approval.Adapter == delivery.Adapter && approval.Destination == delivery.Destination {
			return &approval
		}
	}
	return nil
}

func (a *API) devlogReportReview(w http.ResponseWriter, r *http.Request) {
	report, ok := a.loadDevlogReport(w, r)
	if !ok {
		return
	}
	delivery := a.devlogDeliveryState().status()
	review := DevlogReportReview{Run: report.run, Report: report.report, Evidence: report.evidence, Source: devlogReportSource(report.run, report.evidence), Delivery: delivery, Exports: []DevlogReportExport{}}
	if report.report != nil {
		review.Exports = append(review.Exports, DevlogReportExport{Format: "markdown", Name: devlogReportArtifactName, Path: devlogExportPath(report.run.ID, "markdown"), SHA256: report.report.SHA256, ByteSize: report.report.ByteSize})
	}
	if report.evidence != nil {
		review.Exports = append(review.Exports, DevlogReportExport{Format: "json", Name: devlogEvidenceArtifactName, Path: devlogExportPath(report.run.ID, "json"), SHA256: report.evidence.SHA256, ByteSize: report.evidence.ByteSize})
	}
	approvals, err := a.store.ListDevlogDeliveryApprovals(report.run.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "list approvals: "+RedactForLog(err.Error()))
		return
	}
	receipts, err := a.store.ListDevlogDeliveryReceipts(report.run.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "list receipts: "+RedactForLog(err.Error()))
		return
	}
	review.Approvals, review.Receipts = approvals, receipts
	switch {
	case !report.ready():
		review.State = "not-ready"
	case !delivery.Enabled:
		review.State = "delivery-disabled"
	default:
		review.IdempotencyKey = devlogDeliveryIdempotencyKey(delivery.Adapter, delivery.Destination, report.report.SHA256)
		if receipt, err := a.store.DevlogDeliveryReceipt(review.IdempotencyKey); err == nil {
			review.CurrentReceipt = &receipt
		} else if !errors.Is(err, ErrNotFound) {
			writeCodedError(w, http.StatusInternalServerError, "store-error", "load receipt: "+RedactForLog(err.Error()))
			return
		}
		review.CurrentApproval = latestDevlogDecision(approvals, report.report.SHA256, delivery)
		review.State = devlogReviewState(review.CurrentApproval, review.CurrentReceipt, a.devlogNow())
	}
	writeJSON(w, http.StatusOK, review)
}

func devlogReviewState(approval *DevlogDeliveryApproval, receipt *DevlogDeliveryReceipt, now time.Time) string {
	if receipt != nil {
		switch receipt.Status {
		case DevlogDeliveryDelivered:
			return "delivered"
		case DevlogDeliverySending:
			return "delivering"
		}
	}
	switch {
	case approval == nil:
		if receipt != nil {
			return "delivery-failed"
		}
		return "awaiting-approval"
	case approval.Decision == DevlogApprovalRejected:
		return "rejected"
	case !now.Before(approval.ExpiresAt):
		return "approval-expired"
	case receipt != nil:
		return "delivery-failed"
	default:
		return "approved"
	}
}

func (a *API) devlogNow() time.Time {
	if a.clock != nil {
		return a.clock().UTC()
	}
	delivery := a.devlogDeliveryState()
	if delivery.now != nil {
		return delivery.now().UTC()
	}
	return time.Now().UTC()
}

func (a *API) devlogReportExport(w http.ResponseWriter, r *http.Request) {
	report, ok := a.loadDevlogReport(w, r)
	if !ok {
		return
	}
	var artifact *Artifact
	contentType, extension := "", ""
	switch r.PathValue("format") {
	case "markdown":
		artifact, contentType, extension = report.report, "text/markdown; charset=utf-8", ".md"
	case "json":
		artifact, contentType, extension = report.evidence, "application/json; charset=utf-8", ".json"
	default:
		writeCodedError(w, http.StatusNotFound, "unknown-format", "export format must be markdown or json")
		return
	}
	if artifact == nil {
		writeCodedError(w, http.StatusNotFound, "artifact-not-found", "this Devlog run has no artifact for the requested format")
		return
	}
	_, raw, err := decodeInlineArtifactURI(artifact.URI)
	if err != nil {
		writeCodedError(w, http.StatusUnprocessableEntity, "artifact-unreadable", "artifact is not a readable inline artifact")
		return
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if artifact.SHA256 != "" && digest != artifact.SHA256 {
		writeCodedError(w, http.StatusConflict, "digest-mismatch", "artifact bytes do not match the recorded SHA-256")
		return
	}
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Content-Disposition", `attachment; filename="devlog-`+safeDevlogFileToken(report.run.ID)+extension+`"`)
	header.Set("Content-Length", strconv.Itoa(len(raw)))
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Switchyard-Artifact-Id", artifact.ID)
	header.Set("X-Switchyard-Artifact-SHA256", digest)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(raw)
	}
}

func safeDevlogFileToken(value string) string {
	out := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
	if len(out) > 96 {
		out = out[:96]
	}
	return out
}

func decodeDevlogBody(w http.ResponseWriter, r *http.Request, out any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			writeCodedError(w, http.StatusBadRequest, "invalid-request", "request body is required")
			return false
		}
		writeJSONDecodeError(w, err)
		return false
	}
	return true
}

func (a *API) approveDevlogReport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ArtifactSHA256 string `json:"artifact_sha256"`
		Destination    string `json:"destination"`
		Decision       string `json:"decision"`
		Reason         string `json:"reason"`
	}
	if !decodeDevlogBody(w, r, &input) {
		return
	}
	if input.Decision != DevlogApprovalApproved && input.Decision != DevlogApprovalRejected {
		writeCodedError(w, http.StatusBadRequest, "invalid-decision", "decision must be approved or rejected")
		return
	}
	if !devlogSHA256Pattern.MatchString(input.ArtifactSHA256) {
		writeCodedError(w, http.StatusBadRequest, "invalid-digest", "artifact_sha256 must be the 64-character lowercase SHA-256 of the report")
		return
	}
	if len(input.Reason) > 1000 {
		writeCodedError(w, http.StatusBadRequest, "invalid-reason", "reason must be at most 1000 bytes")
		return
	}
	report, ok := a.loadDevlogReport(w, r)
	if !ok {
		return
	}
	delivery := a.devlogDeliveryState().status()
	if !delivery.Enabled {
		writeCodedError(w, http.StatusConflict, "delivery-disabled", delivery.Reason)
		return
	}
	if !report.ready() {
		writeCodedError(w, http.StatusConflict, "report-not-ready", "only a succeeded Devlog run with a devlog.md artifact can be approved")
		return
	}
	if input.Destination != delivery.Destination {
		writeCodedError(w, http.StatusConflict, "destination-mismatch", "destination must equal the configured destination "+delivery.Destination)
		return
	}
	if input.ArtifactSHA256 != report.report.SHA256 {
		writeCodedError(w, http.StatusConflict, "stale-report", "artifact_sha256 does not match the current report; reload and review it again")
		return
	}
	now := a.devlogNow()
	approval, err := a.store.RecordDevlogDeliveryApproval(DevlogDeliveryApproval{
		ID: newID("devlog-approval"), RunID: report.run.ID, ArtifactID: report.report.ID, ArtifactSHA256: report.report.SHA256,
		Adapter: delivery.Adapter, Destination: delivery.Destination, Decision: input.Decision,
		Actor: boundedAuditValue(requestActor(r)), Reason: strings.TrimSpace(RedactForLog(input.Reason)), CreatedAt: now, ExpiresAt: now.Add(devlogApprovalTTL),
	})
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "record approval: "+RedactForLog(err.Error()))
		return
	}
	slog.Info("devlog delivery decision recorded", "run_id", approval.RunID, "approval_id", approval.ID, "decision", approval.Decision, "artifact_sha256", approval.ArtifactSHA256, "destination", approval.Destination, "actor", approval.Actor)
	writeJSON(w, http.StatusCreated, map[string]any{"approval": approval})
}

func (a *API) deliverDevlogReport(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ApprovalID     string `json:"approval_id"`
		ArtifactSHA256 string `json:"artifact_sha256"`
	}
	if !decodeDevlogBody(w, r, &input) {
		return
	}
	report, ok := a.loadDevlogReport(w, r)
	if !ok {
		return
	}
	service := a.devlogDeliveryState()
	delivery := service.status()
	if !delivery.Enabled {
		writeCodedError(w, http.StatusConflict, "delivery-disabled", delivery.Reason)
		return
	}
	if !report.ready() {
		writeCodedError(w, http.StatusConflict, "report-not-ready", "only a succeeded Devlog run with a devlog.md artifact can be delivered")
		return
	}
	if input.ArtifactSHA256 != report.report.SHA256 {
		writeCodedError(w, http.StatusConflict, "stale-report", "artifact_sha256 does not match the current report; reload and review it again")
		return
	}
	key := devlogDeliveryIdempotencyKey(delivery.Adapter, delivery.Destination, report.report.SHA256)
	if existing, err := a.store.DevlogDeliveryReceipt(key); err == nil && existing.Status == DevlogDeliveryDelivered {
		writeJSON(w, http.StatusOK, DevlogDeliveryResponse{Outcome: "already-delivered", Receipt: existing})
		return
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load receipt: "+RedactForLog(err.Error()))
		return
	}
	if strings.TrimSpace(input.ApprovalID) == "" {
		writeCodedError(w, http.StatusConflict, "approval-required", "an admin approval of this exact report is required before delivery")
		return
	}
	approval, err := a.store.DevlogDeliveryApproval(input.ApprovalID)
	if errors.Is(err, ErrNotFound) || (err == nil && approval.RunID != report.run.ID) {
		writeCodedError(w, http.StatusConflict, "approval-required", "approval not found for this Devlog report")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "load approval: "+RedactForLog(err.Error()))
		return
	}
	now := a.devlogNow()
	switch {
	case approval.Decision != DevlogApprovalApproved:
		writeCodedError(w, http.StatusConflict, "approval-rejected", "the referenced decision rejected delivery")
		return
	case approval.ArtifactSHA256 != report.report.SHA256 || approval.ArtifactID != report.report.ID:
		writeCodedError(w, http.StatusConflict, "stale-approval", "the approval is bound to a different report version; review and approve the current report")
		return
	case approval.Adapter != delivery.Adapter || approval.Destination != delivery.Destination:
		writeCodedError(w, http.StatusConflict, "destination-changed", "the approval names a different destination than the one configured now")
		return
	case !now.Before(approval.ExpiresAt):
		writeCodedError(w, http.StatusConflict, "approval-expired", "the approval has expired; approve the report again")
		return
	}
	approvals, err := a.store.ListDevlogDeliveryApprovals(report.run.ID)
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "list approvals: "+RedactForLog(err.Error()))
		return
	}
	for _, later := range approvals {
		if later.ID == approval.ID {
			break
		}
		if later.Decision == DevlogApprovalRejected && later.ArtifactSHA256 == approval.ArtifactSHA256 && later.Destination == approval.Destination && later.Adapter == approval.Adapter {
			writeCodedError(w, http.StatusConflict, "approval-rejected", "a later decision rejected delivery of this report")
			return
		}
	}
	_, raw, err := decodeInlineArtifactURI(report.report.URI)
	if err != nil {
		writeCodedError(w, http.StatusUnprocessableEntity, "artifact-unreadable", "report artifact is not a readable inline artifact")
		return
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != report.report.SHA256 {
		writeCodedError(w, http.StatusConflict, "digest-mismatch", "report bytes do not match the recorded SHA-256")
		return
	}
	body := devlogDeliveryBody(raw, report.run, report.report.SHA256, devlogReportSource(report.run, report.evidence), key)
	if len(body) > devlogDeliveryMaxBodyBytes {
		writeCodedError(w, http.StatusConflict, "report-too-large", "report exceeds the "+strconv.Itoa(devlogDeliveryMaxBodyBytes)+"-byte destination limit; export it instead")
		return
	}

	// Record outcomes even if the client disconnects after the send.
	store := a.store.WithContext(context.WithoutCancel(r.Context()))
	actor := boundedAuditValue(requestActor(r))
	receipt, claimed, err := store.BeginDevlogDelivery(BeginDevlogDeliveryInput{
		IdempotencyKey: key, RunID: report.run.ID, ArtifactID: report.report.ID, ArtifactSHA256: report.report.SHA256,
		Adapter: delivery.Adapter, Destination: delivery.Destination, ApprovalID: approval.ID, Actor: actor, StaleAfter: devlogDeliveryClaimTTL,
	})
	if errors.Is(err, ErrConflict) {
		writeCodedError(w, http.StatusConflict, "delivery-in-progress", "another delivery attempt for this report is in progress; retry after it finishes")
		return
	}
	if err != nil {
		writeCodedError(w, http.StatusInternalServerError, "store-error", "claim delivery: "+RedactForLog(err.Error()))
		return
	}
	if !claimed {
		writeJSON(w, http.StatusOK, DevlogDeliveryResponse{Outcome: "already-delivered", Receipt: receipt})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), devlogDeliveryTimeout)
	defer cancel()
	finish := func(input FinishDevlogDeliveryInput) (DevlogDeliveryReceipt, bool) {
		input.IdempotencyKey, input.ClaimToken = key, receipt.claimToken
		finished, err := store.FinishDevlogDelivery(input)
		if err != nil {
			writeCodedError(w, http.StatusConflict, "delivery-claim-lost", "delivery outcome could not be recorded: "+RedactForLog(err.Error()))
			return DevlogDeliveryReceipt{}, false
		}
		slog.Info("devlog delivery attempt finished", "run_id", finished.RunID, "receipt_id", finished.ID, "status", finished.Status, "reconciled", finished.Reconciled, "attempts", finished.Attempts, "destination", finished.Destination, "artifact_sha256", finished.ArtifactSHA256, "actor", actor)
		return finished, true
	}
	existing, found, err := service.adapter.FindDelivery(ctx, key)
	if err != nil {
		if finished, ok := finish(FinishDevlogDeliveryInput{Error: "reconcile with destination: " + err.Error()}); ok {
			writeJSON(w, http.StatusBadGateway, DevlogDeliveryResponse{Outcome: "failed", Receipt: finished})
		}
		return
	}
	if found {
		if finished, ok := finish(FinishDevlogDeliveryInput{Delivered: true, Reconciled: true, ExternalID: existing.ExternalID, ExternalURL: existing.ExternalURL}); ok {
			writeJSON(w, http.StatusOK, DevlogDeliveryResponse{Outcome: "reconciled", Receipt: finished})
		}
		return
	}
	result, err := service.adapter.Deliver(ctx, DevlogDeliveryMessage{IdempotencyKey: key, RunID: report.run.ID, ArtifactSHA256: report.report.SHA256, Body: body})
	if err != nil {
		if finished, ok := finish(FinishDevlogDeliveryInput{Error: "deliver: " + err.Error()}); ok {
			writeJSON(w, http.StatusBadGateway, DevlogDeliveryResponse{Outcome: "failed", Receipt: finished})
		}
		return
	}
	if finished, ok := finish(FinishDevlogDeliveryInput{Delivered: true, ExternalID: result.ExternalID, ExternalURL: result.ExternalURL}); ok {
		writeJSON(w, http.StatusCreated, DevlogDeliveryResponse{Outcome: "delivered", Receipt: finished})
	}
}
