package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func openChangemakerReviewPostgres(t *testing.T) (*PostgresStore, func(time.Duration), func() time.Time, func(...string)) {
	t.Helper()
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL Changemaker review tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	var mu sync.Mutex
	now := changemakerFixtureNow
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	cleanup := func(ids ...string) {
		t.Cleanup(func() {
			for _, id := range ids {
				_, _ = db.ExecContext(context.Background(), `delete from changemaker_publication_receipts where run_id=$1`, id)
				_, _ = db.ExecContext(context.Background(), `delete from changemaker_review_decisions where thread_id=$1 or run_id=$1`, id)
				_, _ = db.ExecContext(context.Background(), `delete from changemaker_draft_versions where thread_id=$1 or run_id=$1`, id)
				_, _ = db.ExecContext(context.Background(), `delete from runs where id=$1`, id)
			}
		})
	}
	return NewPostgresStore(db, clock), advance, clock, cleanup
}

// TestPostgresChangemakerReviewHistoryAndReceiptsPersist exercises migration
// 038: version history, decisions, concurrent appends, and the publication
// receipt claim contract. It skips without SWITCHYARD_TEST_DATABASE_URL; run
// `make postgres-test`.
func TestPostgresChangemakerReviewHistoryAndReceiptsPersist(t *testing.T) {
	store, advance, clock, cleanup := openChangemakerReviewPostgres(t)
	thread := newID("pg-changemaker-thread")
	cleanup(thread)
	sha := strings.Repeat("a", 64)
	evidence := strings.Repeat("b", 64)
	root := ChangemakerDraftVersion{ID: newID("changemaker-version"), ThreadID: thread, Version: 1, RunID: thread, SourcesRunID: thread, SourceMode: ChangemakerSourceModeOriginal, CreatedAt: clock()}
	revision := func(runID string) ChangemakerRevisionInput {
		cleanup(runID)
		return ChangemakerRevisionInput{
			Root: root, BaseVersion: 1, ExpectedLatestVersion: 1,
			Version:  ChangemakerDraftVersion{ID: newID("changemaker-version"), ThreadID: thread, Version: 2, RunID: runID, ParentVersion: 1, SourcesRunID: thread, SourceMode: ChangemakerSourceModeSame, RevisionNotes: "tighten", RequestedBy: "token:admin", CreatedAt: clock()},
			Decision: ChangemakerReviewDecision{ID: newID("changemaker-decision"), ThreadID: thread, Version: 1, RunID: thread, ArtifactID: "artifact-1", DraftSHA256: sha, EvidenceSHA256: evidence, Decision: ChangemakerDecisionRevisionRequested, Notes: "tighten", Actor: "token:admin", Adapter: "disabled", RevisionRunID: runID, CreatedAt: clock(), ExpiresAt: clock().Add(changemakerApprovalTTL)},
		}
	}

	// Concurrent revision requests on the same base: exactly one appends.
	var wg sync.WaitGroup
	results := make(chan error, 5)
	for i := 0; i < 5; i++ {
		input := revision(newID("pg-changemaker-run"))
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := store.RecordChangemakerRevision(input)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrConflict):
		default:
			t.Fatalf("unexpected revision error %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("revision winners = %d", wins)
	}
	reopened := NewPostgresStore(store.db, clock)
	versions, err := reopened.ListChangemakerDraftVersions(thread)
	if err != nil || len(versions) != 2 || versions[0].Version != 1 || versions[0].ParentVersion != 0 || versions[1].ParentVersion != 1 || versions[1].SourceMode != ChangemakerSourceModeSame || versions[1].RevisionNotes != "tighten" {
		t.Fatalf("versions = %+v err=%v", versions, err)
	}
	byRun, err := reopened.ChangemakerDraftVersionByRun(versions[1].RunID)
	if err != nil || byRun.ID != versions[1].ID || byRun.ThreadID != thread {
		t.Fatalf("version by run = %+v err=%v", byRun, err)
	}
	if _, err := reopened.ChangemakerDraftVersionByRun("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing version error = %v", err)
	}

	advance(time.Second)
	approval := ChangemakerReviewDecision{ID: newID("changemaker-decision"), ThreadID: thread, Version: 2, RunID: versions[1].RunID, ArtifactID: "artifact-2", DraftSHA256: strings.Repeat("c", 64), EvidenceSHA256: evidence, Decision: ChangemakerDecisionApproved, Notes: "ok", Actor: "token:admin", Adapter: "gitea-issue-comment", Destination: "owner/news#4", CreatedAt: clock(), ExpiresAt: clock().Add(changemakerApprovalTTL)}
	if _, err := store.RecordChangemakerReviewDecision(approval); err != nil {
		t.Fatalf("RecordChangemakerReviewDecision() error = %v", err)
	}
	if _, err := store.RecordChangemakerReviewDecision(approval); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate decision error = %v", err)
	}
	invalid := approval
	invalid.ID, invalid.EvidenceSHA256 = newID("changemaker-decision"), "not-a-digest"
	if _, err := store.RecordChangemakerReviewDecision(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid decision error = %v", err)
	}
	decisions, err := reopened.ListChangemakerReviewDecisions(thread)
	if err != nil || len(decisions) != 2 || decisions[0].ID != approval.ID || decisions[1].Decision != ChangemakerDecisionRevisionRequested || decisions[1].RevisionRunID != versions[1].RunID {
		t.Fatalf("decisions = %+v err=%v", decisions, err)
	}
	loaded, err := reopened.ChangemakerReviewDecision(approval.ID)
	if err != nil || !loaded.CreatedAt.Equal(approval.CreatedAt) || loaded.Destination != "owner/news#4" {
		t.Fatalf("decision = %+v err=%v", loaded, err)
	}

	// Publication receipts share the claim contract with Devlog delivery.
	key := changemakerPublicationIdempotencyKey("gitea-issue-comment", "owner/news#4", approval.DraftSHA256)
	begin := BeginDeliveryInput{IdempotencyKey: key, RunID: approval.RunID, ArtifactID: "artifact-2", ArtifactSHA256: approval.DraftSHA256, Adapter: "gitea-issue-comment", Destination: "owner/news#4", ApprovalID: approval.ID, Actor: "token:admin", StaleAfter: changemakerPublicationClaimTTL}
	first, claimed, err := store.BeginChangemakerPublication(begin)
	if err != nil || !claimed || first.claimToken == "" {
		t.Fatalf("claim = %+v %v %v", first, claimed, err)
	}
	if _, _, err := store.BeginChangemakerPublication(begin); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim error = %v", err)
	}
	failed, err := store.FinishChangemakerPublication(FinishDeliveryInput{IdempotencyKey: key, ClaimToken: first.claimToken, Error: "deliver: 503"})
	if err != nil || failed.Status != DeliveryFailed {
		t.Fatalf("failed = %+v err=%v", failed, err)
	}
	retry, claimed, err := store.BeginChangemakerPublication(begin)
	if err != nil || !claimed || retry.ID != first.ID || retry.Attempts != 2 {
		t.Fatalf("retry = %+v %v %v", retry, claimed, err)
	}
	if _, err := store.FinishChangemakerPublication(FinishDeliveryInput{IdempotencyKey: key, ClaimToken: retry.claimToken, Delivered: true, ExternalID: "7"}); err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.ChangemakerPublicationReceipt(key)
	if err != nil || persisted.Status != DeliveryDelivered || persisted.Attempts != 2 || persisted.ExternalID != "7" || persisted.DeliveredAt == nil {
		t.Fatalf("persisted receipt = %+v err=%v", persisted, err)
	}
	if again, claimed, err := store.BeginChangemakerPublication(begin); err != nil || claimed || again.Status != DeliveryDelivered {
		t.Fatalf("claim after delivery = %+v %v %v", again, claimed, err)
	}
	receipts, err := reopened.ListChangemakerPublicationReceipts(approval.RunID)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts = %+v err=%v", receipts, err)
	}
	// Devlog receipts stay in their own table.
	if _, err := reopened.DevlogDeliveryReceipt(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("changemaker receipt leaked into devlog receipts: %v", err)
	}
}

