package controlplane_test

import (
	"context"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestWorkerPoolClaimsAndExecutesQueuedRun(t *testing.T) {
	store := controlplane.NewStore(func() time.Time { return time.Unix(100, 0) })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{
		"devlog": controlplane.WorkflowExecutorFunc(func(context.Context, controlplane.Run) (controlplane.WorkflowExecution, error) {
			return controlplane.WorkflowExecution{Summary: "worker complete"}, nil
		}),
	})
	run, err := runner.Enqueue(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := controlplane.NewWorkerPool(store, runner, 1, time.Minute, 5*time.Millisecond, func() time.Time { return time.Unix(100, 0) })
	pool.Start(ctx)

	var detail controlplane.RunDetail
	for i := 0; i < 100; i++ {
		detail, err = store.RunDetail(run.ID)
		if err != nil {
			t.Fatalf("RunDetail() error = %v", err)
		}
		if detail.Run.Status == controlplane.RunSucceeded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if detail.Run.Status != controlplane.RunSucceeded || detail.Run.Summary != "worker complete" || detail.Run.ClaimedBy != "" || detail.Run.LeaseExpiresAt != nil {
		t.Fatalf("run = %+v", detail.Run)
	}
	status := pool.Status()
	if status.CompletedRuns < 1 {
		t.Fatalf("status = %+v", status)
	}
}

func TestWorkerPoolRecoversExpiredLeasesOnStart(t *testing.T) {
	now := time.Unix(100, 0)
	store := controlplane.NewStore(func() time.Time { return now })
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}
	run, err := store.CreateRun(controlplane.CreateRunInput{WorkflowID: "devlog"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.ClaimNextRun("worker-1", time.Second); err != nil {
		t.Fatalf("ClaimNextRun() error = %v", err)
	}
	pool := controlplane.NewWorkerPool(store, controlplane.NewRunner(store), 1, time.Minute, time.Hour, func() time.Time { return now.Add(2 * time.Second) })
	ctx, cancel := context.WithCancel(context.Background())
	pool.Start(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()

	detail, err := store.RunDetail(run.ID)
	if err != nil {
		t.Fatalf("RunDetail() error = %v", err)
	}
	if detail.Run.Status != controlplane.RunFailed || detail.Run.Summary != "Worker lease expired" {
		t.Fatalf("run = %+v", detail.Run)
	}
}
