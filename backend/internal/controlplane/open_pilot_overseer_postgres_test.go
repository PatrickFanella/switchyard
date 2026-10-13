package controlplane

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestPostgresOverseerPersistenceOwnershipAndInterruptedIntent(t *testing.T) {
	if os.Getenv("SWITCHYARD_TEST_DATABASE_URL") == "" || os.Getenv("SWITCHYARD_TEST_POSTGRES_ISOLATED") != "1" {
		t.Skip("run make postgres-test against its disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := OpenPostgres(ctx, os.Getenv("SWITCHYARD_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(db, nil)
	repo := "owner/overseer-requeue-" + newID("test")
	defer func() {
		_, _ = db.ExecContext(context.Background(), `delete from open_pilot_issue_states where repo=$1`, repo)
		_, _ = db.ExecContext(context.Background(), `delete from runs where inputs->>'repo'=$1`, repo)
	}()
	assertOverseerFencedRequeue(t, store, repo)
	p := newOverseerPersistence(store)
	initial, err := p.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		current, e := p.load(context.Background())
		if e == nil {
			initial.Revision = current.Revision
			_ = p.save(context.Background(), &initial)
		}
	}()
	state := cloneOverseerState(initial)
	state.Config.Enabled = true
	state.Config.MaxOpenLanes = 3
	if err := p.save(ctx, &state); err != nil {
		t.Fatal(err)
	}
	stale := cloneOverseerState(initial)
	if err := p.save(ctx, &stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write accepted", err)
	}
	replacement := newOverseerPersistence(NewPostgresStore(db, nil))
	read, err := replacement.load(ctx)
	if err != nil || !read.Config.Enabled {
		t.Fatal("configuration did not survive replacement", err)
	}
	err = p.cycle(ctx, func(cycleCtx context.Context) error {
		if err := guardOverseerCycle(cycleCtx); err != nil {
			return err
		}
		if err := replacement.cycle(ctx, func(context.Context) error { return nil }); !errors.Is(err, ErrConflict) {
			return errors.New("second controller acquired live ownership")
		}
		current, err := p.load(cycleCtx)
		if err != nil {
			return err
		}
		current.Pending = &OpenPilotOverseerAction{ID: "test-interrupted", Kind: "queue_label", Repo: "owner/repo", Issue: 1, CreatedAt: time.Now().UTC(), Outcome: "reserved"}
		if err := p.save(cycleCtx, &current); err != nil {
			return err
		}
		var pid int
		if err := db.QueryRowContext(ctx, `select pid from pg_locks where locktype='advisory' and classid=$1 and objid=$2 and objsubid=1 and granted`, overseerLockID>>32, overseerLockID&0xffffffff).Scan(&pid); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, `select pg_terminate_backend($1)`, pid); err != nil {
			return err
		}
		select {
		case <-cycleCtx.Done():
			return cycleCtx.Err()
		case <-time.After(4 * time.Second):
			return errors.New("ownership loss did not cancel cycle")
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cycle after ownership loss = %v", err)
	}
	read, err = replacement.load(ctx)
	if err != nil || read.Pending == nil || read.Pending.ID != "test-interrupted" {
		t.Fatal("durable intent lost after connection failure", err)
	}
	if err := replacement.cycle(ctx, func(c context.Context) error { return guardOverseerCycle(c) }); err != nil {
		t.Fatal("new owner could not acquire released lock", err)
	}
}
