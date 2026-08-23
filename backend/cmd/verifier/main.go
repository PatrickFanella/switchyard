package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"

	"golang.org/x/sys/unix"
)

type verifyRequest struct {
	Command []string `json:"command"`
	Workdir string   `json:"workdir"`
	Timeout string   `json:"timeout"`
}

type verifyResponse struct {
	Output   string `json:"output"`
	Error    string `json:"error,omitempty"`
	Manifest string `json:"manifest,omitempty"`
}

const (
	defaultOutputBytes         = 1 << 20
	defaultSnapshotFiles       = 10_000
	defaultSnapshotBytes       = 256 << 20
	maxOutputBytes             = 16 << 20
	maxSnapshotFiles           = 100_000
	maxSnapshotBytes     int64 = 1 << 30
	snapshotReadBatch          = 128
	processReadBatch           = 128
)

type verifierConfig struct {
	worktreeRoot  string
	gitCommonDir  string
	outputBytes   int
	snapshotFiles int
	snapshotBytes int64
	limiter       chan struct{}
	cleanup       func(context.Context) error
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	config, err := verifierConfigFromEnv()
	if err != nil {
		slog.Error("invalid verifier configuration", "error", err)
		os.Exit(1)
	}
	config.cleanup = cleanupVerifierProcesses
	mux := http.NewServeMux()
	mux.Handle("POST /verify", newVerifyHandler(config))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Addr: ":8090", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("verifier stopped", "error", err)
		os.Exit(1)
	}
}

func verifierConfigFromEnv() (verifierConfig, error) {
	if _, err := boundedEnvInt("SWITCHYARD_VERIFIER_CONCURRENCY", 1, 1, 1); err != nil {
		return verifierConfig{}, err
	}
	outputBytes, err := boundedEnvInt("SWITCHYARD_VERIFIER_OUTPUT_BYTES", defaultOutputBytes, 1, maxOutputBytes)
	if err != nil {
		return verifierConfig{}, err
	}
	snapshotFiles, err := boundedEnvInt("SWITCHYARD_VERIFIER_SNAPSHOT_MAX_FILES", defaultSnapshotFiles, 1, maxSnapshotFiles)
	if err != nil {
		return verifierConfig{}, err
	}
	snapshotBytes, err := boundedEnvInt64("SWITCHYARD_VERIFIER_SNAPSHOT_MAX_BYTES", defaultSnapshotBytes, 1, maxSnapshotBytes)
	if err != nil {
		return verifierConfig{}, err
	}
	return verifierConfig{
		worktreeRoot:  os.Getenv("OPEN_PILOT_WORKTREE_ROOT"),
		gitCommonDir:  os.Getenv("OPEN_PILOT_GIT_COMMON_DIR"),
		outputBytes:   outputBytes,
		snapshotFiles: snapshotFiles,
		snapshotBytes: snapshotBytes,
		limiter:       make(chan struct{}, 1),
	}, nil
}

func boundedEnvInt(name string, fallback, minimum, maximum int) (int, error) {
	value, err := boundedEnvInt64(name, int64(fallback), int64(minimum), int64(maximum))
	return int(value), err
}

func boundedEnvInt64(name string, fallback, minimum, maximum int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", name, minimum, maximum)
	}
	return value, nil
}

func newVerifyHandler(config verifierConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case config.limiter <- struct{}{}:
			defer func() { <-config.limiter }()
		case <-r.Context().Done():
			writeResponse(w, http.StatusRequestTimeout, verifyResponse{Error: "request canceled"})
			return
		}
		if config.cleanup != nil {
			defer func() {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := config.cleanup(cleanupContext); err != nil {
					slog.Error("verifier process cleanup failed", "error", err)
				}
			}()
		}
		verifyWithConfig(config, w, r)
	})
}

func verify(w http.ResponseWriter, r *http.Request) {
	config, err := verifierConfigFromEnv()
	if err != nil {
		writeResponse(w, http.StatusInternalServerError, verifyResponse{Error: "invalid verifier configuration"})
		return
	}
	newVerifyHandler(config).ServeHTTP(w, r)
}

