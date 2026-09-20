package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
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
