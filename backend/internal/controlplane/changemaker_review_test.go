package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeChangemakerPublisher stands in for the publication destination. posted
// holds what the destination would show; nothing leaves the process.
type fakeChangemakerPublisher struct {
	mu           sync.Mutex
	destination  string
	posted       []DeliveryMessage
	failNext     int
	lostResponse int
	findErr      error
	deliverCalls int
	findCalls    int
}

func (f *fakeChangemakerPublisher) Name() string { return "fake-publisher" }
func (f *fakeChangemakerPublisher) Destination() string {
	if f.destination == "" {
		return "fake/changemaker#9"
	}
	return f.destination
}

func (f *fakeChangemakerPublisher) FindDelivery(_ context.Context, key string) (DeliveryResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.findCalls++
	if f.findErr != nil {
		return DeliveryResult{}, false, f.findErr
	}
	for index, message := range f.posted {
		if strings.Contains(message.Body, changemakerPublicationMarker(key)) {
			return DeliveryResult{ExternalID: strconv.Itoa(index + 1), ExternalURL: "https://fake.invalid/post/" + strconv.Itoa(index+1)}, true, nil
		}
	}
	return DeliveryResult{}, false, nil
}

func (f *fakeChangemakerPublisher) Deliver(_ context.Context, message DeliveryMessage) (DeliveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliverCalls++
	if f.failNext > 0 {
		f.failNext--
		return DeliveryResult{}, errors.New("destination unavailable: 503 Service Unavailable")
	}
	f.posted = append(f.posted, message)
	if f.lostResponse > 0 {
		f.lostResponse--
		return DeliveryResult{}, errors.New("read response: connection reset")
	}
	id := strconv.Itoa(len(f.posted))
	return DeliveryResult{ExternalID: id, ExternalURL: "https://fake.invalid/post/" + id}, nil
}

func (f *fakeChangemakerPublisher) counts() (posted, finds, delivers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posted), f.findCalls, f.deliverCalls
}

type changemakerReviewFixture struct {
	t       *testing.T
	cm      changemakerFixture
	now     *time.Time
	api     *API
	control http.Handler
	fake    *fakeChangemakerPublisher
	run     Run
}

func changemakerReviewGitea() *giteaActivityFixture {
	return &giteaActivityFixture{items: map[string][]map[string]any{
		"acme/app": {
			giteaFixtureItem(3, "Fix parser crash", "Merged the parser fix.", "closed", "2026-09-23T15:00:00Z", true, true),
			giteaFixtureItem(1, "Parser crashes on empty input", "Steps to reproduce.", "closed", "2026-09-23T16:00:00Z", false, false),
		},
	}}
}

const changemakerFirstDraft = "## Digest\n\nThe parser crash was fixed [S1, S2]."

func newChangemakerReviewFixture(t *testing.T, inputs map[string]string) *changemakerReviewFixture {
	t.Helper()
	for _, key := range []string{"CHANGEMAKER_PUBLICATION_ADAPTER", "CHANGEMAKER_PUBLICATION_GITEA_ISSUE", "CHANGEMAKER_PUBLICATION_ALLOWED_REPOS"} {
		t.Setenv(key, "")
	}
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	cm := newChangemakerFixture(t, changemakerReviewGitea(), changemakerFirstDraft)
	now := changemakerFixtureNow
	f := &changemakerReviewFixture{t: t, cm: cm, now: &now, fake: &fakeChangemakerPublisher{}}
	if inputs == nil {
		inputs = map[string]string{"source_repositories": "acme/app"}
	}
	run, detail := cm.run(t, inputs)
	if run.Status != RunSucceeded {
		t.Fatalf("grounded run = %+v events=%+v", run, detail.Events)
	}
	f.run = run
	f.api = NewAPI(cm.store, NewRunner(cm.store))
	f.api.clock = func() time.Time { return *f.now }
	f.api.SetChangemakerPublication(NewChangemakerPublication(f.fake, ""))
	f.control = f.api.ControlPlaneHandler()
	return f
}

func (f *changemakerReviewFixture) setModelResponse(response string) {
	f.cm.model.mu.Lock()
	f.cm.model.response = response
	f.cm.model.mu.Unlock()
}

