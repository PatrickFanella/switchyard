package controlplane

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDevlogAdapter stands in for the destination. posted holds what the
// destination would show; nothing leaves the process.
type fakeDevlogAdapter struct {
	mu           sync.Mutex
	posted       []DevlogDeliveryMessage
	failNext     int
	lostResponse int
	findErr      error
	deliverCalls int
	findCalls    int
}

func (f *fakeDevlogAdapter) Name() string        { return "fake-destination" }
func (f *fakeDevlogAdapter) Destination() string { return "fake/repo#7" }

func (f *fakeDevlogAdapter) FindDelivery(_ context.Context, key string) (DevlogDeliveryResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	if f.findErr != nil {
		return DevlogDeliveryResult{}, false, f.findErr
	}
	for index, message := range f.posted {
		if strings.Contains(message.Body, devlogDeliveryMarker(key)) {
			return DevlogDeliveryResult{ExternalID: strconv.Itoa(index + 1), ExternalURL: "https://fake.invalid/comment/" + strconv.Itoa(index+1)}, true, nil
		}
	}
	return DevlogDeliveryResult{}, false, nil
}

func (f *fakeDevlogAdapter) Deliver(_ context.Context, message DevlogDeliveryMessage) (DevlogDeliveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliverCalls++
	if f.failNext > 0 {
		f.failNext--
		return DevlogDeliveryResult{}, errors.New("destination unavailable: 503 Service Unavailable")
	}
	f.posted = append(f.posted, message)
	if f.lostResponse > 0 {
		f.lostResponse--
		return DevlogDeliveryResult{}, errors.New("read response: connection reset")
	}
	id := strconv.Itoa(len(f.posted))
	return DevlogDeliveryResult{ExternalID: id, ExternalURL: "https://fake.invalid/comment/" + id}, nil
}

func (f *fakeDevlogAdapter) postedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posted)
}

type devlogDeliveryFixture struct {
	t       *testing.T
	now     *time.Time
	store   *MemoryStore
	api     *API
	control http.Handler
	fake    *fakeDevlogAdapter
	run     Run
	report  Artifact
}

const devlogFixtureMarkdown = "# Switchyard Devlog\n\nShipped **delivery** review.\n"

func newDevlogDeliveryFixture(t *testing.T, succeed bool) *devlogDeliveryFixture {
	t.Helper()
	for _, key := range []string{"DEVLOG_DELIVERY_ADAPTER", "DEVLOG_DELIVERY_GITEA_ISSUE", "DEVLOG_DELIVERY_ALLOWED_REPOS"} {
		t.Setenv(key, "")
	}
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	fixture := &devlogDeliveryFixture{t: t, now: &now, fake: &fakeDevlogAdapter{}}
	fixture.store = NewStore(func() time.Time { return *fixture.now })
	if err := SeedDefaults(fixture.store); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := fixture.store.AddArtifact(CreateArtifactInput{RunID: run.ID, Name: devlogReportArtifactName, Kind: "markdown", URI: inlineArtifactURI("text/markdown;charset=utf-8", devlogFixtureMarkdown)})
	if err != nil {
		t.Fatal(err)
	}
	if succeed {
		if run, err = fixture.store.FinishRun(run.ID, RunSucceeded, "Generated Devlog markdown artifact"); err != nil {
			t.Fatal(err)
		}
	}
	fixture.run, fixture.report = run, report
	fixture.api = NewAPI(fixture.store, NewRunner(fixture.store))
	fixture.api.clock = func() time.Time { return *fixture.now }
	fixture.api.SetDevlogDelivery(NewDevlogDelivery(fixture.fake, ""))
	fixture.control = fixture.api.ControlPlaneHandler()
	return fixture
}

func (f *devlogDeliveryFixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, reader)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response := httptest.NewRecorder()
	f.control.ServeHTTP(response, request)
	return response
}

func (f *devlogDeliveryFixture) path(suffix string) string {
	return "/api/devlog/reports/" + f.run.ID + suffix
}

func (f *devlogDeliveryFixture) review() DevlogReportReview {
	f.t.Helper()
	response := f.do(http.MethodGet, f.path(""), "read-token", nil)
	if response.Code != http.StatusOK {
		f.t.Fatalf("review status = %d body=%s", response.Code, response.Body.String())
	}
	var review DevlogReportReview
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil {
		f.t.Fatal(err)
	}
	return review
}

