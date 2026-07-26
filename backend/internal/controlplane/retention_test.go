package controlplane_test

import (
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestRetentionPrunesOnlyExpiredTerminalRecords(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	old, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(old.ID, controlplane.RunSucceeded, "done"); err != nil {
		t.Fatal(err)
	}
	active, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatal(err)
	}
	request, err := store.CreateModelBrokerRequest(controlplane.CreateModelBrokerRequestInput{ID: "old-model", Model: "test", RequestBody: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishModelBrokerRequest(request.ID, controlplane.FinishModelBrokerRequestInput{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	result, err := store.PruneRetention(now.Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Runs != 1 || result.ModelRequests != 1 {
		t.Fatalf("result = %+v", result)
	}
	if _, err := store.RunDetail(old.ID); err != controlplane.ErrNotFound {
		t.Fatalf("old run error = %v", err)
	}
	if _, err := store.RunDetail(active.ID); err != nil {
		t.Fatalf("active run was pruned: %v", err)
	}
}
