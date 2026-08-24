package controlplane

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
)

const modelServiceSingletonLockKey int64 = 7_031_270_002

var ErrModelServiceAlreadyRunning = errors.New("postgres model service singleton already held")

var ErrModelServiceFenced = errors.New("model service singleton fence lost")

type modelServiceSingleton interface {
	HealthError(context.Context) error
	Close(context.Context) error
	Invalidate() error
}

func (l *postgresModelServiceSingleton) Invalidate() error {
	var closeErr error
	l.once.Do(func() {
		if err := l.conn.Raw(func(any) error { return driver.ErrBadConn }); !closedPostgresSessionError(err) {
			closeErr = err
		}
		if err := l.conn.Close(); !closedPostgresSessionError(err) {
			closeErr = errors.Join(closeErr, err)
		}
	})
	return closeErr
}

type postgresModelServiceSingleton struct {
	conn *sql.Conn
	pid  int
	once sync.Once
}

func (s *PostgresStore) acquireModelServiceSingleton(ctx context.Context) (*postgresModelServiceSingleton, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire model service postgres session: %w", err)
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `select pg_try_advisory_lock($1)`, modelServiceSingletonLockKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("acquire model service singleton: %w", err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, ErrModelServiceAlreadyRunning
	}
	var pid int
	if err := conn.QueryRowContext(ctx, `select pg_backend_pid()`).Scan(&pid); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("read model service singleton session identity: %w", err)
	}
	return &postgresModelServiceSingleton{conn: conn, pid: pid}, nil
}

func (l *postgresModelServiceSingleton) Close(ctx context.Context) error {
	var closeErr error
	l.once.Do(func() {
		var unlocked bool
		if err := l.conn.QueryRowContext(ctx, `select pg_advisory_unlock($1)`, modelServiceSingletonLockKey).Scan(&unlocked); err != nil {
			if !closedPostgresSessionError(err) {
				closeErr = err
			}
			_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		if err := l.conn.Close(); !closedPostgresSessionError(err) {
			closeErr = errors.Join(closeErr, err)
		}
	})
	return closeErr
}

func closedPostgresSessionError(err error) bool {
	return err == nil || errors.Is(err, sql.ErrConnDone) || errors.Is(err, driver.ErrBadConn)
}

func (l *postgresModelServiceSingleton) HealthError(ctx context.Context) error {
	var held bool
	err := l.conn.QueryRowContext(ctx, `select exists(
		select 1 from pg_locks
		where locktype='advisory' and pid=pg_backend_pid() and objsubid=1
		  and ((classid::bigint << 32) | objid::bigint)=$1
	)`, modelServiceSingletonLockKey).Scan(&held)
	if err != nil {
		return fmt.Errorf("check model service singleton session: %w", err)
	}
	if !held {
		return errors.New("model service singleton lock is no longer held")
	}
	return nil
}