func verifyWithConfig(config verifierConfig, w http.ResponseWriter, r *http.Request) {
	var request verifyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&request); err != nil {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "invalid request"})
		return
	}
	if len(request.Command) == 0 || strings.TrimSpace(request.Command[0]) == "" {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "command is required"})
		return
	}
	timeout, err := time.ParseDuration(request.Timeout)
	if err != nil || timeout <= 0 || timeout > 10*time.Minute {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: "invalid timeout"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	workdir, err := openWorkdir(config.worktreeRoot, request.Workdir)
	if err != nil {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: err.Error()})
		return
	}
	defer workdir.Close()
	identity, err := gitworktree.ValidateOpened(ctx, workdir, gitworktree.Expected{
		WorktreePath: request.Workdir,
		CommonDir:    config.gitCommonDir,
	})
	if err != nil {
		writeResponse(w, http.StatusBadRequest, verifyResponse{Error: err.Error()})
		return
	}
	defer identity.Close()
	limits := gitworktree.ManifestLimits{MaxFiles: config.snapshotFiles, MaxBytes: config.snapshotBytes}
	manifest, err := gitworktree.ManifestBounded(ctx, identity.Worktree, limits)
	if err != nil {
		writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Error: err.Error()})
		return
	}
	var command *exec.Cmd
	var cleanup func()
	if request.Command[0] == "git" {
		command = identity.Command(ctx, nil, request.Command[1:]...)
	} else {
		snapshot, snapshotErr := createSnapshot(ctx, workdir, config.snapshotFiles, config.snapshotBytes)
		if snapshotErr != nil {
			writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Error: snapshotErr.Error()})
			return
		}
		cleanup = func() { _ = os.RemoveAll(snapshot) }
		defer cleanup()
		snapshotDirectory, openErr := os.Open(snapshot)
		if openErr != nil {
			writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Error: "open verification snapshot"})
			return
		}
		snapshotManifest, manifestErr := gitworktree.ManifestBounded(ctx, snapshotDirectory, limits)
		_ = snapshotDirectory.Close()
		if manifestErr != nil {
			writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Error: manifestErr.Error()})
			return
		}
		if snapshotManifest != manifest {
			writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Error: fmt.Sprintf("verification snapshot does not match worktree manifest (%s != %s)", snapshotManifest, manifest)})
			return
		}
		command = exec.CommandContext(ctx, request.Command[0], request.Command[1:]...)
		command.Dir = snapshot
		command.Env = nonGitEnvironment(snapshot)
	}
	output := &cappedWriter{limit: config.outputBytes}
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	err = command.Run()
	killProcessGroup(command)
	manifestContext, manifestCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer manifestCancel()
	afterManifest, manifestErr := gitworktree.ManifestBounded(manifestContext, identity.Worktree, limits)
	if manifestErr != nil {
		writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Output: strings.TrimSpace(output.String()), Error: manifestErr.Error()})
		return
	}
	if afterManifest != manifest {
		writeResponse(w, http.StatusUnprocessableEntity, verifyResponse{Output: strings.TrimSpace(output.String()), Error: "managed worktree changed during verification"})
		return
	}
	response := verifyResponse{Output: strings.TrimSpace(output.String()), Manifest: manifest}
	if err != nil {
		response.Error = "command failed"
		writeResponse(w, http.StatusUnprocessableEntity, response)
		return
	}
	writeResponse(w, http.StatusOK, response)
}

type cappedWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	limit  int
}

func (w *cappedWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(value)
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = w.buffer.Write(value)
	}
	return original, nil
}

func (w *cappedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String()
}

func nonGitEnvironment(home string) []string {
	allowed := map[string]bool{
		"PATH": true, "GOCACHE": true, "GOMODCACHE": true, "GOTMPDIR": true,
		"CARGO_HOME": true, "PNPM_HOME": true, "npm_config_cache": true,
		"XDG_CACHE_HOME": true, "TMPDIR": true,
	}
	env := []string{"HOME=" + home}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if allowed[name] {
			env = append(env, entry)
		}
	}
	return env
}

type snapshotBudget struct {
	files    int
	bytes    int64
	maxFiles int
	maxBytes int64
}

func createSnapshot(ctx context.Context, source *os.File, maxFiles int, maxBytes int64) (string, error) {
	destination, err := os.MkdirTemp("/tmp", "switchyard-verifier-")
	if err != nil {
		return "", errors.New("create verification snapshot")
	}
	budget := &snapshotBudget{maxFiles: maxFiles, maxBytes: maxBytes}
	if err := copySnapshotDirectory(ctx, int(source.Fd()), destination, budget, true); err != nil {
		_ = os.RemoveAll(destination)
		return "", err
	}
	return destination, nil
}

