package persistence

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

func TestReadMigrations(t *testing.T) {
	migs, err := readMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) < 1 {
		t.Fatalf("got %d migrations", len(migs))
	}
	if migs[0].version != 1 {
		t.Fatalf("got version %d", migs[0].version)
	}
	foundOptional := false
	for _, m := range migs {
		if m.name == "002_embeddings.sql" && m.optional {
			foundOptional = true
		}
	}
	if !foundOptional {
		t.Fatal("expected embeddings migration to be marked optional")
	}
}

func TestBaseMigrationDoesNotRequireVectorExtension(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/001_init_persistence.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	if strings.Contains(sql, "extension if not exists vector") || strings.Contains(sql, " vector(") || strings.Contains(sql, "using hnsw") {
		t.Fatal("base persistence migration must not require pgvector; embeddings are optional")
	}
}

func TestEmbeddingMigrationOwnsVectorExtension(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/002_embeddings.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, want := range []string{"create extension if not exists vector", "broker.request_embedding", "using hnsw"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("embedding migration missing %q", want)
		}
	}
}

func TestRequestEventSearchUsesImmutableBodyText(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/001_init_persistence.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, want := range []string{"coalesce(request_body_text,'')", "coalesce(response_body_text,'')"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("request search_tsv missing %q", want)
		}
	}
	for _, bad := range []string{"request_body::text", "response_body::text"} {
		if strings.Contains(sql, bad) {
			t.Fatalf("request search_tsv must not use non-immutable jsonb cast %q", bad)
		}
	}
	if strings.Contains(sql, "array_to_string") {
		t.Fatal("request search_tsv must not use non-immutable array_to_string")
	}
}

func TestRequestEventSearchUpgradeMigration(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/003_request_search_body_json.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, want := range []string{
		"drop column if exists search_tsv",
		"coalesce(request_body_text,'')",
		"coalesce(response_body_text,'')",
		"create index if not exists request_event_search_idx",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("search upgrade migration missing %q", want)
		}
	}
	for _, bad := range []string{"request_body::text", "response_body::text"} {
		if strings.Contains(sql, bad) {
			t.Fatalf("search upgrade migration must not use non-immutable jsonb cast %q", bad)
		}
	}
	if strings.Contains(sql, "array_to_string") {
		t.Fatal("search upgrade migration must not use non-immutable array_to_string")
	}
}

func TestTokenCountsMigrationAddsColumns(t *testing.T) {
	data, err := migrationFS.ReadFile("migrations/004_token_counts.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(data))
	for _, want := range []string{"add column if not exists prompt_tokens", "add column if not exists completion_tokens", "alter table if exists broker.request_event"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("token migration missing %q", want)
		}
	}
}

func TestMigrateNilPool(t *testing.T) {
	if err := migrate(context.Background(), nil, fstest.MapFS{}, false); err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreValidate(t *testing.T) {
	if err := (Config{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Database: config.DatabaseConfig{Enabled: true}}).Validate(); err == nil {
		t.Fatal("expected error")
	}
}

func TestOpenDisabled(t *testing.T) {
	store, err := Open(context.Background(), config.DatabaseConfig{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if store != nil {
		t.Fatal("expected nil store")
	}
}