func (f *changemakerReviewFixture) do(method, path, token string, body any) *httptest.ResponseRecorder {
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

func changemakerDraftPath(runID, suffix string) string {
	return "/api/changemaker/drafts/" + runID + suffix
}

func (f *changemakerReviewFixture) review(runID string) ChangemakerDraftReview {
	f.t.Helper()
	response := f.do(http.MethodGet, changemakerDraftPath(runID, ""), "read-token", nil)
	if response.Code != http.StatusOK {
		f.t.Fatalf("review status = %d body=%s", response.Code, response.Body.String())
	}
	var review ChangemakerDraftReview
	if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil {
		f.t.Fatal(err)
	}
	return review
}

func selectedView(t *testing.T, review ChangemakerDraftReview) ChangemakerDraftVersionView {
	t.Helper()
	for _, view := range review.Versions {
		if view.Version == review.SelectedVersion {
			return view
		}
	}
	t.Fatalf("selected version %d missing from %+v", review.SelectedVersion, review.Versions)
	return ChangemakerDraftVersionView{}
}

func (f *changemakerReviewFixture) decide(runID string, view ChangemakerDraftVersionView, decision string) (ChangemakerReviewDecision, *httptest.ResponseRecorder) {
	f.t.Helper()
	// Decisions are ordered by time; keep each one strictly later.
	*f.now = f.now.Add(time.Second)
	response := f.do(http.MethodPost, changemakerDraftPath(runID, "/decisions"), "admin-token", map[string]any{"version": view.Version, "draft_sha256": view.DraftSHA256, "evidence_sha256": view.EvidenceSHA256, "decision": decision, "notes": "reviewed " + decision})
	var body struct {
		Decision ChangemakerReviewDecision `json:"decision"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body.Decision, response
}

func (f *changemakerReviewFixture) approve(runID string) (ChangemakerDraftVersionView, ChangemakerReviewDecision) {
	f.t.Helper()
	view := selectedView(f.t, f.review(runID))
	decision, response := f.decide(runID, view, ChangemakerDecisionApproved)
	if response.Code != http.StatusCreated {
		f.t.Fatalf("approve = %d %s", response.Code, response.Body.String())
	}
	return view, decision
}

func (f *changemakerReviewFixture) publish(runID, approvalID, sha string) (ChangemakerPublicationResponse, *httptest.ResponseRecorder) {
	f.t.Helper()
	response := f.do(http.MethodPost, changemakerDraftPath(runID, "/publications"), "admin-token", map[string]string{"approval_id": approvalID, "draft_sha256": sha})
	var body ChangemakerPublicationResponse
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body, response
}

type changemakerRevisionResponse struct {
	Version  ChangemakerDraftVersion   `json:"version"`
	Decision ChangemakerReviewDecision `json:"decision"`
	Run      Run                       `json:"run"`
}

func (f *changemakerReviewFixture) revise(runID string, view ChangemakerDraftVersionView, notes, sources string) (changemakerRevisionResponse, *httptest.ResponseRecorder) {
	f.t.Helper()
	response := f.do(http.MethodPost, changemakerDraftPath(runID, "/revisions"), "admin-token", map[string]any{"version": view.Version, "draft_sha256": view.DraftSHA256, "evidence_sha256": view.EvidenceSHA256, "notes": notes, "sources": sources})
	var body changemakerRevisionResponse
	_ = json.Unmarshal(response.Body.Bytes(), &body)
	return body, response
}

func TestChangemakerReviewApproveAndPublishEndToEndWithFakePublisher(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	detail, err := f.cm.store.RunDetail(f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, _, _ := artifactBody(t, detail, changemakerEvidenceArtifactName)
	draftArtifact, draftBody, _ := artifactBody(t, detail, changemakerDraftArtifactName)

	review := f.review(f.run.ID)
	view := selectedView(t, review)
	if review.ThreadID != f.run.ID || review.State != "awaiting-review" || review.LatestVersion != 1 || review.CurrentVersion != 1 || !review.Publication.Enabled || review.Publication.Destination != "fake/changemaker#9" {
		t.Fatalf("review = %+v", review)
	}
	if !view.Ready || !view.DraftGenerated || view.DraftSHA256 != draftArtifact.SHA256 || view.EvidenceSHA256 != evidence.SHA256 || view.EvidenceArtifactID != evidence.ID || view.SourceMode != ChangemakerSourceModeOriginal || view.GroundingStatus != SourceStatusComplete {
		t.Fatalf("view = %+v", view)
	}
	if posted, finds, delivers := f.fake.counts(); posted+finds+delivers != 0 {
		t.Fatal("review or a completed run reached the publisher")
	}

	// Publication requires an approval.
	if _, response := f.publish(f.run.ID, "", view.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-required" {
		t.Fatalf("unapproved publish = %d %s", response.Code, response.Body.String())
	}
	_, approval := f.approve(f.run.ID)
	if approval.DraftSHA256 != view.DraftSHA256 || approval.EvidenceSHA256 != view.EvidenceSHA256 || approval.Adapter != "fake-publisher" || approval.Destination != "fake/changemaker#9" || approval.Actor == "" {
		t.Fatalf("approval = %+v", approval)
	}
	if state := f.review(f.run.ID).State; state != "approved" {
		t.Fatalf("state after approval = %s", state)
	}
	published, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256)
	if response.Code != http.StatusCreated || published.Outcome != "published" || published.Receipt.Status != DeliveryDelivered || published.Receipt.ExternalURL != "https://fake.invalid/post/1" || published.Receipt.ApprovalID != approval.ID {
		t.Fatalf("publish = %d %+v", response.Code, published)
	}
	f.fake.mu.Lock()
	message := f.fake.posted[0]
	f.fake.mu.Unlock()
	if !strings.HasPrefix(message.Body, draftBody) || !strings.Contains(message.Body, changemakerPublicationMarker(published.Receipt.IdempotencyKey)) || !strings.Contains(message.Body, view.EvidenceSHA256) || message.ArtifactSHA256 != view.DraftSHA256 {
		t.Fatalf("published body does not carry the exact approved draft:\n%s", message.Body)
	}
	if published.Receipt.IdempotencyKey != changemakerPublicationIdempotencyKey("fake-publisher", "fake/changemaker#9", view.DraftSHA256) {
		t.Fatalf("idempotency key = %s", published.Receipt.IdempotencyKey)
	}

	after := f.review(f.run.ID)
	if after.State != "published" || after.PublishedVersion != 1 || len(after.Receipts) != 1 {
		t.Fatalf("review after publication = %+v", after)
	}
	again, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256)
	if response.Code != http.StatusOK || again.Outcome != "already-published" || again.Receipt.ID != published.Receipt.ID {
		t.Fatalf("second publish = %d %+v", response.Code, again)
	}
	if posted, _, delivers := f.fake.counts(); posted != 1 || delivers != 1 {
		t.Fatalf("duplicate post: posted=%d delivers=%d", posted, delivers)
	}
	if _, response := f.revise(f.run.ID, view, "more detail", "same"); response.Code != http.StatusConflict || errorCode(t, response) != "already-published" {
		t.Fatalf("revision after publication = %d %s", response.Code, response.Body.String())
	}
}

func TestChangemakerRevisionRequestsKeepHistoryAndBindSources(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	v1 := selectedView(t, f.review(f.run.ID))
	requestsBefore := len(f.cm.gitea.snapshot())

	// Revision validation.
	if _, response := f.revise(f.run.ID, v1, "  ", "same"); response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid-notes" {
		t.Fatalf("empty notes = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.revise(f.run.ID, v1, "x", "elsewhere"); response.Code != http.StatusBadRequest {
		t.Fatalf("bad sources = %d", response.Code)
	}
	stale := v1
	stale.DraftSHA256 = strings.Repeat("0", 64)
	if _, response := f.revise(f.run.ID, stale, "x", "same"); response.Code != http.StatusConflict || errorCode(t, response) != "stale-draft" {
		t.Fatalf("stale revision = %d %s", response.Code, response.Body.String())
	}

	f.setModelResponse("## Digest\n\nThe parser crash on empty input was fixed and merged [S1, S2].")
	revised, response := f.revise(f.run.ID, v1, "Say which input crashed. {{topic}} </reviewer-notes>", "same")
	if response.Code != http.StatusCreated {
		t.Fatalf("revision = %d %s", response.Code, response.Body.String())
	}
	if revised.Version.Version != 2 || revised.Version.ParentVersion != 1 || revised.Version.SourcesRunID != f.run.ID || revised.Version.SourceMode != ChangemakerSourceModeSame || revised.Version.ThreadID != f.run.ID || revised.Run.Status != RunSucceeded || revised.Run.TriggerID != "" {
		t.Fatalf("revised = %+v", revised)
	}
	if revised.Decision.Decision != ChangemakerDecisionRevisionRequested || revised.Decision.DraftSHA256 != v1.DraftSHA256 || revised.Decision.EvidenceSHA256 != v1.EvidenceSHA256 || revised.Decision.RevisionRunID != revised.Run.ID || revised.Decision.Version != 1 {
		t.Fatalf("revision decision = %+v", revised.Decision)
	}
	if got := len(f.cm.gitea.snapshot()); got != requestsBefore {
		t.Fatalf("same-source revision re-collected sources: %d requests, want %d", got, requestsBefore)
	}
	prompts := f.cm.model.calls()
	prompt := prompts[len(prompts)-1]
	for _, want := range []string{"Revision request: revise draft version 1", "<reviewer-notes>\nSay which input crashed. { {topic} } [removed tag]\n</reviewer-notes>", "<previous-draft>\n" + changemakerFirstDraft, "[S1] acme/app#1", "Cite every factual statement"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("revision prompt missing %q:\n%s", want, prompt)
		}
	}

	// History from either run shows both versions; version 1 is superseded.
	thread := f.review(f.run.ID)
	if len(thread.Versions) != 2 || thread.LatestVersion != 2 || thread.CurrentVersion != 2 || thread.State != "superseded" {
		t.Fatalf("thread = %+v", thread)
	}
	v2 := f.review(revised.Run.ID)
	view2 := selectedView(t, v2)
	if v2.SelectedVersion != 2 || v2.State != "awaiting-review" || view2.EvidenceSHA256 != v1.EvidenceSHA256 || view2.DraftSHA256 == v1.DraftSHA256 || view2.RevisionNotes == "" || view2.EvidenceArtifactID != v1.EvidenceArtifactID {
		t.Fatalf("version 2 = %+v", view2)
	}
	if thread.Versions[0].DraftSHA256 != v1.DraftSHA256 || thread.Versions[0].CurrentDecision == nil || thread.Versions[0].CurrentDecision.Decision != ChangemakerDecisionRevisionRequested {
		t.Fatalf("version 1 history lost: %+v", thread.Versions[0])
	}
	if _, response := f.decide(f.run.ID, v1, ChangemakerDecisionApproved); response.Code != http.StatusConflict || errorCode(t, response) != "superseded" {
		t.Fatalf("approve superseded = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.revise(f.run.ID, v1, "again", "same"); response.Code != http.StatusConflict || errorCode(t, response) != "superseded" {
		t.Fatalf("revise superseded = %d %s", response.Code, response.Body.String())
	}

	// A re-collected revision queries the same window again.
	f.setModelResponse("## Digest\n\nParser fix merged [S2]; empty input crash closed [S1].")
	recollected, response := f.revise(revised.Run.ID, view2, "Lead with the merged fix.", "recollect")
	if response.Code != http.StatusCreated || recollected.Version.Version != 3 || recollected.Version.ParentVersion != 2 || recollected.Version.SourceMode != ChangemakerSourceModeRecollected || recollected.Version.SourcesRunID != recollected.Run.ID || recollected.Run.Status != RunSucceeded {
		t.Fatalf("recollect = %d %+v", response.Code, recollected)
	}
	if got := len(f.cm.gitea.snapshot()); got <= requestsBefore {
		t.Fatal("recollected revision did not query sources")
	}
	assertReadOnlyCollection(t, f.cm.gitea.snapshot(), "2026-09-23T09:00:00Z", "2026-09-24T09:00:00Z")
	view3 := selectedView(t, f.review(recollected.Run.ID))
	if view3.EvidenceSHA256 != v1.EvidenceSHA256 || !view3.Ready {
		t.Fatalf("re-collected evidence over an unchanged window differs: %+v", view3)
	}
	full := f.review(f.run.ID)
	if len(full.Versions) != 3 || full.Versions[1].CurrentDecision == nil || full.Versions[1].CurrentDecision.RevisionRunID != recollected.Run.ID {
		t.Fatalf("history = %+v", full.Versions)
	}
	// Approve and publish the latest version only.
	_, approval := f.approve(recollected.Run.ID)
	if _, response := f.publish(revised.Run.ID, approval.ID, view2.DraftSHA256); response.Code != http.StatusConflict {
		t.Fatalf("publishing a superseded version = %d %s", response.Code, response.Body.String())
	}
	published, response := f.publish(recollected.Run.ID, approval.ID, view3.DraftSHA256)
	if response.Code != http.StatusCreated || published.Receipt.ArtifactSHA256 != view3.DraftSHA256 {
		t.Fatalf("publish latest = %d %+v", response.Code, published)
	}
	if final := f.review(f.run.ID); final.PublishedVersion != 3 || final.Versions[2].State != "published" || final.Versions[0].State != "superseded" {
		t.Fatalf("final thread = %+v", final)
	}
}

func TestChangemakerRevisionAfterFailedRevisionRunRevisesLastGoodVersion(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	v1 := selectedView(t, f.review(f.run.ID))
	revised, response := f.revise(f.run.ID, v1, "tighten", "same")
	if response.Code != http.StatusCreated {
		t.Fatalf("revision = %d %s", response.Code, response.Body.String())
	}
	// Simulate the revision run failing after it was recorded.
	if _, err := f.cm.store.FinishRun(revised.Run.ID, RunFailed, "model unavailable"); err != nil {
		t.Fatal(err)
	}
	thread := f.review(f.run.ID)
	if thread.LatestVersion != 2 || thread.CurrentVersion != 1 || thread.State == "superseded" {
		t.Fatalf("failed revision should reopen version 1: %+v", thread)
	}
	retry, response := f.revise(f.run.ID, v1, "tighten again", "same")
	if response.Code != http.StatusCreated || retry.Version.Version != 3 || retry.Version.ParentVersion != 1 {
		t.Fatalf("retry revision = %d %+v", response.Code, retry)
	}
}

func TestChangemakerStaleApprovalsAreRefused(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	view, approval := f.approve(f.run.ID)

	// Wrong evidence digest is refused at decision time.
	wrong := view
	wrong.EvidenceSHA256 = strings.Repeat("e", 64)
	if _, response := f.decide(f.run.ID, wrong, ChangemakerDecisionApproved); response.Code != http.StatusConflict || errorCode(t, response) != "stale-evidence" {
		t.Fatalf("stale evidence decision = %d %s", response.Code, response.Body.String())
	}

	// The draft digest changes after approval (a newer draft artifact).
	if _, err := f.cm.store.AddArtifact(CreateArtifactInput{RunID: f.run.ID, Name: changemakerDraftArtifactName, Kind: "markdown", URI: inlineArtifactURI("text/markdown;charset=utf-8", "# edited after approval\n")}); err != nil {
		t.Fatal(err)
	}
	review := f.review(f.run.ID)
	current := selectedView(t, review)
	if current.DraftSHA256 == view.DraftSHA256 || review.State != "awaiting-review" || current.CurrentDecision != nil {
		t.Fatalf("review after digest change = %+v", review)
	}
	if _, response := f.publish(f.run.ID, approval.ID, current.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "stale-approval" {
		t.Fatalf("stale approval publish = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "stale-draft" {
		t.Fatalf("old digest publish = %d %s", response.Code, response.Body.String())
	}
	if _, response := f.decide(f.run.ID, view, ChangemakerDecisionApproved); response.Code != http.StatusConflict || errorCode(t, response) != "stale-draft" {
		t.Fatalf("old digest decision = %d %s", response.Code, response.Body.String())
	}

	// Expired, superseded, and retargeted approvals.
	g := newChangemakerReviewFixture(t, nil)
	gView, gApproval := g.approve(g.run.ID)
	*g.now = g.now.Add(changemakerApprovalTTL + time.Second)
	if state := g.review(g.run.ID).State; state != "approval-expired" {
		t.Fatalf("expired state = %s", state)
	}
	if _, response := g.publish(g.run.ID, gApproval.ID, gView.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-expired" {
		t.Fatalf("expired publish = %d %s", response.Code, response.Body.String())
	}
	_, fresh := g.approve(g.run.ID)
	if _, response := g.decide(g.run.ID, gView, ChangemakerDecisionRejected); response.Code != http.StatusCreated {
		t.Fatalf("reject = %d", response.Code)
	}
	if state := g.review(g.run.ID).State; state != "rejected" {
		t.Fatalf("rejected state = %s", state)
	}
	if _, response := g.publish(g.run.ID, fresh.ID, gView.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "approval-superseded" {
		t.Fatalf("approval replaced by rejection = %d %s", response.Code, response.Body.String())
	}
	_, retarget := g.approve(g.run.ID)
	g.api.SetChangemakerPublication(NewChangemakerPublication(&fakeChangemakerPublisher{destination: "fake/other#1"}, ""))
	if state := g.review(g.run.ID).State; state != "destination-changed" {
		t.Fatalf("retargeted state = %s", state)
	}
	if _, response := g.publish(g.run.ID, retarget.ID, gView.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "destination-changed" {
		t.Fatalf("retargeted publish = %d %s", response.Code, response.Body.String())
	}
	if posted, _, delivers := f.fake.counts(); posted != 0 || delivers != 0 {
		t.Fatal("a stale approval published")
	}
	if posted, _, delivers := g.fake.counts(); posted != 0 || delivers != 0 {
		t.Fatal("an expired, replaced, or retargeted approval published")
	}
}

func TestChangemakerPublicationFailureThenRetryDoesNotDuplicate(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	view, approval := f.approve(f.run.ID)
	f.fake.failNext = 1
	failed, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256)
	if response.Code != http.StatusBadGateway || failed.Outcome != "failed" || failed.Receipt.Status != DeliveryFailed || failed.Receipt.Attempts != 1 || !strings.Contains(failed.Receipt.LastError, "503") {
		t.Fatalf("failed publish = %d %+v", response.Code, failed)
	}
	if state := f.review(f.run.ID).State; state != "publication-failed" {
		t.Fatalf("state after failure = %s", state)
	}
	retried, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256)
	if response.Code != http.StatusCreated || retried.Outcome != "published" || retried.Receipt.ID != failed.Receipt.ID || retried.Receipt.Attempts != 2 || retried.Receipt.LastError != "" {
		t.Fatalf("retry = %d %+v", response.Code, retried)
	}
	if posted, _, _ := f.fake.counts(); posted != 1 {
		t.Fatalf("posted = %d", posted)
	}

	// The post lands but the response is lost: the retry reconciles.
	g := newChangemakerReviewFixture(t, nil)
	gView, gApproval := g.approve(g.run.ID)
	g.fake.lostResponse = 1
	if body, response := g.publish(g.run.ID, gApproval.ID, gView.DraftSHA256); response.Code != http.StatusBadGateway || body.Receipt.Status != DeliveryFailed {
		t.Fatalf("lost response = %d %+v", response.Code, body)
	}
	reconciled, response := g.publish(g.run.ID, gApproval.ID, gView.DraftSHA256)
	if response.Code != http.StatusOK || reconciled.Outcome != "reconciled" || !reconciled.Receipt.Reconciled || reconciled.Receipt.ExternalID != "1" {
		t.Fatalf("reconcile = %d %+v", response.Code, reconciled)
	}
	if posted, _, delivers := g.fake.counts(); posted != 1 || delivers != 1 {
		t.Fatalf("retry after lost response duplicated: posted=%d delivers=%d", posted, delivers)
	}

	// A destination that cannot be searched is never posted to.
	h := newChangemakerReviewFixture(t, nil)
	hView, hApproval := h.approve(h.run.ID)
	h.fake.findErr = errors.New("list comments: 500")
	if body, response := h.publish(h.run.ID, hApproval.ID, hView.DraftSHA256); response.Code != http.StatusBadGateway || !strings.Contains(body.Receipt.LastError, "reconcile") {
		t.Fatalf("unsearchable destination = %d %+v", response.Code, body)
	}
	if _, _, delivers := h.fake.counts(); delivers != 0 {
		t.Fatal("posted without confirming absence")
	}

	// A concurrent in-flight claim is refused until it goes stale.
	k := newChangemakerReviewFixture(t, nil)
	kView, kApproval := k.approve(k.run.ID)
	key := changemakerPublicationIdempotencyKey(k.fake.Name(), k.fake.Destination(), kView.DraftSHA256)
	if _, claimed, err := k.cm.store.BeginChangemakerPublication(BeginDeliveryInput{IdempotencyKey: key, RunID: k.run.ID, ArtifactID: kView.Draft.ID, ArtifactSHA256: kView.DraftSHA256, Adapter: k.fake.Name(), Destination: k.fake.Destination(), ApprovalID: kApproval.ID, Actor: "other", StaleAfter: changemakerPublicationClaimTTL}); err != nil || !claimed {
		t.Fatalf("claim = %v %v", claimed, err)
	}
	if _, response := k.publish(k.run.ID, kApproval.ID, kView.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "publication-in-progress" {
		t.Fatalf("concurrent publish = %d %s", response.Code, response.Body.String())
	}
}

func TestChangemakerScheduledRunNeverPublishes(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	trigger, ok := f.cm.store.Trigger("daily-changemaker-native")
	if !ok {
		t.Fatal("seeded Changemaker trigger missing")
	}
	trigger.Inputs = cloneMap(trigger.Inputs)
	trigger.Inputs["source_repositories"] = "acme/app"
	trigger.Enabled = true
	if err := f.cm.store.UpsertTrigger(trigger); err != nil {
		t.Fatal(err)
	}
	due := changemakerFixtureNow.Add(-time.Minute)
	if _, err := f.cm.store.UpdateTriggerScheduleState(trigger.ID, nil, &due, ""); err != nil {
		t.Fatal(err)
	}
	scheduler := NewScheduler(f.cm.store, NewRunner(f.cm.store), time.Minute, func() time.Time { return changemakerFixtureNow })
	if err := scheduler.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	var scheduled Run
	for _, run := range f.cm.store.ListRuns() {
		if run.TriggerID == trigger.ID {
			scheduled = run
		}
	}
	if scheduled.ID == "" || scheduled.Status != RunSucceeded {
		t.Fatalf("scheduled run = %+v", scheduled)
	}
	review := f.review(scheduled.ID)
	if review.State != "awaiting-review" || len(review.Receipts) != 0 || len(review.Decisions) != 0 || selectedView(t, review).TriggerID != trigger.ID {
		t.Fatalf("scheduled review = %+v", review)
	}
	if posted, finds, delivers := f.fake.counts(); posted+finds+delivers != 0 {
		t.Fatalf("scheduled run reached the publisher: posted=%d finds=%d delivers=%d", posted, finds, delivers)
	}
	for _, step := range scheduled.PipelineSnapshot.Steps {
		if step.Executor != "internal" && step.Executor != "source" && step.Executor != "model" {
			t.Fatalf("Changemaker pipeline has an unexpected executor %q", step.Executor)
		}
	}
}

func TestChangemakerReviewAuthorizationAndListenerBoundaries(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	view := selectedView(t, f.review(f.run.ID))
	if response := f.do(http.MethodGet, changemakerDraftPath(f.run.ID, ""), "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated review = %d", response.Code)
	}
	decision := map[string]any{"version": 1, "draft_sha256": view.DraftSHA256, "evidence_sha256": view.EvidenceSHA256, "decision": "approved"}
	revision := map[string]any{"version": 1, "draft_sha256": view.DraftSHA256, "evidence_sha256": view.EvidenceSHA256, "notes": "x"}
	publish := map[string]any{"approval_id": "x", "draft_sha256": view.DraftSHA256}
	for suffix, body := range map[string]any{"/decisions": decision, "/revisions": revision, "/publications": publish} {
		if response := f.do(http.MethodPost, changemakerDraftPath(f.run.ID, suffix), "read-token", body); response.Code != http.StatusForbidden {
			t.Errorf("read-token POST %s = %d", suffix, response.Code)
		}
		if response := f.do(http.MethodPost, changemakerDraftPath(f.run.ID, suffix), "", body); response.Code != http.StatusUnauthorized {
			t.Errorf("unauthenticated POST %s = %d", suffix, response.Code)
		}
	}
	model := f.api.ModelIngressHandler()
	for _, probe := range []struct{ method, suffix string }{{http.MethodGet, ""}, {http.MethodPost, "/decisions"}, {http.MethodPost, "/revisions"}, {http.MethodPost, "/publications"}} {
		request := httptest.NewRequest(probe.method, changemakerDraftPath(f.run.ID, probe.suffix), strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		model.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "route not available on this listener") {
			t.Errorf("model listener %s %s = %d %s", probe.method, probe.suffix, response.Code, response.Body.String())
		}
	}
	if review := f.review(f.run.ID); len(review.Decisions) != 0 || len(review.Versions) != 1 || review.Access.Role != "read" || review.Access.CanWrite {
		t.Fatalf("authorization failures changed state: %+v", review)
	}
	if posted, finds, delivers := f.fake.counts(); posted+finds+delivers != 0 {
		t.Fatal("authorization failures or model listener reached the publisher")
	}
	// Non-Changemaker runs are not reviewable here.
	other, err := f.cm.store.CreateRun(CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	if response := f.do(http.MethodGet, changemakerDraftPath(other.ID, ""), "read-token", nil); response.Code != http.StatusNotFound || errorCode(t, response) != "not-changemaker" {
		t.Fatalf("devlog run review = %d %s", response.Code, response.Body.String())
	}
}

func TestChangemakerPublicationDisabledByDefaultAndNoDraftCannotBeApproved(t *testing.T) {
	f := newChangemakerReviewFixture(t, nil)
	api := NewAPI(f.cm.store, NewRunner(f.cm.store))
	api.clock = f.api.clock
	f.control = api.ControlPlaneHandler()
	review := f.review(f.run.ID)
	if review.Publication.Enabled || review.Publication.Adapter != DeliveryAdapterDisabled || !strings.Contains(review.Publication.Reason, "CHANGEMAKER_PUBLICATION_ADAPTER") || review.State != "awaiting-review" {
		t.Fatalf("default publication = %+v", review)
	}
	view, approval := f.approve(f.run.ID)
	if approval.Adapter != DeliveryAdapterDisabled || approval.Destination != "" {
		t.Fatalf("approval while disabled = %+v", approval)
	}
	if _, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "publication-disabled" {
		t.Fatalf("publish while disabled = %d %s", response.Code, response.Body.String())
	}
	// Enabling publication later does not turn the old approval into an
	// authorization for the new destination.
	f.control = f.api.ControlPlaneHandler()
	if _, response := f.publish(f.run.ID, approval.ID, view.DraftSHA256); response.Code != http.StatusConflict || errorCode(t, response) != "destination-changed" {
		t.Fatalf("approval from disabled config = %d %s", response.Code, response.Body.String())
	}

	empty := newChangemakerReviewFixture(t, map[string]string{})
	emptyReview := empty.review(empty.run.ID)
	emptyView := selectedView(t, emptyReview)
	if emptyReview.State != "no-draft" || emptyView.DraftGenerated || !emptyView.Ready {
		t.Fatalf("no-source review = %+v", emptyReview)
	}
	if _, response := empty.decide(empty.run.ID, emptyView, ChangemakerDecisionApproved); response.Code != http.StatusConflict || errorCode(t, response) != "draft-not-generated" {
		t.Fatalf("approve empty draft = %d %s", response.Code, response.Body.String())
	}
	if _, response := empty.decide(empty.run.ID, emptyView, ChangemakerDecisionRejected); response.Code != http.StatusCreated {
		t.Fatalf("reject empty draft = %d %s", response.Code, response.Body.String())
	}
}

func TestChangemakerPublicationConfigurationRequiresExplicitAllowlistedDestination(t *testing.T) {
	client := func() GiteaIssueCommentClient { return &fakeGiteaCommentClient{} }
	cases := []struct {
		name, adapter, issue, allowed string
		gitea                         bool
		reason                        string
	}{
		{"default disabled", "", "", "", true, "CHANGEMAKER_PUBLICATION_ADAPTER is disabled"},
		{"unsupported adapter", "slack", "o/r#1", "o/r", true, "not supported"},
		{"missing destination", "gitea-issue-comment", "", "o/r", true, "CHANGEMAKER_PUBLICATION_GITEA_ISSUE is not set"},
		{"not allowlisted", "gitea-issue-comment", "o/r#1", "o/other", true, "CHANGEMAKER_PUBLICATION_ALLOWED_REPOS"},
		{"no gitea credentials", "gitea-issue-comment", "o/r#1", "o/r", false, "GITEA_TOKEN"},
	}
	for _, test := range cases {
		adapter, reason := changemakerPublicationAdapterFromConfig(test.adapter, test.issue, test.allowed, test.gitea, client)
		if adapter != nil || !strings.Contains(reason, test.reason) {
			t.Errorf("%s: adapter=%v reason=%q", test.name, adapter, reason)
		}
	}
	adapter, reason := changemakerPublicationAdapterFromConfig("gitea-issue-comment", "owner/news#4", "owner/news", true, client)
	if adapter == nil || reason != "" || adapter.Destination() != "owner/news#4" {
		t.Fatalf("valid config adapter=%v reason=%q", adapter, reason)
	}
	// Markers are namespaced: a Devlog marker for the same key is not a
	// Changemaker publication.
	key := strings.Repeat("c", 64)
	comments := &fakeGiteaCommentClient{comments: []GiteaIssueComment{{ID: 1, Body: devlogDeliveryMarker(key), User: &struct {
		Login string `json:"login"`
	}{Login: "bot"}}}}
	namespaced := &giteaIssueCommentDelivery{client: comments, repo: "owner/news", issue: 4, markerPrefix: changemakerPublicationMarkerStart}
	if _, found, err := namespaced.FindDelivery(t.Context(), key); err != nil || found {
		t.Fatalf("devlog marker matched a changemaker publication: found=%v err=%v", found, err)
	}
	comments.comments = append(comments.comments, GiteaIssueComment{ID: 2, Body: "draft\n" + changemakerPublicationMarker(key), User: &struct {
		Login string `json:"login"`
	}{Login: "bot"}})
	if result, found, err := namespaced.FindDelivery(t.Context(), key); err != nil || !found || result.ExternalID != "2" {
		t.Fatalf("changemaker marker not found: %+v found=%v err=%v", result, found, err)
	}
}

func TestChangemakerReviewMigrationDeclaresDurableHistory(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/038_changemaker_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"create table if not exists changemaker_draft_versions", "constraint changemaker_draft_versions_thread_version unique (thread_id, version)", "constraint changemaker_draft_versions_run unique (run_id)",
		"create table if not exists changemaker_review_decisions", "decision in ('approved','rejected','revision-requested')", "draft_sha256 ~ '^[0-9a-f]{64}$'", "evidence_sha256 ~ '^[0-9a-f]{64}$'",
		"create table if not exists changemaker_publication_receipts", "constraint changemaker_publication_receipts_idempotency_key unique (idempotency_key)",
	} {
		if !strings.Contains(string(data), field) {
			t.Errorf("changemaker review migration missing %q", field)
		}
	}
	if strings.Contains(string(data), "references runs") {
		t.Error("review evidence must survive run retention; do not reference runs")
	}
}

func TestMemoryChangemakerRevisionRejectsConcurrentAppend(t *testing.T) {
	store := NewStore(func() time.Time { return changemakerFixtureNow })
	sha := strings.Repeat("a", 64)
	now := changemakerFixtureNow
	root := ChangemakerDraftVersion{ID: "v1", ThreadID: "run-1", Version: 1, RunID: "run-1", SourcesRunID: "run-1", SourceMode: ChangemakerSourceModeOriginal, CreatedAt: now}
	input := func(runID, id string) ChangemakerRevisionInput {
		return ChangemakerRevisionInput{
			Root: root, BaseVersion: 1, ExpectedLatestVersion: 1,
			Version:  ChangemakerDraftVersion{ID: id, ThreadID: "run-1", Version: 2, RunID: runID, ParentVersion: 1, SourcesRunID: "run-1", SourceMode: ChangemakerSourceModeSame, RevisionNotes: "n", RequestedBy: "a", CreatedAt: now},
			Decision: ChangemakerReviewDecision{ID: "d-" + id, ThreadID: "run-1", Version: 1, RunID: "run-1", ArtifactID: "art", DraftSHA256: sha, EvidenceSHA256: sha, Decision: ChangemakerDecisionRevisionRequested, Notes: "n", Actor: "a", Adapter: "disabled", RevisionRunID: runID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)},
		}
	}
	if _, _, err := store.RecordChangemakerRevision(input("run-2", "v2")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RecordChangemakerRevision(input("run-3", "v3")); !errors.Is(err, ErrConflict) {
		t.Fatalf("second append on the same base = %v", err)
	}
	bad := input("run-4", "v4")
	bad.Decision.Notes = ""
	if _, _, err := store.RecordChangemakerRevision(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("revision without notes = %v", err)
	}
	versions, _ := store.ListChangemakerDraftVersions("run-1")
	if len(versions) != 2 || versions[0].Version != 1 || versions[1].RunID != "run-2" {
		t.Fatalf("versions = %+v", versions)
	}
}
