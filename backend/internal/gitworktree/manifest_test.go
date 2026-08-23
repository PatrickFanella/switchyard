package gitworktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestTracksStagedBytesPathsAndExecutableMode(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	baseline, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	nonExecutable, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if nonExecutable != baseline {
		t.Fatal("non-executable permission bits changed manifest")
	}
	if err := os.Chmod(file, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if executable == baseline {
		t.Fatal("executable mode did not change manifest")
	}
	if err := os.Rename(file, filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	renamed, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if renamed == executable {
		t.Fatal("path did not change manifest")
	}
	if err := os.WriteFile(filepath.Join(root, "renamed.txt"), []byte("two\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	changed, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if changed == renamed {
		t.Fatal("content did not change manifest")
	}
}

func TestManifestExcludesRootGitMetadataFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	before, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := Manifest(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("root .git metadata changed manifest")
	}
}

func TestManifestBoundsDirectoryEntries(t *testing.T) {
	root := t.TempDir()
	for index := range 3 {
		if err := os.Mkdir(filepath.Join(root, string(rune('a'+index))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, err = ManifestBounded(context.Background(), directory, ManifestLimits{MaxFiles: 10, MaxEntries: 2, MaxBytes: 1024})
	if err == nil {
		t.Fatal("directory entry budget was not enforced")
	}
}