// TestPostgresChangemakerReviewRevisionAndPublicationEndToEnd runs a grounded
// draft, a same-source revision, approval, a failed publication, and its
// retry against PostgreSQL with local fake sources, model, and publisher.
func TestPostgresChangemakerReviewRevisionAndPublicationEndToEnd(t *testing.T) {
	store, advance, clock, cleanup := openChangemakerReviewPostgres(t)
	gitea := changemakerReviewGitea()
	cm := newChangemakerFixture(t, gitea, changemakerFirstDraft)
	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	if err := SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := NewRunner(store).RunOnce(t.Context(), CreateRunInput{WorkflowID: changemakerDailyDigestID, Inputs: map[string]string{"topic": "project activity", "prompt": "Write a short digest.", "source_repositories": "acme/app"}})
	cleanup(run.ID)
	if err != nil || run.Status != RunSucceeded {
		t.Fatalf("grounded run = %+v err=%v", run, err)
	}
	fake := &fakeChangemakerPublisher{}
	api := NewAPI(store, NewRunner(store))
	// NewAPI owns a PostgreSQL model service singleton; release it so later
	// singleton tests can acquire it.
	t.Cleanup(func() { _ = api.Shutdown(context.Background()) })
	api.clock = clock
	api.SetChangemakerPublication(NewChangemakerPublication(fake, ""))
	control := api.ControlPlaneHandler()
	do := func(method, path string, body any) *httptest.ResponseRecorder {
		var reader *bytes.Reader
		if body == nil {
			reader = bytes.NewReader(nil)
		} else {
			encoded, _ := json.Marshal(body)
			reader = bytes.NewReader(encoded)
		}
		request := httptest.NewRequest(method, path, reader)
		request.Header.Set("Authorization", "Bearer admin-token")
		response := httptest.NewRecorder()
		control.ServeHTTP(response, request)
		return response
	}
	review := func(runID string) (ChangemakerDraftReview, ChangemakerDraftVersionView) {
		response := do(http.MethodGet, changemakerDraftPath(runID, ""), nil)
		var out ChangemakerDraftReview
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &out) != nil {
			t.Fatalf("review = %d %s", response.Code, response.Body.String())
		}
		return out, selectedView(t, out)
	}
	_, v1 := review(run.ID)
	cm.model.mu.Lock()
	cm.model.response = "## Digest\n\nParser crash on empty input fixed [S1, S2]."
	cm.model.mu.Unlock()
	response := do(http.MethodPost, changemakerDraftPath(run.ID, "/revisions"), map[string]any{"version": 1, "draft_sha256": v1.DraftSHA256, "evidence_sha256": v1.EvidenceSHA256, "notes": "Name the input.", "sources": "same"})
	var revised changemakerRevisionResponse
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &revised) != nil || revised.Run.Status != RunSucceeded {
		t.Fatalf("revision = %d %s", response.Code, response.Body.String())
	}
	cleanup(revised.Run.ID)
	thread, v2 := review(revised.Run.ID)
	if thread.ThreadID != run.ID || len(thread.Versions) != 2 || v2.EvidenceSHA256 != v1.EvidenceSHA256 || v2.DraftSHA256 == v1.DraftSHA256 || thread.Versions[0].State != "superseded" {
		t.Fatalf("thread = %+v", thread)
	}
	advance(time.Second)
	response = do(http.MethodPost, changemakerDraftPath(revised.Run.ID, "/decisions"), map[string]any{"version": 2, "draft_sha256": v2.DraftSHA256, "evidence_sha256": v2.EvidenceSHA256, "decision": "approved"})
	var decided struct {
		Decision ChangemakerReviewDecision `json:"decision"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &decided) != nil {
		t.Fatalf("approve = %d %s", response.Code, response.Body.String())
	}
	fake.failNext = 1
	publish := map[string]any{"approval_id": decided.Decision.ID, "draft_sha256": v2.DraftSHA256}
	if response := do(http.MethodPost, changemakerDraftPath(revised.Run.ID, "/publications"), publish); response.Code != http.StatusBadGateway {
		t.Fatalf("failed publish = %d %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodPost, changemakerDraftPath(revised.Run.ID, "/publications"), publish); response.Code != http.StatusCreated {
		t.Fatalf("retry publish = %d %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodPost, changemakerDraftPath(revised.Run.ID, "/publications"), publish); response.Code != http.StatusOK {
		t.Fatalf("repeat publish = %d %s", response.Code, response.Body.String())
	}
	if posted, _, _ := fake.counts(); posted != 1 {
		t.Fatalf("posted = %d", posted)
	}
	final, _ := review(run.ID)
	if final.PublishedVersion != 2 || final.Versions[1].CurrentReceipt == nil || final.Versions[1].CurrentReceipt.Attempts != 2 || len(final.Decisions) != 2 {
		t.Fatalf("final = %+v", final)
	}
}