func (f *devlogDeliveryFixture) decide(decision, sha string) (DevlogDeliveryApproval, *httptest.ResponseRecorder) {
	f.t.Helper()
	response := f.do(http.MethodPost, f.path("/approvals"), "admin-token", map[string]string{"artifact_sha256": sha, "destination": f.fake.Destination(), "decision": decision, "reason": "reviewed " + decision})
	var body struct {
		Approval DevlogDeliveryApproval `json:"approval"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Approval, response
}

func (f *devlogDeliveryFixture) approve() DevlogDeliveryApproval {
	f.t.Helper()
	approval, response := f.decide(DevlogApprovalApproved, f.report.SHA256)
	if response.Code != http.StatusCreated {
		f.t.Fatalf("approve status = %d body=%s", response.Code, response.Body.String())
	}
	return approval
}

func (f *devlogDeliveryFixture) deliver(approvalID, sha string) (DevlogDeliveryResponse, *httptest.ResponseRecorder) {
	f.t.Helper()
	response := f.do(http.MethodPost, f.path("/deliveries"), "admin-token", map[string]string{"approval_id": approvalID, "artifact_sha256": sha})
	var body DevlogDeliveryResponse
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body, response
}

func errorCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Error.Code
}

func TestDevlogReportReviewShowsPreviewSourceAndLocalExport(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	review := f.review()
	if review.State != "awaiting-approval" || review.Report == nil || review.Report.SHA256 != f.report.SHA256 || !strings.HasPrefix(review.Report.URI, "data:text/markdown") {
		t.Fatalf("review = %+v", review)
	}
	if review.Source.Mode != "run-history" || review.Source.Until != f.run.CreatedAt.UTC().Format(time.RFC3339) || review.Evidence != nil {
		t.Fatalf("source = %+v", review.Source)
	}
	if !review.Delivery.Enabled || review.Delivery.Destination != "fake/repo#7" || review.Delivery.ApprovalTTLSeconds != int(devlogApprovalTTL/time.Second) || review.IdempotencyKey != devlogDeliveryIdempotencyKey("fake-destination", "fake/repo#7", f.report.SHA256) {
		t.Fatalf("delivery = %+v key=%s", review.Delivery, review.IdempotencyKey)
	}
	if len(review.Exports) != 1 || review.Exports[0].Format != "markdown" || review.Exports[0].Path != f.path("/export/markdown") {
		t.Fatalf("exports = %+v", review.Exports)
	}

	exported := f.do(http.MethodGet, f.path("/export/markdown"), "read-token", nil)
	if exported.Code != http.StatusOK || exported.Body.String() != devlogFixtureMarkdown {
		t.Fatalf("markdown export status=%d body=%q", exported.Code, exported.Body.String())
	}
	sum := sha256.Sum256([]byte(devlogFixtureMarkdown))
	if exported.Header().Get("X-Switchyard-Artifact-SHA256") != hex.EncodeToString(sum[:]) || exported.Header().Get("Content-Type") != "text/markdown; charset=utf-8" ||
		!strings.Contains(exported.Header().Get("Content-Disposition"), "attachment;") || exported.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export headers = %v", exported.Header())
	}
	if missing := f.do(http.MethodGet, f.path("/export/json"), "read-token", nil); missing.Code != http.StatusNotFound || errorCode(t, missing) != "artifact-not-found" {
		t.Fatalf("json export without evidence = %d %s", missing.Code, missing.Body.String())
	}
	if unknown := f.do(http.MethodGet, f.path("/export/pdf"), "read-token", nil); unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown format = %d", unknown.Code)
	}

	// Repository-evidence mode: the JSON export is the exact evidence artifact
	// and the review shows its window before any approval.
	evidence := DevlogEvidenceReport{Version: 1, Provider: "gitea", Window: DevlogEvidenceWindow{Since: "2026-09-14T00:00:00Z", Until: "2026-09-21T00:00:00Z"}, Complete: false,
		Repositories: []DevlogEvidenceRepository{{Repository: "owner/alpha"}}, Items: []DevlogEvidenceItem{}, Missing: []string{"owner/alpha tags: failed"}}
	evidenceJSON := evidence.JSON()
	if _, err := f.store.AddArtifact(CreateArtifactInput{RunID: f.run.ID, Name: devlogEvidenceArtifactName, Kind: "json", URI: inlineArtifactURI("application/json;charset=utf-8", evidenceJSON)}); err != nil {
		t.Fatal(err)
	}
	review = f.review()
	if review.Source.Mode != "repository-evidence" || review.Source.Since != "2026-09-14T00:00:00Z" || review.Source.Until != "2026-09-21T00:00:00Z" ||
		review.Source.Complete == nil || *review.Source.Complete || len(review.Source.Missing) != 1 || len(review.Source.Repositories) != 1 || len(review.Exports) != 2 {
		t.Fatalf("evidence review = %+v exports=%+v", review.Source, review.Exports)
	}
	jsonExport := f.do(http.MethodGet, f.path("/export/json"), "read-token", nil)
	if jsonExport.Code != http.StatusOK || jsonExport.Body.String() != evidenceJSON || jsonExport.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("json export status=%d body=%q", jsonExport.Code, jsonExport.Body.String())
	}
	if f.fake.postedCount() != 0 || f.fake.findCalls != 0 {
		t.Fatalf("review and export must not contact the destination: posted=%d finds=%d", f.fake.postedCount(), f.fake.findCalls)
	}
}

func TestDevlogReportReviewRejectsNonDevlogRuns(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	other, err := f.store.CreateRun(CreateRunInput{WorkflowID: "open-pilot"})
	if err != nil {
		t.Fatal(err)
	}
	if response := f.do(http.MethodGet, "/api/devlog/reports/"+other.ID, "read-token", nil); response.Code != http.StatusNotFound || errorCode(t, response) != "not-devlog" {
		t.Fatalf("non-devlog review = %d %s", response.Code, response.Body.String())
	}
	if response := f.do(http.MethodGet, "/api/devlog/reports/missing-run", "read-token", nil); response.Code != http.StatusNotFound || errorCode(t, response) != "run-not-found" {
		t.Fatalf("missing run review = %d %s", response.Code, response.Body.String())
	}
}

func TestDevlogDeliveryDeliversOnceWithFakeAdapter(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	approval := f.approve()
	if approval.Decision != DevlogApprovalApproved || approval.ArtifactSHA256 != f.report.SHA256 || approval.Actor != "token:admin" || !approval.ExpiresAt.Equal(f.now.Add(devlogApprovalTTL)) {
		t.Fatalf("approval = %+v", approval)
	}
	if state := f.review().State; state != "approved" {
		t.Fatalf("state after approval = %s", state)
	}
	body, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusCreated || body.Outcome != "delivered" || body.Receipt.Status != DevlogDeliveryDelivered || body.Receipt.Attempts != 1 || body.Receipt.Reconciled || body.Receipt.ExternalID != "1" || body.Receipt.DeliveredAt == nil {
		t.Fatalf("delivery = %d %+v", response.Code, body)
	}
	if f.fake.postedCount() != 1 {
		t.Fatalf("posted = %d", f.fake.postedCount())
	}
	message := f.fake.posted[0]
	if !strings.HasPrefix(message.Body, devlogFixtureMarkdown) || !strings.Contains(message.Body, devlogDeliveryMarker(body.Receipt.IdempotencyKey)) || !strings.Contains(message.Body, f.report.SHA256) || message.ArtifactSHA256 != f.report.SHA256 {
		t.Fatalf("delivered body = %q", message.Body)
	}

	again, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusOK || again.Outcome != "already-delivered" || again.Receipt.ID != body.Receipt.ID || f.fake.postedCount() != 1 || f.fake.deliverCalls != 1 {
		t.Fatalf("repeat delivery = %d %+v posted=%d", response.Code, again, f.fake.postedCount())
	}
	review := f.review()
	if review.State != "delivered" || review.CurrentReceipt == nil || len(review.Receipts) != 1 || len(review.Approvals) != 1 {
		t.Fatalf("review after delivery = %+v", review)
	}
}

func TestDevlogDeliveryRequiresApproval(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	if _, response := f.deliver("", f.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-required" {
		t.Fatalf("missing approval = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.deliver("devlog-approval-unknown", f.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-required" {
		t.Fatalf("unknown approval = %d %s", response.Code, response.Body.String())
	}
	if f.fake.deliverCalls != 0 || f.fake.findCalls != 0 {
		t.Fatal("delivery without approval reached the adapter")
	}
}

func TestDevlogDeliveryRejectedApprovalBlocksDelivery(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	rejected, response := f.decide(DevlogApprovalRejected, f.report.SHA256)
	if response.Code != http.StatusCreated || rejected.Decision != DevlogApprovalRejected {
		t.Fatalf("reject = %d %s", response.Code, response.Body.String())
	}
	if state := f.review().State; state != "rejected" {
		t.Fatalf("state = %s", state)
	}
	if _, response := f.deliver(rejected.ID, f.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-rejected" {
		t.Fatalf("deliver with rejection = %d %s", response.Code, response.Body.String())
	}
	// An approval superseded by a later rejection cannot be used either.
	*f.now = f.now.Add(time.Minute)
	approval := f.approve()
	*f.now = f.now.Add(time.Minute)
	if _, response := f.decide(DevlogApprovalRejected, f.report.SHA256); response.Code != http.StatusCreated {
		t.Fatalf("second reject = %d", response.Code)
	}
	if _, response := f.deliver(approval.ID, f.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-rejected" {
		t.Fatalf("deliver superseded approval = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.decide("maybe", f.report.SHA256); response.Code != http.StatusBadRequest {
		t.Fatalf("invalid decision = %d", response.Code)
	}
	if f.fake.deliverCalls != 0 {
		t.Fatal("rejected report reached the adapter")
	}
}

func TestDevlogDeliveryStaleOrExpiredApprovalIsRefused(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	oldSHA := f.report.SHA256
	if _, response := f.decide(DevlogApprovalApproved, strings.Repeat("0", 64)); response.Code != http.StatusConflict || errorCode(t, response) != "stale-report" {
		t.Fatalf("approve wrong digest = %d %s", response.Code, response.Body.String())
	}
	wrongDestination := f.do(http.MethodPost, f.path("/approvals"), "admin-token", map[string]string{"artifact_sha256": oldSHA, "destination": "other/repo#1", "decision": "approved"})
	if wrongDestination.Code != http.StatusConflict || errorCode(t, wrongDestination) != "destination-mismatch" {
		t.Fatalf("approve wrong destination = %d %s", wrongDestination.Code, wrongDestination.Body.String())
	}
	approval := f.approve()

	// A new report version replaces the one that was approved.
	*f.now = f.now.Add(time.Minute)
	newer, err := f.store.AddArtifact(CreateArtifactInput{RunID: f.run.ID, Name: devlogReportArtifactName, Kind: "markdown", URI: inlineArtifactURI("text/markdown;charset=utf-8", devlogFixtureMarkdown+"\nEdited.\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, response := f.deliver(approval.ID, newer.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "stale-approval" {
		t.Fatalf("stale approval = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.deliver(approval.ID, oldSHA); response.Code != http.StatusConflict || errorCode(t, response) != "stale-report" {
		t.Fatalf("stale report digest = %d %s", response.Code, response.Body.String())
	}
	if review := f.review(); review.Report.SHA256 != newer.SHA256 || review.State != "awaiting-approval" {
		t.Fatalf("review after new version = %s %s", review.Report.SHA256, review.State)
	}

	f.report = newer
	fresh := f.approve()
	*f.now = f.now.Add(devlogApprovalTTL + time.Second)
	if state := f.review().State; state != "approval-expired" {
		t.Fatalf("state = %s", state)
	}
	if _, response := f.deliver(fresh.ID, newer.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-expired" {
		t.Fatalf("expired approval = %d %s", response.Code, response.Body.String())
	}
	if f.fake.deliverCalls != 0 {
		t.Fatal("stale or expired approval reached the adapter")
	}
}

func TestDevlogDeliveryFailureThenRetryDoesNotDuplicate(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	approval := f.approve()
	f.fake.failNext = 1
	failed, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusBadGateway || failed.Outcome != "failed" || failed.Receipt.Status != DevlogDeliveryFailed || failed.Receipt.Attempts != 1 || !strings.Contains(failed.Receipt.LastError, "503") {
		t.Fatalf("failed delivery = %d %+v", response.Code, failed)
	}
	if state := f.review().State; state != "delivery-failed" {
		t.Fatalf("state after failure = %s", state)
	}
	retried, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusCreated || retried.Outcome != "delivered" || retried.Receipt.ID != failed.Receipt.ID || retried.Receipt.Attempts != 2 || retried.Receipt.LastError != "" || f.fake.postedCount() != 1 {
		t.Fatalf("retry = %d %+v posted=%d", response.Code, retried, f.fake.postedCount())
	}
}

func TestDevlogDeliveryRetryAfterLostResponseReconciles(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	approval := f.approve()
	f.fake.lostResponse = 1
	failed, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusBadGateway || failed.Receipt.Status != DevlogDeliveryFailed || f.fake.postedCount() != 1 {
		t.Fatalf("lost response = %d %+v posted=%d", response.Code, failed, f.fake.postedCount())
	}
	reconciled, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusOK || reconciled.Outcome != "reconciled" || !reconciled.Receipt.Reconciled || reconciled.Receipt.Status != DevlogDeliveryDelivered || reconciled.Receipt.ExternalID != "1" {
		t.Fatalf("reconcile = %d %+v", response.Code, reconciled)
	}
	if f.fake.postedCount() != 1 || f.fake.deliverCalls != 1 {
		t.Fatalf("retry posted a duplicate: posted=%d calls=%d", f.fake.postedCount(), f.fake.deliverCalls)
	}

	// A destination that cannot be searched is never posted to.
	g := newDevlogDeliveryFixture(t, true)
	g.fake.findErr = errors.New("list comments: 500")
	gApproval := g.approve()
	body, response := g.deliver(gApproval.ID, g.report.SHA256)
	if response.Code != http.StatusBadGateway || body.Receipt.Status != DevlogDeliveryFailed || !strings.Contains(body.Receipt.LastError, "reconcile") || g.fake.deliverCalls != 0 {
		t.Fatalf("unsearchable destination = %d %+v calls=%d", response.Code, body, g.fake.deliverCalls)
	}
}

func TestDevlogDeliveryConcurrentClaimIsRefusedUntilStale(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	approval := f.approve()
	key := devlogDeliveryIdempotencyKey(f.fake.Name(), f.fake.Destination(), f.report.SHA256)
	if _, claimed, err := f.store.BeginDevlogDelivery(BeginDevlogDeliveryInput{IdempotencyKey: key, RunID: f.run.ID, ArtifactID: f.report.ID, ArtifactSHA256: f.report.SHA256, Adapter: f.fake.Name(), Destination: f.fake.Destination(), ApprovalID: approval.ID, Actor: "other", StaleAfter: devlogDeliveryClaimTTL}); err != nil || !claimed {
		t.Fatalf("claim = %v %v", claimed, err)
	}
	if _, response := f.deliver(approval.ID, f.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "delivery-in-progress" {
		t.Fatalf("concurrent delivery = %d %s", response.Code, response.Body.String())
	}
	*f.now = f.now.Add(devlogDeliveryClaimTTL + time.Second)
	body, response := f.deliver(approval.ID, f.report.SHA256)
	if response.Code != http.StatusCreated || body.Receipt.Attempts != 2 || f.fake.postedCount() != 1 {
		t.Fatalf("stale reclaim = %d %+v", response.Code, body)
	}
}

func TestDevlogDeliveryAuthorizationAndListenerBoundaries(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	if response := f.do(http.MethodGet, f.path(""), "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated review = %d", response.Code)
	}
	if response := f.do(http.MethodGet, f.path("/export/markdown"), "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated export = %d", response.Code)
	}
	if response := f.do(http.MethodPost, f.path("/approvals"), "read-token", map[string]string{"artifact_sha256": f.report.SHA256, "destination": f.fake.Destination(), "decision": "approved"}); response.Code != http.StatusForbidden {
		t.Fatalf("read-token approval = %d", response.Code)
	}
	approval := f.approve()
	if response := f.do(http.MethodPost, f.path("/deliveries"), "read-token", map[string]string{"approval_id": approval.ID, "artifact_sha256": f.report.SHA256}); response.Code != http.StatusForbidden {
		t.Fatalf("read-token delivery = %d", response.Code)
	}
	if response := f.do(http.MethodPost, f.path("/deliveries"), "", map[string]string{"approval_id": approval.ID, "artifact_sha256": f.report.SHA256}); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated delivery = %d", response.Code)
	}

	model := f.api.ModelIngressHandler()
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, f.path("")},
		{http.MethodGet, f.path("/export/markdown")},
		{http.MethodPost, f.path("/approvals")},
		{http.MethodPost, f.path("/deliveries")},
	} {
		body := strings.NewReader(`{"approval_id":"` + approval.ID + `","artifact_sha256":"` + f.report.SHA256 + `"}`)
		request := httptest.NewRequest(probe.method, probe.path, body)
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		model.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "route not available on this listener") {
			t.Errorf("model listener %s %s = %d %s", probe.method, probe.path, response.Code, response.Body.String())
		}
	}
	if f.fake.deliverCalls != 0 || f.fake.findCalls != 0 {
		t.Fatal("authorization failures or model listener reached the adapter")
	}
}

func TestDevlogDeliveryDisabledByDefaultAndNotReadyRuns(t *testing.T) {
	f := newDevlogDeliveryFixture(t, true)
	api := NewAPI(f.store, NewRunner(f.store))
	control := api.ControlPlaneHandler()
	request := httptest.NewRequest(http.MethodGet, f.path(""), nil)
	request.Header.Set("Authorization", "Bearer read-token")
	response := httptest.NewRecorder()
	control.ServeHTTP(response, request)
	var review DevlogReportReview
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil || response.Code != http.StatusOK {
		t.Fatalf("review = %d %v", response.Code, err)
	}
	if review.State != "delivery-disabled" || review.Delivery.Enabled || review.Delivery.Adapter != DevlogDeliveryAdapterDisabled || !strings.Contains(review.Delivery.Reason, "DEVLOG_DELIVERY_ADAPTER") || len(review.Exports) != 1 {
		t.Fatalf("default delivery = %+v", review)
	}
	request = httptest.NewRequest(http.MethodPost, f.path("/approvals"), strings.NewReader(`{"artifact_sha256":"`+f.report.SHA256+`","destination":"x/y#1","decision":"approved"}`))
	request.Header.Set("Authorization", "Bearer admin-token")
	response = httptest.NewRecorder()
	control.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || errorCode(t, response) != "delivery-disabled" {
		t.Fatalf("approval while disabled = %d %s", response.Code, response.Body.String())
	}

	pending := newDevlogDeliveryFixture(t, false)
	if state := pending.review().State; state != "not-ready" {
		t.Fatalf("unfinished run state = %s", state)
	}
	if _, response := pending.decide(DevlogApprovalApproved, pending.report.SHA256); response.Code != http.StatusConflict || errorCode(t, response) != "report-not-ready" {
		t.Fatalf("approve unfinished run = %d %s", response.Code, response.Body.String())
	}
}

func TestDevlogDeliveryConfigurationRequiresExplicitAllowlistedDestination(t *testing.T) {
	client := func() GiteaIssueCommentClient { return &fakeGiteaCommentClient{} }
	cases := []struct {
		name, adapter, issue, allowed string
		gitea                         bool
		reason                        string
	}{
		{"default disabled", "", "", "", true, "disabled"},
		{"explicitly disabled", "disabled", "o/r#1", "o/r", true, "disabled"},
		{"unsupported adapter", "slack", "o/r#1", "o/r", true, "not supported"},
		{"missing destination", "gitea-issue-comment", "", "o/r", true, "is not set"},
		{"malformed destination", "gitea-issue-comment", "o/r", "o/r", true, "owner/repo#number"},
		{"zero issue", "gitea-issue-comment", "o/r#0", "o/r", true, "owner/repo#number"},
		{"not allowlisted", "gitea-issue-comment", "o/r#1", "o/other", true, "DEVLOG_DELIVERY_ALLOWED_REPOS"},
		{"no gitea credentials", "gitea-issue-comment", "o/r#1", "o/r", false, "GITEA_TOKEN"},
	}
	for _, test := range cases {
		adapter, reason := devlogDeliveryAdapterFromConfig(test.adapter, test.issue, test.allowed, test.gitea, client)
		if adapter != nil || !strings.Contains(reason, test.reason) {
			t.Errorf("%s: adapter=%v reason=%q", test.name, adapter, reason)
		}
	}
	adapter, reason := devlogDeliveryAdapterFromConfig("gitea-issue-comment", "owner/devlog#12", "owner/other, owner/devlog", true, client)
	if adapter == nil || reason != "" || adapter.Name() != DevlogDeliveryAdapterGiteaIssueComment || adapter.Destination() != "owner/devlog#12" {
		t.Fatalf("valid config adapter=%v reason=%q", adapter, reason)
	}
}

func TestDevlogDeliveryMigrationDeclaresDurableReceipts(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/035_devlog_delivery.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"create table if not exists devlog_delivery_approvals", "decision in ('approved','rejected')", "create table if not exists devlog_delivery_receipts", "constraint devlog_delivery_receipts_idempotency_key unique (idempotency_key)", "status in ('sending','delivered','failed')", "((status = 'delivered') = (delivered_at is not null))", "devlog_delivery_receipts_run_idx", "devlog_delivery_approvals_run_idx"} {
		if !strings.Contains(string(data), field) {
			t.Errorf("devlog delivery migration missing %q", field)
		}
	}
	if strings.Contains(string(data), "references runs") {
		t.Error("delivery evidence must survive run retention; do not reference runs")
	}
}

type fakeGiteaCommentClient struct {
	comments []GiteaIssueComment
}

func (c *fakeGiteaCommentClient) CurrentUserLogin(context.Context) (string, error) { return "bot", nil }
func (c *fakeGiteaCommentClient) ListIssueComments(_ context.Context, _ string, _, page, limit int) ([]GiteaIssueComment, error) {
	start := (page - 1) * limit
	if start >= len(c.comments) {
		return nil, nil
	}
	end := start + limit
	if end > len(c.comments) {
		end = len(c.comments)
	}
	return c.comments[start:end], nil
}
func (c *fakeGiteaCommentClient) CreateIssueComment(_ context.Context, _ string, _ int, body string) (GiteaIssueComment, error) {
	comment := GiteaIssueComment{ID: int64(len(c.comments) + 1), Body: body}
	return comment, nil
}

// TestGiteaIssueCommentDeliveryAgainstLocalFakeGitea drives the real HTTP
// client against an in-process fake Gitea. No external host is contacted.
func TestGiteaIssueCommentDeliveryAgainstLocalFakeGitea(t *testing.T) {
	var mu sync.Mutex
	comments := []map[string]any{}
	posts := 0
	key := devlogDeliveryIdempotencyKey(DevlogDeliveryAdapterGiteaIssueComment, "owner/devlog#12", strings.Repeat("a", 64))
	// A marker written by another account must not suppress delivery.
	comments = append(comments, map[string]any{"id": 1, "body": "spoof " + devlogDeliveryMarker(key), "html_url": "https://git.invalid/c/1", "user": map[string]string{"login": "someone"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "token test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "devlog-bot"})
		case r.URL.Path == "/api/v1/repos/owner/devlog/issues/12/comments" && r.Method == http.MethodGet:
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			start := (page - 1) * limit
			out := []map[string]any{}
			for index := start; index < len(comments) && index < start+limit; index++ {
				out = append(out, comments[index])
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.URL.Path == "/api/v1/repos/owner/devlog/issues/12/comments" && r.Method == http.MethodPost:
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			posts++
			id := len(comments) + 1
			comment := map[string]any{"id": id, "body": body.Body, "html_url": fmt.Sprintf("https://git.invalid/c/%d", id), "user": map[string]string{"login": "devlog-bot"}}
			comments = append(comments, comment)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(comment)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := &EnvGiteaClient{baseURL: server.URL, token: "test-token", client: server.Client()}
	adapter, reason := devlogDeliveryAdapterFromConfig("gitea-issue-comment", "owner/devlog#12", "owner/devlog", true, func() GiteaIssueCommentClient { return client })
	if adapter == nil {
		t.Fatal(reason)
	}
	ctx := context.Background()
	if _, found, err := adapter.FindDelivery(ctx, key); err != nil || found {
		t.Fatalf("spoofed marker found=%v err=%v", found, err)
	}
	result, err := adapter.Deliver(ctx, DevlogDeliveryMessage{IdempotencyKey: key, Body: "report\n" + devlogDeliveryMarker(key)})
	if err != nil || result.ExternalID != "2" || result.ExternalURL != "https://git.invalid/c/2" {
		t.Fatalf("deliver = %+v err=%v", result, err)
	}
	existing, found, err := adapter.FindDelivery(ctx, key)
	if err != nil || !found || existing.ExternalID != "2" || posts != 1 {
		t.Fatalf("reconcile = %+v found=%v err=%v posts=%d", existing, found, err, posts)
	}

	// Too many comments to confirm absence: refuse rather than guess.
	crowded := &fakeGiteaCommentClient{}
	for index := 0; index < devlogGiteaCommentPageSize*devlogGiteaCommentMaxPages; index++ {
		crowded.comments = append(crowded.comments, GiteaIssueComment{ID: int64(index + 1), Body: "noise"})
	}
	capped := &giteaIssueCommentDelivery{client: crowded, repo: "owner/devlog", issue: 12}
	if _, found, err := capped.FindDelivery(ctx, key); err == nil || found {
		t.Fatalf("capped scan found=%v err=%v", found, err)
	}
}
