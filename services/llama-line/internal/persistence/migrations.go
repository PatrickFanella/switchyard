package persistence

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version  int
	name     string
	path     string
	optional bool
}

func (s *Store) Migrate(ctx context.Context) error {
	return migrate(ctx, s.pool, migrationFS, false)
}

// MigrateEmbeddings applies optional pgvector-backed embedding migrations.
// Keep this separate so basic DB logging/search works on Postgres installs
// without pgvector installed or extension privileges granted to the app role.
func (s *Store) MigrateEmbeddings(ctx context.Context) error {
	return migrate(ctx, s.pool, migrationFS, true)
}

func migrate(ctx context.Context, pool interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}, fsys fs.FS, includeOptional bool) error {
	if pool == nil {
		return errors.New("nil pool")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `create schema if not exists broker`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `create table if not exists broker.schema_migrations (version integer primary key, applied_at timestamptz not null default now())`); err != nil {
		return err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return err
	}
	migrations, err := readMigrations(fsys)
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.optional && !includeOptional {
			continue
		}
		if applied[m.version] {
			continue
		}
		if err := applyMigration(ctx, conn, fsys, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, fsys fs.FS, m migration) error {
	data, err := fs.ReadFile(fsys, m.path)
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from broker.schema_migrations where version=$1)`, m.version).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, string(data)); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx, `insert into broker.schema_migrations(version) values ($1) on conflict do nothing`, m.version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[int]bool, error) {
	rows, err := conn.Query(ctx, `select version from broker.schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

func readMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		v, err := strconv.Atoi(version)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{
			version:  v,
			name:     e.Name(),
			path:     filepath.ToSlash(filepath.Join("migrations", e.Name())),
			optional: strings.Contains(e.Name(), "embedding"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
