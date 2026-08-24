package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPostgresStoreWebhookAdmissionConcurrencyReplayAndRevisionIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL webhook integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `delete from webhook_deliveries; delete from webhook_trigger_auth_keys; delete from webhook_trigger_auth; update webhook_auth_config set owner='',generation=0,digest='',receipt_retention_seconds=2592000 where singleton=true; update webhook_receipt_metrics set receipts_pruned=0 where singleton=true`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, nil)
	suffix := newID("postgres-webhook")
	workflowID, triggerID := suffix+"-workflow", suffix+"-trigger"
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: WorkflowChangemaker, Name: "Postgres webhook", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertWorkflowDefinition(WorkflowDefinition{ID: workflowID, Name: "Postgres webhook", Status: "active", InputShape: JSONShape{Type: "object", Properties: map[string]JSONShapeProperty{"static": {Type: "string"}, "repo": {Type: "string"}}, Required: []string{"static", "repo"}}, Pipeline: PipelineDefinition{ID: workflowID + "-pipeline", WorkflowID: workflowID, Name: "Postgres webhook", Steps: []PipelineStepDefinition{{ID: "step", Position: 1, Name: "webhook.step", Executor: "internal"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertTrigger(Trigger{ID: triggerID, WorkflowID: workflowID, Type: TriggerWebhook, Name: "Postgres webhook", Enabled: true, Inputs: map[string]string{"static": "value"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	authConfig := WebhookAuthConfig{Owner: suffix, Generation: 1, Digest: fmt.Sprintf("%064x", 1), ReceiptRetention: 30 * 24 * time.Hour, Triggers: []WebhookAuthMetadata{{TriggerID: triggerID, Revision: "revision-1", Keys: []WebhookAuthKey{{ID: "key-1", NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}}}}}
	if err := store.ReplaceWebhookAuth(ctx, authConfig); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceWebhookAuth(ctx, authConfig); err != nil {
		t.Fatalf("idempotent config replacement: %v", err)
	}
	conflictingConfig := authConfig
	conflictingConfig.Digest = strings.Repeat("f", 64)
	if err := store.ReplaceWebhookAuth(ctx, conflictingConfig); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-generation config error=%v", err)
	}
	input := AdmitWebhookInput{TriggerID: triggerID, DeliveryID: "delivery-1", BodySHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AuthGeneration: 1, AuthRevision: "revision-1", KeyID: "key-1", Inputs: map[string]string{"repo": "owner/repo"}, InputTypes: map[string]string{"repo": "string"}}
	const callers = 16
	var wg sync.WaitGroup
	results := make(chan WebhookAdmission, callers)
	errorsCh := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admission, err := store.AdmitWebhook(input)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- admission
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("concurrent admission: %v", err)
	}
	unique, fresh := map[string]bool{}, 0
	for result := range results {
		unique[result.Run.ID] = true
		if !result.Replayed {
			fresh++
		}
		if result.Run.PipelineSnapshot == nil || result.Run.Inputs["static"] != "value" {
			t.Errorf("run snapshot/inputs = %+v", result.Run)
		}
	}
	if len(unique) != 1 || fresh != 1 {
		t.Fatalf("unique=%v fresh=%d", unique, fresh)
	}
	var originalRunID string
	for id := range unique {
		originalRunID = id
	}
	if _, err := store.FinishRun(originalRunID, RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneRetention(time.Now().UTC().Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	retainedReplay, err := store.AdmitWebhook(input)
	if err != nil || !retainedReplay.Replayed || retainedReplay.Run.ID != originalRunID {
		t.Fatalf("post-retention replay=%+v err=%v", retainedReplay, err)
	}
	changed := input
	changed.BodySHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := store.AdmitWebhook(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed digest error=%v", err)
	}
	authConfig.Generation, authConfig.Digest, authConfig.Triggers[0].Revision = 2, fmt.Sprintf("%064x", 2), "revision-2"
	if err := store.ReplaceWebhookAuth(ctx, authConfig); err != nil {
		t.Fatal(err)
	}
	staleGenerationInput := input
	staleGenerationInput.DeliveryID, staleGenerationInput.AuthRevision = "stale-generation", "revision-2"
	if _, err := store.AdmitWebhook(staleGenerationInput); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale dispatcher generation error=%v", err)
	}
	if err := store.ReplaceWebhookAuth(ctx, WebhookAuthConfig{Owner: suffix + "-foreign", Generation: 3, Digest: fmt.Sprintf("%064x", 3), ReceiptRetention: 30 * 24 * time.Hour, Triggers: authConfig.Triggers}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign config owner error=%v", err)
	}
	staleConfig := authConfig
	staleConfig.Generation, staleConfig.Digest = 1, fmt.Sprintf("%064x", 1)
	if err := store.ReplaceWebhookAuth(ctx, staleConfig); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale config error=%v", err)
	}
	input.DeliveryID = "revision-race"
	if _, err := store.AdmitWebhook(input); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision error=%v", err)
	}
	triggerLiteral := strings.ReplaceAll(triggerID, "'", "''")
	if _, err := db.ExecContext(ctx, `
		create or replace function switchyard_test_reject_webhook_event() returns trigger language plpgsql as $$
		begin
		  if new.type='webhook.accepted' and exists(select 1 from runs where id=new.run_id and trigger_id=TG_ARGV[0]) then
		    raise exception 'forced webhook event failure';
		  end if;
		  return new;
		end $$;
		drop trigger if exists switchyard_test_reject_webhook_event on run_events;
		create trigger switchyard_test_reject_webhook_event before insert on run_events for each row execute function switchyard_test_reject_webhook_event('`+triggerLiteral+`')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `drop trigger if exists switchyard_test_reject_webhook_event on run_events; drop function if exists switchyard_test_reject_webhook_event()`)
	})
	rollbackInput := input
	rollbackInput.DeliveryID = "forced-rollback"
	rollbackInput.AuthGeneration = 2
	rollbackInput.AuthRevision = "revision-2"
	if _, err := store.AdmitWebhook(rollbackInput); err == nil || !strings.Contains(fmt.Sprint(err), "forced webhook event failure") {
		t.Fatalf("forced rollback error=%v", err)
	}
	var receipts, runs int
	if err := db.QueryRowContext(ctx, `select count(*) from webhook_deliveries where trigger_id=$1`, triggerID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from runs where trigger_id=$1`, triggerID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || runs != 0 {
		t.Fatalf("rollback receipts=%d runs=%d", receipts, runs)
	}
	if _, err := db.ExecContext(ctx, `drop trigger switchyard_test_reject_webhook_event on run_events`); err != nil {
		t.Fatal(err)
	}

	expiryInput := input
	expiryInput.AuthGeneration, expiryInput.AuthRevision, expiryInput.DeliveryID = 2, "revision-2", "expiry-replay"
	firstExpiry, err := store.AdmitWebhook(expiryInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update webhook_deliveries set accepted_at=clock_timestamp()-interval '31 days' where trigger_id=$1 and delivery_id=$2`, triggerID, expiryInput.DeliveryID); err != nil {
		t.Fatal(err)
	}
	afterExpiry, err := store.AdmitWebhook(expiryInput)
	if err != nil || afterExpiry.Replayed || afterExpiry.Run.ID == firstExpiry.Run.ID {
		t.Fatalf("expired receipt admission=%+v err=%v", afterExpiry, err)
	}
	pruneInput := expiryInput
	pruneInput.DeliveryID = "expiry-prune"
	if _, err := store.AdmitWebhook(pruneInput); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update webhook_deliveries set accepted_at=clock_timestamp()-interval '31 days' where trigger_id=$1 and delivery_id=$2`, triggerID, pruneInput.DeliveryID); err != nil {
		t.Fatal(err)
	}
	pruned, err := store.PruneRetention(time.Now().UTC().Add(-time.Hour))
	if err != nil || pruned.WebhookReceipts < 1 {
		t.Fatalf("receipt prune=%+v err=%v", pruned, err)
	}
	postPrune, err := store.AdmitWebhook(pruneInput)
	if err != nil || postPrune.Replayed {
		t.Fatalf("post-prune replay=%+v err=%v", postPrune, err)
	}

	omittedTriggerID := suffix + "-omitted-trigger"
	if err := store.UpsertTrigger(Trigger{ID: omittedTriggerID, WorkflowID: workflowID, Type: TriggerWebhook, Name: "Omitted webhook", Enabled: true, Inputs: map[string]string{"static": "value"}}); err != nil {
		t.Fatal(err)
	}
	fullConfig := authConfig
	fullConfig.Generation, fullConfig.Digest = 3, fmt.Sprintf("%064x", 3)
	omittedMetadata := fullConfig.Triggers[0]
	omittedMetadata.TriggerID = omittedTriggerID
	fullConfig.Triggers = append(append([]WebhookAuthMetadata(nil), fullConfig.Triggers...), omittedMetadata)
	if err := store.ReplaceWebhookAuth(ctx, fullConfig); err != nil {
		t.Fatal(err)
	}
	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockTx.ExecContext(ctx, `select id from triggers where id=$1 for update`, omittedTriggerID); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	replacement := authConfig
	replacement.Generation, replacement.Digest = 4, fmt.Sprintf("%064x", 4)
	replaced := make(chan error, 1)
	go func() { replaced <- store.ReplaceWebhookAuth(ctx, replacement) }()
	select {
	case err := <-replaced:
		_ = lockTx.Rollback()
		t.Fatalf("replacement did not lock omitted trigger: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := lockTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-replaced; err != nil {
		t.Fatal(err)
	}
	var omittedAuth int
	if err := db.QueryRowContext(ctx, `select count(*) from webhook_trigger_auth where trigger_id=$1`, omittedTriggerID).Scan(&omittedAuth); err != nil {
		t.Fatal(err)
	}
	if omittedAuth != 0 {
		t.Fatalf("omitted trigger auth rows=%d", omittedAuth)
	}
	if err := store.RevokeWebhookAuth(ctx, replacement.Owner, replacement.Generation-1, replacement.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitWebhook(AdmitWebhookInput{TriggerID: triggerID, DeliveryID: "before-revoke", BodySHA256: strings.Repeat("c", 64), AuthGeneration: 4, AuthRevision: "revision-2", KeyID: "key-1", Inputs: map[string]string{"repo": "owner/repo"}, InputTypes: map[string]string{"repo": "string"}}); err != nil {
		t.Fatalf("mismatched revocation changed active auth: %v", err)
	}
	if err := store.RevokeWebhookAuth(ctx, replacement.Owner, replacement.Generation, replacement.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AdmitWebhook(AdmitWebhookInput{TriggerID: triggerID, DeliveryID: "after-revoke", BodySHA256: strings.Repeat("d", 64), AuthGeneration: 4, AuthRevision: "revision-2", KeyID: "key-1", Inputs: map[string]string{"repo": "owner/repo"}, InputTypes: map[string]string{"repo": "string"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("revoked generation admission error=%v", err)
	}
	if err := store.ReplaceWebhookAuth(ctx, replacement); err != nil {
		t.Fatalf("same-generation restart after revocation: %v", err)
	}
	dispatcher := &WebhookDispatcher{store: store, authConfig: replacement}
	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "replace", call: dispatcher.Activate},
		{name: "revoke", call: dispatcher.Deactivate},
	} {
		t.Run("blocked lifecycle "+test.name+" honors deadline", func(t *testing.T) {
			lockTx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := lockTx.ExecContext(ctx, `select singleton from webhook_auth_config where singleton=true for update`); err != nil {
				_ = lockTx.Rollback()
				t.Fatal(err)
			}
			operationCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			started := time.Now()
			err = test.call(operationCtx)
			cancel()
			if rollbackErr := lockTx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
				t.Fatalf("blocked %s error=%v duration=%s", test.name, err, time.Since(started))
			}
		})
	}
}
