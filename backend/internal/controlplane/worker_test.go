package controlplane_test

import (
	"context"
	"errors"
	"sync/atomic"
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

func TestWorkerPoolRetriesFailedRunWithBackoff(t *testing.T) {
	t.Setenv("SWITCHYARD_RUN_RETRY_BACKOFF", "1ms")
	store := controlplane.NewStore(time.Now)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{
		"devlog": controlplane.WorkflowExecutorFunc(func(context.Context, controlplane.Run) (controlplane.WorkflowExecution, error) {
			if attempts.Add(1) == 1 {
				return controlplane.WorkflowExecution{}, errors.New("transient failure")
			}
			return controlplane.WorkflowExecution{Summary: "retry complete"}, nil
		}),
	})
	run, err := runner.Enqueue(controlplane.CreateRunInput{WorkflowID: "devlog", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pool := controlplane.NewWorkerPool(store, runner, 1, time.Minute, time.Millisecond, time.Now)
	pool.Start(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		detail, detailErr := store.RunDetail(run.ID)
		if detailErr != nil {
			t.Fatal(detailErr)
		}
		if detail.Run.Status == controlplane.RunSucceeded {
			if detail.Run.AttemptCount != 2 || attempts.Load() != 2 {
				t.Fatalf("run/attempts = %+v/%d", detail.Run, attempts.Load())
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	detail, _ := store.RunDetail(run.ID)
	t.Fatalf("run did not succeed after retry: run=%+v attempts=%d events=%+v", detail.Run, attempts.Load(), detail.Events)
}

func TestWorkerPoolDrainsActiveRunAfterIntakeStops(t *testing.T) {
	store := controlplane.NewStore(time.Now)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{
		"devlog": controlplane.WorkflowExecutorFunc(func(ctx context.Context, _ controlplane.Run) (controlplane.WorkflowExecution, error) {
			close(started)
			select {
			case <-release:
				return controlplane.WorkflowExecution{Summary: "drained"}, nil
			case <-ctx.Done():
				return controlplane.WorkflowExecution{}, ctx.Err()
			}
		}),
	})
	if _, err := runner.Enqueue(controlplane.CreateRunInput{WorkflowID: "devlog"}); err != nil {
		t.Fatal(err)
	}
	intakeCtx, stopIntake := context.WithCancel(t.Context())
	pool := controlplane.NewWorkerPool(store, runner, 1, time.Minute, time.Millisecond, time.Now)
	pool.Start(intakeCtx)
	<-started
	stopIntake()
	shortCtx, shortCancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer shortCancel()
	if err := pool.Wait(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait() before release = %v", err)
	}
	close(release)
	longCtx, longCancel := context.WithTimeout(t.Context(), time.Second)
	defer longCancel()
	if err := pool.Wait(longCtx); err != nil {
		t.Fatalf("Wait() after release = %v", err)
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

type permanentExecutorError struct{}

func (permanentExecutorError) Error() string   { return "refresh provider credentials" }
func (permanentExecutorError) Retryable() bool { return false }
func TestWorkerPoolPausesPermanentExecutorFailure(t *testing.T) {
	t.Setenv("SWITCHYARD_RUN_RETRY_BACKOFF", "1ms")
	store := controlplane.NewStore(time.Now)
	if err := controlplane.SeedDefaults(store); err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	runner := controlplane.NewRunner(store, controlplane.MapExecutorRegistry{"devlog": controlplane.WorkflowExecutorFunc(func(context.Context, controlplane.Run) (controlplane.WorkflowExecution, error) {
		attempts.Add(1)
		return controlplane.WorkflowExecution{}, permanentExecutorError{}
	})})
	run, err := runner.Enqueue(controlplane.CreateRunInput{WorkflowID: "devlog", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	pool := controlplane.NewWorkerPool(store, runner, 1, time.Minute, time.Millisecond, time.Now)
	pool.Start(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		detail, err := store.RunDetail(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if detail.Run.Status == controlplane.RunPaused {
			cancel()
			if err := pool.Wait(t.Context()); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 1 || detail.Run.AttemptCount != 1 || detail.Run.LeaseExpiresAt != nil {
				t.Fatalf("attempts=%d run=%+v", attempts.Load(), detail.Run)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("permanent executor error did not pause")
}
