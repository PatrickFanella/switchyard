package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestOpenWorkdirAcceptsDirectWorktree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stable /proc directory descriptors require Linux")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	workdir := filepath.Join(root, "repo", "run")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := openWorkdir(root, workdir)
	if err != nil {
		t.Fatal(err)
	}
	directory.Close()
}

func TestOpenWorkdirRejectsUnsafePaths(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("openat no-follow traversal requires Linux")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root")
	inside := filepath.Join(root, "repo", "run")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		root    string
		workdir string
		want    string
	}{
		{name: "missing root", workdir: inside, want: "must be an absolute path"},
		{name: "relative root", root: "root", workdir: inside, want: "must be an absolute path"},
		{name: "relative workdir", root: root, workdir: "run", want: "workdir must be an absolute path"},
		{name: "filesystem root", root: string(filepath.Separator), workdir: inside, want: "must not be filesystem root"},
		{name: "equal root", root: root, workdir: root, want: "must be an Open Pilot worktree"},
		{name: "outside root", root: root, workdir: outside, want: "must be an Open Pilot worktree"},
		{name: "symlink escape", root: root, workdir: escape, want: "symlink component"},
		{name: "missing workdir", root: root, workdir: filepath.Join(root, "missing"), want: "invalid Open Pilot worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory, err := openWorkdir(test.root, test.workdir)
			if directory != nil {
				directory.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOpenedWorkdirSurvivesPathReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("stable /proc directory descriptors require Linux")
	}
	root := filepath.Join(t.TempDir(), "root")
	workdir := filepath.Join(root, "repo", "run")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "identity"), []byte("opened-original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := openWorkdir(root, workdir)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	moved := workdir + "-moved"
	if err := os.Rename(workdir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "identity"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(context.Background(), "/bin/sh", "-c", `cd /proc/self/fd/3 && exec cat identity`)
	command.ExtraFiles = []*os.File{directory}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("command: %v: %s", err, output)
	}
	if string(output) != "opened-original\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestVerifyAcceptsRegisteredLinkedWorktree(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	t.Setenv("OPEN_PILOT_WORKTREE_ROOT", root)
	t.Setenv("OPEN_PILOT_GIT_COMMON_DIR", filepath.Join(repo, ".git"))

	response := callVerify(t, verifyRequest{Command: []string{"git", "diff", "--stat"}, Workdir: workdir, Timeout: "5s"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestVerifyRejectsFakeAndUnregisteredGitMetadata(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string, string)
		want   string
	}{
		{
			name: "fake git file",
			mutate: func(t *testing.T, _, _, workdir string) {
				if err := os.WriteFile(filepath.Join(workdir, ".git"), []byte("gitdir: /tmp/fake\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "linked Git",
		},
		{
			name: "unregistered copy",
			mutate: func(t *testing.T, _, root, workdir string) {
				copyPath := filepath.Join(root, "copy")
				if err := os.Mkdir(copyPath, 0o755); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(filepath.Join(workdir, ".git"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(copyPath, ".git"), contents, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "backlink does not match",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, root, workdir := verifierTestWorktree(t)
			test.mutate(t, repo, root, workdir)
			requestWorkdir := workdir
			if test.name == "unregistered copy" {
				requestWorkdir = filepath.Join(root, "copy")
			}
			t.Setenv("OPEN_PILOT_WORKTREE_ROOT", root)
			t.Setenv("OPEN_PILOT_GIT_COMMON_DIR", filepath.Join(repo, ".git"))
			response := callVerify(t, verifyRequest{Command: []string{"true"}, Workdir: requestWorkdir, Timeout: "5s"})
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNonGitSnapshotEnforcesLargeDirectoryFileLimit(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	for index := range snapshotReadBatch * 3 {
		name := filepath.Join(workdir, fmt.Sprintf("entry-%04d", index))
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler := newVerifyHandler(verifierConfig{
		worktreeRoot: root, gitCommonDir: filepath.Join(repo, ".git"), outputBytes: 1 << 20,
		snapshotFiles: snapshotReadBatch + 1, snapshotBytes: 1 << 20, limiter: make(chan struct{}, 1),
	})
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"true"}, Workdir: workdir, Timeout: "5s"})
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "entry limit") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func callVerify(t *testing.T, request verifyRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	verify(recorder, httptest.NewRequest(http.MethodPost, "/verify", bytes.NewReader(body)))
	return recorder
}

func callVerifyWithContext(t *testing.T, handler http.Handler, ctx context.Context, request verifyRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/verify", bytes.NewReader(body)).WithContext(ctx))
	return recorder
}

func testVerifierHandler(root, common string, concurrency, outputBytes int) http.Handler {
	return newVerifyHandler(verifierConfig{
		worktreeRoot:  root,
		gitCommonDir:  common,
		outputBytes:   outputBytes,
		snapshotFiles: 1_000,
		snapshotBytes: 16 << 20,
		limiter:       make(chan struct{}, concurrency),
	})
}

func TestNonGitCommandUsesWritableSnapshotAndLeavesWorktreeUnchanged(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 2, 1<<20)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{
		Command: []string{"sh", "-c", "printf changed > README.md; printf new > generated.txt; test -z \"${GIT_DIR:-}${GIT_COMMON_DIR:-}${GIT_WORK_TREE:-}\""},
		Workdir: workdir,
		Timeout: "5s",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	contents, err := os.ReadFile(filepath.Join(workdir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "test\n" {
		t.Fatalf("source changed: %q", contents)
	}
	if _, err := os.Stat(filepath.Join(workdir, "generated.txt")); !os.IsNotExist(err) {
		t.Fatalf("generated file escaped snapshot: %v", err)
	}
}

func TestVerifierRejectsOriginalWorktreeMutationDuringSnapshotCommand(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 1, 1<<20)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{
		Command: []string{"sh", "-c", "printf changed > \"$1/README.md\"", "sh", workdir},
		Workdir: workdir,
		Timeout: "5s",
	})
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "changed during verification") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestNonGitCommandRunsDependencyFreeGoTestInSnapshot(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte("module snapshot.test/example\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "example_test.go"), []byte("package example\nimport \"testing\"\nfunc TestSnapshot(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 2, 1<<20)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"go", "test", "./..."}, Workdir: workdir, Timeout: "30s"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestNonGitSnapshotPreservesExecutableFiles(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	script := filepath.Join(workdir, "check.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf executable\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 2, 1<<20)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"./check.sh"}, Workdir: workdir, Timeout: "5s"})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "executable") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestNonGitSnapshotEnforcesFileAndByteLimits(t *testing.T) {
	for _, test := range []struct {
		name      string
		maxFiles  int
		maxBytes  int64
		wantError string
	}{
		{name: "files", maxFiles: 1, maxBytes: 1 << 20, wantError: "entry limit"},
		{name: "bytes", maxFiles: 1_000, maxBytes: 1, wantError: "byte limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, root, workdir := verifierTestWorktree(t)
			if test.name == "files" {
				if err := os.WriteFile(filepath.Join(workdir, "second.txt"), []byte("second\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			handler := newVerifyHandler(verifierConfig{
				worktreeRoot: root, gitCommonDir: filepath.Join(repo, ".git"), outputBytes: 1 << 20,
				snapshotFiles: test.maxFiles, snapshotBytes: test.maxBytes, limiter: make(chan struct{}, 1),
			})
			response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"true"}, Workdir: workdir, Timeout: "5s"})
			if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), test.wantError) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestNonGitSnapshotRejectsSymlink(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	if err := os.Symlink("README.md", filepath.Join(workdir, "linked")); err != nil {
		t.Fatal(err)
	}
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 2, 1<<20)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"true"}, Workdir: workdir, Timeout: "5s"})
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "rejects symlinks") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestVerifierOutputIsCappedAcrossStdoutAndStderr(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 2, 128)
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"sh", "-c", "printf '%0200d' 0; printf '%0200d' 0 >&2"}, Workdir: workdir, Timeout: "5s"})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var decoded verifyResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Output) != 128 {
		t.Fatalf("output bytes = %d, want 128", len(decoded.Output))
	}
}

