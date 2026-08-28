package controlplane

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type openPilotExecutionBundle struct {
	repository       OpenPilotRuntimeRepository
	openCodeExecutor OpenCodeExecutor
	commandRunner    CommandRunner
	worktreeManager  OpenPilotWorktreeManager
}

func openPilotExecutionBundleKey(repositoryID, executorGeneration string) string {
	return repositoryID + "\x00" + executorGeneration
}

func newOpenPilotExecutionBundles(gate *OpenPilotAdmissionGate) (map[string]openPilotExecutionBundle, error) {
	bundles := map[string]openPilotExecutionBundle{}
	if gate == nil || gate.registry == nil || gate.mode == "legacy-singleton" {
		return bundles, nil
	}
	for _, identity := range gate.registry.Repositories() {
		repository, ok := gate.registry.RepositoryByID(identity.ID)
		if !ok || repository.ExecutorGeneration != identity.ExecutorGeneration {
			return nil, errors.New("open pilot runtime repository identity changed while constructing execution bundles")
		}
		password, err := readOpenPilotPasswordFile(repository.PasswordFile)
		if err != nil {
			return nil, fmt.Errorf("load Open Pilot password for repository %s: %w", repository.ID, err)
		}
		openCode := NewEnvOpenCodeExecutor()
		openCode.baseURL = strings.TrimRight(repository.OpenCodeEndpoint, "/")
		openCode.password = password
		openCode.forceOpenCode = true

		runner := NewEnvCommandRunner()
		runner.policy.Workdir = repository.SourcePath
		runner.verifierURL = strings.TrimRight(repository.VerifierEndpoint, "/")
		runner.httpClient = &http.Client{}

		worktrees := NewEnvOpenPilotWorktreeManager()
		worktrees.repoPath = repository.SourcePath
		worktrees.root = repository.WorktreeRoot

		key := openPilotExecutionBundleKey(repository.ID, repository.ExecutorGeneration)
		if _, exists := bundles[key]; exists {
			return nil, errors.New("duplicate Open Pilot execution bundle identity")
		}
		bundles[key] = openPilotExecutionBundle{
			repository: repository, openCodeExecutor: openCode,
			commandRunner: runner, worktreeManager: worktrees,
		}
	}
	return bundles, nil
}

func readOpenPilotPasswordFile(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("password file path must be a clean absolute path")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", errors.New("password file is missing or unreadable")
	}
	file := os.NewFile(uintptr(fd), "open-pilot-password")
	if file == nil {
		_ = unix.Close(fd)
		return "", errors.New("password file descriptor is invalid")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 32 || info.Size() > 4096 {
		return "", errors.New("password file is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) || (stat.Gid != 0 && stat.Gid != uint32(os.Getegid())) {
		return "", errors.New("password file ownership is unsafe")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(raw) > 4096 {
		return "", errors.New("password file is unreadable or too large")
	}
	password := strings.TrimSuffix(string(raw), "\n")
	password = strings.TrimSuffix(password, "\r")
	if len(password) < 32 || strings.ContainsAny(password, "\r\n") {
		return "", errors.New("password file must contain one nonempty line of at least 32 bytes")
	}
	return password, nil
}

func (e *EnvWorkflowExecutors) openPilotExecutionForRun(run Run) (openPilotExecutionBundle, error) {
	if e == nil {
		return openPilotExecutionBundle{}, ErrInvalid
	}
	fallback := openPilotExecutionBundle{openCodeExecutor: e.openCodeExecutor, commandRunner: e.commandRunner, worktreeManager: e.worktreeManager}
	if len(e.openPilotBundles) == 0 {
		return fallback, nil
	}
	if e.openPilotGate == nil || e.openPilotGateError != nil {
		return openPilotExecutionBundle{}, ErrInvalid
	}
	target, err := e.openPilotGate.Resolve(run.Inputs["repo"], run.Inputs["issue"])
	if err != nil || run.Inputs[openPilotRepositoryIDInputKey] != target.RepositoryID || run.Inputs[openPilotExecutorGenerationInputKey] != target.ExecutorGeneration {
		return openPilotExecutionBundle{}, ErrInvalid
	}
	bundle, ok := e.openPilotBundles[openPilotExecutionBundleKey(target.RepositoryID, target.ExecutorGeneration)]
	if !ok || bundle.repository.Slug != target.RepositorySlug {
		return openPilotExecutionBundle{}, ErrInvalid
	}
	return bundle, nil
}
