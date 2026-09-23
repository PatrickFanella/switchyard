package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreparedDependenciesRejectDriftAndMissingInputs(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "lock")
	if err := os.WriteFile(p, []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned")))
	files := map[string]*string{"lock": &hash, "absent": nil}
	if err := validatePreparedFiles(root, files); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedFiles(root, files); err == nil {
		t.Fatal("accepted stale dependencies")
	}
	_ = os.Remove(p)
	if err := validatePreparedFiles(root, files); err == nil {
		t.Fatal("accepted missing input")
	}
	_ = os.WriteFile(p, []byte("pinned"), 0600)
	_ = os.WriteFile(filepath.Join(root, "absent"), []byte("new"), 0600)
	if err := validatePreparedFiles(root, files); err == nil {
		t.Fatal("accepted newly introduced configuration")
	}
	if err := validatePreparedFiles(root, map[string]*string{"../escape": &hash}); err == nil {
		t.Fatal("accepted escaping path")
	}
}

func TestPreparedProfileReusesDependenciesAcrossSourceRevisions(t *testing.T) {
	root := t.TempDir()
	inputs := []string{"backend/go.mod", "backend/go.sum", "web/package.json", "web/pnpm-lock.yaml", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc", "web/.npmrc", "go.work", "go.work.sum"}
	files := map[string]*string{}
	for _, name := range inputs {
		files[name] = nil
	}
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0700); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned")))
	files["backend/go.mod"] = &hash
	if err := os.WriteFile(filepath.Join(root, "backend/go.mod"), []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	m := preparedManifest{Version: 2, Profile: "go1.26.6-node24-pnpm10.33.0-v2", BaseSHA: strings.Repeat("a", 40), Files: files, Commands: [][]string{{"make", "verify"}}}
	head := strings.Repeat("b", 40)
	if err := validatePreparedManifest(m, head, root, []string{"make", "verify"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(m, head, root, []string{"make", "verify"}); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(m, head, root, []string{"sh"}); err == nil {
		t.Fatal("accepted unsupported command")
	}
	m.Version = 1
	m.Profile = "go1.26.6-node24-pnpm10.33.0-v1"
	if err := validatePreparedManifest(m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("relaxed legacy image binding")
	}
	m.Version = 2
	m.Profile = "go1.26.6-node24-pnpm10.33.0-v2"
	delete(m.Files, ".npmrc")
	m.Files["unrelated"] = nil
	if err := validatePreparedManifest(m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("accepted incomplete dependency contract")
	}
	delete(m.Files, "unrelated")
	m.Files[".npmrc"] = nil
	if err := os.WriteFile(filepath.Join(root, "backend/go.mod"), []byte("new dependency"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("accepted dependency change across revisions")
	}
}
