package controlplane

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPostgresDevlogDeliveryReceiptsPersistAndReconcile exercises migration
// 035 and the durable approval/receipt contract. It skips without
// SWITCHYARD_TEST_DATABASE_URL; run `make postgres-test`.
func TestPostgresDevlogDeliveryReceiptsPersistAndReconcile(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL Devlog delivery integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatalf("OpenPostgres() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	runID := newID("pg-devlog-run")
	cleanup := func() {
		_, _ = db.ExecContext(ctx, `delete from devlog_delivery_receipts where run_id=$1`, runID)
		_, _ = db.ExecContext(ctx, `delete from devlog_delivery_approvals where run_id=$1`, runID)
	}
	cleanup()
	t.Cleanup(cleanup)
	var mu sync.Mutex
	now := time.Now().UTC().Truncate(time.Microsecond)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	store := NewPostgresStore(db, clock)
	sha := strings.Repeat("b", 64)

	approval := DevlogDeliveryApproval{ID: newID("devlog-approval"), RunID: runID, ArtifactID: "artifact-1", ArtifactSHA256: sha, Adapter: "gitea-issue-comment", Destination: "owner/devlog#12", Decision: DevlogApprovalApproved, Actor: "token:admin", Reason: "ok", CreatedAt: clock(), ExpiresAt: clock().Add(devlogApprovalTTL)}
	if _, err := store.RecordDevlogDeliveryApproval(approval); err != nil {
		t.Fatalf("RecordDevlogDeliveryApproval() error = %v", err)
	}
	if _, err := store.RecordDevlogDeliveryApproval(approval); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate approval error = %v", err)
	}
	bad := approval
	bad.ID, bad.ArtifactSHA256 = newID("devlog-approval"), "not-a-digest"
	if _, err := store.RecordDevlogDeliveryApproval(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid approval error = %v", err)
	}
	advance(time.Second)
	rejection := approval
	rejection.ID, rejection.Decision, rejection.CreatedAt, rejection.ExpiresAt = newID("devlog-approval"), DevlogApprovalRejected, clock(), clock().Add(devlogApprovalTTL)
	if _, err := store.RecordDevlogDeliveryApproval(rejection); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.DevlogDeliveryApproval(approval.ID)
	if err != nil || loaded.ArtifactSHA256 != sha || !loaded.CreatedAt.Equal(approval.CreatedAt) || loaded.Decision != DevlogApprovalApproved {
		t.Fatalf("approval = %+v err=%v", loaded, err)
	}
	listed, err := store.ListDevlogDeliveryApprovals(runID)
	if err != nil || len(listed) != 2 || listed[0].ID != rejection.ID {
		t.Fatalf("approvals = %+v err=%v", listed, err)
	}
	if _, err := store.DevlogDeliveryApproval("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing approval error = %v", err)
	}

	key := devlogDeliveryIdempotencyKey("gitea-issue-comment", "owner/devlog#12", sha)
	begin := BeginDevlogDeliveryInput{IdempotencyKey: key, RunID: runID, ArtifactID: "artifact-1", ArtifactSHA256: sha, Adapter: "gitea-issue-comment", Destination: "owner/devlog#12", ApprovalID: approval.ID, Actor: "token:admin", StaleAfter: devlogDeliveryClaimTTL}

	// Concurrent claims: exactly one wins.
	var wg sync.WaitGroup
	type claim struct {
		receipt DevlogDeliveryReceipt
		claimed bool
		err     error
	}
	results := make(chan claim, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, claimed, err := store.BeginDevlogDelivery(begin)
			results <- claim{receipt, claimed, err}
		}()
	}
	wg.Wait()
	close(results)
	var first DevlogDeliveryReceipt
	winners := 0
	for result := range results {
		switch {
		case result.claimed && result.err == nil:
			winners++
			first = result.receipt
		case errors.Is(result.err, ErrConflict):
		default:
			t.Fatalf("unexpected claim result %+v", result)
		}
	}
	if winners != 1 || first.Attempts != 1 || first.Status != DevlogDeliverySending || first.claimToken == "" {
		t.Fatalf("winners=%d first=%+v", winners, first)
	}
	if _, err := store.FinishDevlogDelivery(FinishDevlogDeliveryInput{IdempotencyKey: key, ClaimToken: "wrong", Delivered: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong-token finish error = %v", err)
	}
	advance(time.Second)
	failed, err := store.FinishDevlogDelivery(FinishDevlogDeliveryInput{IdempotencyKey: key, ClaimToken: first.claimToken, Error: "deliver: 503 token=hunter2secretvalue"})
	if err != nil || failed.Status != DevlogDeliveryFailed || failed.DeliveredAt != nil || !strings.Contains(failed.LastError, "503") {
		t.Fatalf("failed = %+v err=%v", failed, err)
	}

	// Retry re-claims the same row; the attempt count survives.
	advance(time.Second)
	retry, claimed, err := store.BeginDevlogDelivery(begin)
	if err != nil || !claimed || retry.ID != first.ID || retry.Attempts != 2 || retry.claimToken == first.claimToken {
		t.Fatalf("retry = %+v claimed=%v err=%v", retry, claimed, err)
	}
	delivered, err := store.FinishDevlogDelivery(FinishDevlogDeliveryInput{IdempotencyKey: key, ClaimToken: retry.claimToken, Delivered: true, Reconciled: true, ExternalID: "42", ExternalURL: "https://git.invalid/c/42"})
	if err != nil || delivered.Status != DevlogDeliveryDelivered || !delivered.Reconciled || delivered.ExternalID != "42" || delivered.DeliveredAt == nil || delivered.LastError != "" {
		t.Fatalf("delivered = %+v err=%v", delivered, err)
	}
	again, claimed, err := store.BeginDevlogDelivery(begin)
	if err != nil || claimed || again.Status != DevlogDeliveryDelivered || again.Attempts != 2 {
		t.Fatalf("after delivery = %+v claimed=%v err=%v", again, claimed, err)
	}

	// A fresh store instance reads the same durable receipt.
	reopened := NewPostgresStore(db, clock)
	persisted, err := reopened.DevlogDeliveryReceipt(key)
	if err != nil || persisted.ID != first.ID || persisted.Status != DevlogDeliveryDelivered || persisted.ExternalURL != "https://git.invalid/c/42" || persisted.claimToken != "" {
		t.Fatalf("persisted = %+v err=%v", persisted, err)
	}
	receipts, err := reopened.ListDevlogDeliveryReceipts(runID)
	if err != nil || len(receipts) != 1 || receipts[0].IdempotencyKey != key {
		t.Fatalf("receipts = %+v err=%v", receipts, err)
	}

	// A stale in-flight claim can be taken over after the claim TTL.
	staleKey := devlogDeliveryIdempotencyKey("gitea-issue-comment", "owner/devlog#12", strings.Repeat("c", 64))
	staleBegin := begin
	staleBegin.IdempotencyKey, staleBegin.ArtifactSHA256 = staleKey, strings.Repeat("c", 64)
	if _, claimed, err := store.BeginDevlogDelivery(staleBegin); err != nil || !claimed {
		t.Fatalf("stale first claim = %v %v", claimed, err)
	}
	if _, _, err := store.BeginDevlogDelivery(staleBegin); !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh in-flight claim error = %v", err)
	}
	advance(devlogDeliveryClaimTTL + time.Second)
	if taken, claimed, err := store.BeginDevlogDelivery(staleBegin); err != nil || !claimed || taken.Attempts != 2 {
		t.Fatalf("stale takeover = %+v %v %v", taken, claimed, err)
	}
}