func TestVerifierLimiterAcquiresBeforeValidation(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 1, 1<<20)
	started := filepath.Join(t.TempDir(), "started")
	var first *httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		first = callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"sh", "-c", "touch \"$1\"; sleep 1", "sh", started}, Workdir: workdir, Timeout: "5s"})
	}()
	waitForFile(t, started)
	secondDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { secondDone <- callVerifyWithContext(t, handler, context.Background(), verifyRequest{}) }()
	select {
	case <-secondDone:
		t.Fatal("invalid request bypassed concurrency limiter")
	case <-time.After(100 * time.Millisecond):
	}
	wg.Wait()
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d", first.Code)
	}
	if second := <-secondDone; second.Code != http.StatusBadRequest {
		t.Fatalf("second status = %d", second.Code)
	}
}

func TestVerifierCancellationKillsDescendantProcess(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 1, 1<<20)
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- callVerifyWithContext(t, handler, ctx, verifyRequest{Command: []string{"sh", "-c", "sleep 30 & echo $! > \"$1\"; wait", "sh", pidFile}, Workdir: workdir, Timeout: "30s"})
	}()
	waitForFile(t, pidFile)
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response := <-done
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processTerminated(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived cancellation", pid)
}

func TestVerifierTimeoutKillsDescendantProcess(t *testing.T) {
	repo, root, workdir := verifierTestWorktree(t)
	handler := testVerifierHandler(root, filepath.Join(repo, ".git"), 1, 1<<20)
	pidFile := filepath.Join(t.TempDir(), "pid")
	response := callVerifyWithContext(t, handler, context.Background(), verifyRequest{
		Command: []string{"sh", "-c", "sleep 30 & echo $! > \"$1\"; wait", "sh", pidFile},
		Workdir: workdir,
		Timeout: "200ms",
	})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processTerminated(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived timeout", pid)
}

func processTerminated(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	closing := bytes.LastIndexByte(status, ')')
	return closing >= 0 && len(status) > closing+2 && status[closing+2] == 'Z'
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func verifierTestWorktree(t *testing.T) (string, string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	root := filepath.Join(repo, ".open-pilot-worktrees")
	workdir := filepath.Join(root, "run")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runVerifierGit(t, repo, "init", "--initial-branch=main")
	runVerifierGit(t, repo, "config", "user.email", "switchyard@example.test")
	runVerifierGit(t, repo, "config", "user.name", "Switchyard Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runVerifierGit(t, repo, "add", "README.md")
	runVerifierGit(t, repo, "commit", "-m", "initial")
	runVerifierGit(t, repo, "worktree", "add", "-b", "test-branch", workdir, "main")
	return repo, root, workdir
}

func runVerifierGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func TestOpenWorkdirRejectsSymlinkInRootPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("openat no-follow traversal requires Linux")
	}
	base := t.TempDir()
	realParent := filepath.Join(base, "real")
	workdir := filepath.Join(realParent, "root", "repo", "run")
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(base, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	directory, err := openWorkdir(
		filepath.Join(linkedParent, "root"),
		filepath.Join(linkedParent, "root", "repo", "run"),
	)
	if directory != nil {
		directory.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "invalid Open Pilot worktree root") {
		t.Fatalf("error = %v", err)
	}
}

func TestProcessCleanupPreservesInitAndVerifier(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"1", "7", "23", "self"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	pids, err := processIDs(root, 7)
	if err != nil || len(pids) != 1 || pids[0] != 23 {
		t.Fatalf("cleanup must target escaped children only: %v, %v", pids, err)
	}
}