func copySnapshotDirectory(ctx context.Context, sourceFD int, destination string, budget *snapshotBudget, root bool) error {
	duplicate, err := unix.Openat(sourceFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("read verification snapshot source")
	}
	directory := os.NewFile(uintptr(duplicate), "snapshot-source")
	defer directory.Close()
	for {
		batchSize := min(snapshotReadBatch, budget.maxFiles-budget.files+1)
		if batchSize <= 0 {
			return errors.New("verification snapshot file limit exceeded")
		}
		entries, readErr := directory.ReadDir(batchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return errors.New("verification snapshot canceled")
			}
			if root && entry.Name() == ".git" {
				continue
			}
			budget.files++
			if budget.files > budget.maxFiles {
				return errors.New("verification snapshot file limit exceeded")
			}
			var stat unix.Stat_t
			if err := unix.Fstatat(sourceFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return errors.New("inspect verification snapshot entry")
			}
			mode := stat.Mode & unix.S_IFMT
			switch mode {
			case unix.S_IFDIR:
				childFD, err := unix.Openat(sourceFD, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return errors.New("open verification snapshot directory")
				}
				target := filepath.Join(destination, entry.Name())
				if err := os.Mkdir(target, 0o700); err != nil {
					_ = unix.Close(childFD)
					return errors.New("create verification snapshot directory")
				}
				err = copySnapshotDirectory(ctx, childFD, target, budget, false)
				_ = unix.Close(childFD)
				if err != nil {
					return err
				}
			case unix.S_IFREG:
				if stat.Size < 0 || budget.bytes > budget.maxBytes-stat.Size {
					return errors.New("verification snapshot byte limit exceeded")
				}
				sourceFileFD, err := unix.Openat(sourceFD, entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
				if err != nil {
					return errors.New("open verification snapshot file")
				}
				sourceFile := os.NewFile(uintptr(sourceFileFD), entry.Name())
				info, statErr := sourceFile.Stat()
				if statErr != nil || !info.Mode().IsRegular() {
					_ = sourceFile.Close()
					return errors.New("verification snapshot entry changed during copy")
				}
				permissions := info.Mode().Perm()
				target := filepath.Join(destination, entry.Name())
				targetFile, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
				if err != nil {
					_ = sourceFile.Close()
					return errors.New("create verification snapshot file")
				}
				written, copyErr := io.Copy(targetFile, io.LimitReader(contextReader{ctx: ctx, reader: sourceFile}, budget.maxBytes-budget.bytes+1))
				closeErr := targetFile.Close()
				_ = sourceFile.Close()
				if copyErr != nil || closeErr != nil || written != info.Size() {
					return errors.New("copy verification snapshot file")
				}
				budget.bytes += written
			default:
				return errors.New("verification snapshot rejects symlinks and special files")
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return errors.New("read verification snapshot source")
		}
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(value []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(value)
}

func killProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("kill verifier process group", "error", err)
	}
}

func cleanupVerifierProcesses(ctx context.Context) error {
	self := os.Getpid()
	for {
		reapChildren()
		pids, err := processIDs("/proc", self)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			if err := unix.Kill(pid, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
				slog.Warn("kill escaped verifier process", "pid", pid, "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("clean verifier processes: %w", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func processIDs(procRoot string, self int) ([]int, error) {
	directory, err := os.Open(procRoot)
	if err != nil {
		return nil, fmt.Errorf("open process table: %w", err)
	}
	defer directory.Close()
	var pids []int
	for {
		entries, readErr := directory.ReadDir(processReadBatch)
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, parseErr := strconv.Atoi(entry.Name())
			if parseErr == nil && pid > 0 && pid != self {
				pids = append(pids, pid)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return pids, nil
		}
		if readErr != nil {
			return nil, fmt.Errorf("read process table: %w", readErr)
		}
	}
}

func reapChildren() {
	for {
		pid, err := unix.Wait4(-1, nil, unix.WNOHANG, nil)
		if pid <= 0 || errors.Is(err, unix.ECHILD) {
			return
		}
		if err != nil {
			slog.Warn("reap verifier child", "error", err)
			return
		}
	}
}

func stableDirectoryPath() string {
	return "/proc/self/fd/3"
}

func openWorkdir(root string, workdir string) (*os.File, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("OPEN_PILOT_WORKTREE_ROOT must be an absolute path")
	}
	if !filepath.IsAbs(workdir) {
		return nil, errors.New("workdir must be an absolute path")
	}
	cleanRoot := filepath.Clean(root)
	if cleanRoot == string(filepath.Separator) {
		return nil, errors.New("OPEN_PILOT_WORKTREE_ROOT must not be filesystem root")
	}
	cleanWorkdir := filepath.Clean(workdir)
	relative, err := filepath.Rel(cleanRoot, cleanWorkdir)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return nil, errors.New("workdir must be an Open Pilot worktree")
	}
	fd, err := openDirectoryWithoutSymlinks(cleanRoot)
	if err != nil {
		return nil, errors.New("invalid Open Pilot worktree root")
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, errors.New("invalid Open Pilot worktree or symlink component")
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), cleanWorkdir), nil
}

func openDirectoryWithoutSymlinks(path string) (int, error) {
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func writeResponse(w http.ResponseWriter, status int, response verifyResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}
