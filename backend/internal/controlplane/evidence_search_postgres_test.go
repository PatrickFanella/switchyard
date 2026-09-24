package controlplane

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestPostgresEvidenceSearchIntegration proves the PostgreSQL search contract
// matches the memory store: retention floor, scopes, payload exclusion, LIKE
// escaping, and stable keyset pagination.
func TestPostgresEvidenceSearchIntegration(t *testing.T) {
	databaseURL := os.Getenv("SWITCHYARD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set SWITCHYARD_TEST_DATABASE_URL to run PostgreSQL evidence search integration test")
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
	now := time.Now().UTC().Truncate(time.Microsecond)
	store := NewPostgresStore(db, func() time.Time { return now })
	workflowID := newID("evidence-search")
	marker := newID("needle")
	if err := store.UpsertWorkflow(Workflow{ID: workflowID, Kind: WorkflowDevlog, Name: "Evidence search", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	requestIDs := []string{newID("req"), newID("req")}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = db.ExecContext(cleanupCtx, `delete from runs where workflow_id=$1`, workflowID)
		_, _ = db.ExecContext(cleanupCtx, `delete from workflows where id=$1`, workflowID)
		_, _ = db.ExecContext(cleanupCtx, `delete from model_broker_requests where id = any($1)`, requestIDs)
	})
	floor := now.Add(-24 * time.Hour)

	now = floor.Add(-time.Hour)
	oldRun, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(oldRun.ID, RunFailed, marker+" ancient"); err != nil {
		t.Fatal(err)
	}

	now = floor.Add(time.Hour)
	okRun, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID, Inputs: map[string]string{"secret": marker + "-input"}})
	if err != nil {
		t.Fatal(err)
	}
	okStep, err := store.CreateRunStep(CreateRunStepInput{RunID: okRun.ID, Position: 1, Name: "devlog.generate", Executor: "model", Inputs: map[string]string{"prompt": marker + "-step-input"}, CorrelationIDs: map[string]string{"trace_id": marker + "-trace"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRunStep(okStep.ID, StepSucceeded, marker+" 100%_done", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(okRun.ID, RunSucceeded, marker+" published"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, _, err := store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: requestIDs[0], Model: "qwen", Path: "/v1/chat/completions", Client: "cli", RequestBody: `{"content":"` + marker + `-body"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishModelBrokerRequest(requestIDs[0], FinishModelBrokerRequestInput{Status: ModelBrokerRequestCompleted, ResponseBody: `{"content":"` + marker + `-response"}`, CorrelationIDs: map[string]string{"trace_id": marker + "-trace", "run_id": okRun.ID}}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	failRun, err := store.CreateRun(CreateRunInput{WorkflowID: workflowID})
	if err != nil {
		t.Fatal(err)
	}
	failStep, err := store.CreateRunStep(CreateRunStepInput{RunID: failRun.ID, Position: 1, Name: "devlog.generate", Executor: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRunStep(failStep.ID, StepFailed, "", marker+" refused token=abc123secretvalue", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(failRun.ID, RunFailed, marker+" failed"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, _, err := store.BeginModelBrokerRequest(CreateModelBrokerRequestInput{ID: requestIDs[1], Model: "llama", Path: "/api/generate", Client: "cli"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishModelBrokerRequest(requestIDs[1], FinishModelBrokerRequestInput{Status: ModelBrokerRequestFailed, Error: marker + " upstream unavailable"}); err != nil {
		t.Fatal(err)
	}

	search := func(input EvidenceSearchInput) EvidenceSearchPage {
		t.Helper()
		if input.RetainedSince.IsZero() {
			input.RetainedSince = floor
		}
		if input.Query == "" {
			input.Query = marker
		}
		page, err := store.SearchEvidence(ctx, input)
		if err != nil {
			t.Fatalf("SearchEvidence(%+v) error = %v", input, err)
		}
		return page
	}
	// The PostgreSQL model-request writer stamps created_at from the database
	// clock, so cross-kind order is asserted through the ordering invariant
	// rather than a fixed sequence; membership is compared as sorted sets.
	assertKeys := func(name string, got []EvidenceHit, want []string) {
		t.Helper()
		gotKeys := hitKeys(got)
		sort.Strings(gotKeys)
		wantKeys := append([]string(nil), want...)
		sort.Strings(wantKeys)
		if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
			t.Fatalf("%s hits = %v, want %v", name, hitKeys(got), want)
		}
		for i := 1; i < len(got); i++ {
			if !evidenceHitNewer(got[i-1], got[i]) {
				t.Fatalf("%s hits out of order at %d: %+v then %+v", name, i, got[i-1], got[i])
			}
		}
	}
	all := search(EvidenceSearchInput{})
	assertKeys("all", all.Hits, []string{"request:" + requestIDs[1], "step:" + failStep.ID, "run:" + failRun.ID, "request:" + requestIDs[0], "step:" + okStep.ID, "run:" + okRun.ID})
	for _, hit := range all.Hits {
		if hit.Kind == EvidenceKindStep && hit.WorkflowID != workflowID {
			t.Fatalf("step hit lacks workflow: %+v", hit)
		}
	}
	assertKeys("retained-out", search(EvidenceSearchInput{Query: marker + " ancient"}).Hits, nil)
	assertKeys("widened", search(EvidenceSearchInput{Query: marker + " ancient", RetainedSince: floor.Add(-2 * time.Hour)}).Hits, []string{"run:" + oldRun.ID})
	assertKeys("errors", search(EvidenceSearchInput{Scope: EvidenceScopeErrors}).Hits, []string{"request:" + requestIDs[1], "step:" + failStep.ID, "run:" + failRun.ID})
	assertKeys("correlations", search(EvidenceSearchInput{Scope: EvidenceScopeCorrelations, Query: marker + "-trace"}).Hits, []string{"request:" + requestIDs[0], "step:" + okStep.ID})
	assertKeys("run filter", search(EvidenceSearchInput{RunID: okRun.ID}).Hits, []string{"request:" + requestIDs[0], "step:" + okStep.ID, "run:" + okRun.ID})
	assertKeys("status filter", search(EvidenceSearchInput{Status: "failed"}).Hits, []string{"request:" + requestIDs[1], "step:" + failStep.ID, "run:" + failRun.ID})
	assertKeys("like escaping", search(EvidenceSearchInput{Query: "100%_done"}).Hits, []string{"step:" + okStep.ID})
	assertKeys("like wildcard is literal", search(EvidenceSearchInput{Query: "100%_dune"}).Hits, nil)
	for _, needle := range []string{marker + "-input", marker + "-step-input", marker + "-body", marker + "-response"} {
		assertKeys("payload "+needle, search(EvidenceSearchInput{Query: needle}).Hits, nil)
	}

	var walked []EvidenceHit
	input := EvidenceSearchInput{Query: marker, Limit: 4, RetainedSince: floor}
	for {
		page, err := store.SearchEvidence(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, page.Hits...)
		if !page.HasMore {
			break
		}
		last := page.Hits[len(page.Hits)-1]
		input.BeforeCreated, input.BeforeKind, input.BeforeID = &last.CreatedAt, last.Kind, last.ID
	}
	if strings.Join(hitKeys(walked), ",") != strings.Join(hitKeys(all.Hits), ",") {
		t.Fatalf("paginated walk %v != full listing %v", hitKeys(walked), hitKeys(all.Hits))
	}
}
