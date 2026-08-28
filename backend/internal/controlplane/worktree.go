package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"
	"golang.org/x/sys/unix"
)

type OpenPilotWorktreeMode string

type OpenPilotWorktreeCleanupMode string

const (
	OpenPilotWorktreeDisabled OpenPilotWorktreeMode = "disabled"
	OpenPilotWorktreeDryRun   OpenPilotWorktreeMode = "dry-run"
	OpenPilotWorktreeAllow    OpenPilotWorktreeMode = "allow"

	OpenPilotWorktreeCleanupDisabled  OpenPilotWorktreeCleanupMode = "disabled"
	OpenPilotWorktreeCleanupOnSuccess OpenPilotWorktreeCleanupMode = "on-success"
)

type OpenPilotWorktreePlan struct {
	Repo                string `json:"repo"`
	RepoPath            string `json:"repo_path,omitempty"`
	BaseBranch          string `json:"base_branch"`
	Branch              string `json:"branch"`
	Mode                string `json:"mode"`
	PlannedWorktreePath string `json:"planned_worktree_path,omitempty"`
	WorktreePath        string `json:"worktree_path,omitempty"`
	Executed            bool   `json:"executed"`
	DryRun              bool   `json:"dry_run,omitempty"`
}

func (p OpenPilotWorktreePlan) StepInputs() map[string]string {
	inputs := map[string]string{
		"repo":        p.Repo,
		"base_branch": p.BaseBranch,
		"branch":      p.Branch,
		"mode":        p.Mode,
	}
	if p.RepoPath != "" {
		inputs["repo_path"] = p.RepoPath
	}
	if p.PlannedWorktreePath != "" {
		inputs["planned_worktree_path"] = p.PlannedWorktreePath
	}
	if p.Executed && p.WorktreePath != "" {
		inputs["worktree_path"] = p.WorktreePath
	}
	if p.Executed {
		inputs["executed"] = "true"
	}
	if p.DryRun {
		inputs["dry_run"] = "true"
	}
	return inputs
}

type OpenPilotWorktreeManager interface {
	Prepare(ctx context.Context, run Run, repo string, baseBranch string, branch string) (OpenPilotWorktreePlan, error)
	Cleanup(ctx context.Context, plan OpenPilotWorktreePlan, durableHandoff bool) (OpenPilotWorktreeCleanupPlan, error)
}

type OpenPilotWorktreeCleanupPlan struct {
	Mode         string `json:"mode"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Executed     bool   `json:"executed"`
	Skipped      bool   `json:"skipped,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type OpenPilotPushPlan struct {
	Repo             string `json:"repo,omitempty"`
	RepoPath         string `json:"repo_path,omitempty"`
	Mode             string `json:"mode"`
	Remote           string `json:"remote"`
	Branch           string `json:"branch"`
	WorktreePath     string `json:"worktree_path,omitempty"`
	Executed         bool   `json:"executed"`
	DryRun           bool   `json:"dry_run,omitempty"`
	RemoteHeadExists bool   `json:"remote_head_exists,omitempty"`
}

func (p OpenPilotPushPlan) StepInputs() map[string]string {
	inputs := map[string]string{"mode": p.Mode, "remote": p.Remote, "branch": p.Branch}
	if p.Repo != "" {
		inputs["repo"] = p.Repo
	}
	if p.RepoPath != "" {
		inputs["repo_path"] = p.RepoPath
	}
	if p.WorktreePath != "" {
		inputs["worktree_path"] = p.WorktreePath
	}
	if p.Executed {
		inputs["executed"] = "true"
	}
	if p.DryRun {
		inputs["dry_run"] = "true"
	}
	if p.RemoteHeadExists {
		inputs["remote_head_exists"] = "true"
	}
	return inputs
}

type GitOpenPilotWorktreeManager struct {
	mode        OpenPilotWorktreeMode
	cleanupMode OpenPilotWorktreeCleanupMode
	repoPath    string
	root        string
	timeout     time.Duration
	beforeAdd   func()
}

func NewEnvOpenPilotWorktreeManager() *GitOpenPilotWorktreeManager {
	repoPath := firstNonEmpty(os.Getenv("OPEN_PILOT_REPO_PATH"), os.Getenv("OPEN_PILOT_WORKDIR"))
	root := os.Getenv("OPEN_PILOT_WORKTREE_ROOT")
	timeout := runtimeconfig.Duration("OPEN_PILOT_WORKTREE_TIMEOUT")
	if raw := os.Getenv("OPEN_PILOT_WORKTREE_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			timeout = parsed
		}
	}
	mode := firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_MODE"), string(OpenPilotWorktreeDisabled))
	cleanupMode := firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_CLEANUP"), string(OpenPilotWorktreeCleanupDisabled))
	if openPilotAutonomyFullEnabled() {
		mode = firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_MODE"), string(OpenPilotWorktreeAllow))
		cleanupMode = firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_CLEANUP"), string(OpenPilotWorktreeCleanupOnSuccess))
	}
	return &GitOpenPilotWorktreeManager{
		mode:        OpenPilotWorktreeMode(mode),
		cleanupMode: OpenPilotWorktreeCleanupMode(cleanupMode),
		repoPath:    repoPath,
		root:        root,
		timeout:     timeout,
	}
}

func NewGitOpenPilotWorktreeManager(mode OpenPilotWorktreeMode, repoPath string, root string) *GitOpenPilotWorktreeManager {
	if root == "" {
		root = filepath.Join(os.TempDir(), "switchyard-open-pilot-worktrees")
	}
	return &GitOpenPilotWorktreeManager{mode: mode, cleanupMode: OpenPilotWorktreeCleanupDisabled, repoPath: repoPath, root: root, timeout: 120 * time.Second}
}

func (m *GitOpenPilotWorktreeManager) Prepare(ctx context.Context, run Run, repo string, baseBranch string, branch string) (OpenPilotWorktreePlan, error) {
	mode := m.mode
	if mode == "" {
		mode = OpenPilotWorktreeDisabled
	}
	plan := OpenPilotWorktreePlan{
		Repo:       repo,
		RepoPath:   m.repoPath,
		BaseBranch: baseBranch,
		Branch:     branch,
		Mode:       string(mode),
	}
	switch mode {
	case OpenPilotWorktreeDisabled:
		return plan, nil
	case OpenPilotWorktreeDryRun:
		root := m.root
		if root == "" {
			root = filepath.Join(os.TempDir(), "switchyard-open-pilot-worktrees")
		}
		plan.PlannedWorktreePath = filepath.Join(root, safePathSegment(repo), safePathSegment(run.ID+"-"+branch))
		plan.DryRun = true
		return plan, nil
	case OpenPilotWorktreeAllow:
		if strings.TrimSpace(m.repoPath) == "" {
			return plan, errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR is required when OPEN_PILOT_WORKTREE_MODE=allow")
		}
		if strings.TrimSpace(m.root) == "" {
			return plan, errors.New("OPEN_PILOT_WORKTREE_ROOT is required when OPEN_PILOT_WORKTREE_MODE=allow")
		}
		source, root, err := m.validatedPaths(ctx, false)
		if err != nil {
			return plan, err
		}
		if err := m.validateRepoMatchesRequest(ctx, source, repo); err != nil {
			return plan, err
		}
		plan.RepoPath = source
		plan.PlannedWorktreePath = filepath.Join(root, safePathSegment(repo), safePathSegment(run.ID+"-"+branch))
		plan.PlannedWorktreePath, err = canonicalPath(plan.PlannedWorktreePath)
		if err != nil {
			return plan, fmt.Errorf("canonicalize planned worktree path: %w", err)
		}
		if err := requirePathWithin(root, plan.PlannedWorktreePath); err != nil {
			return plan, fmt.Errorf("validate planned worktree path: %w", err)
		}
		plan.WorktreePath = plan.PlannedWorktreePath
		if err := os.MkdirAll(filepath.Dir(plan.WorktreePath), 0o755); err != nil {
			return plan, err
		}
		root, err = canonicalExistingPath(m.root)
		if err != nil {
			return plan, fmt.Errorf("canonicalize OPEN_PILOT_WORKTREE_ROOT: %w", err)
		}
		parent, err := canonicalExistingPath(filepath.Dir(plan.PlannedWorktreePath))
		if err != nil {
			return plan, fmt.Errorf("canonicalize planned worktree parent: %w", err)
		}
		if err := requirePathWithin(root, parent); err != nil {
			return plan, fmt.Errorf("revalidate planned worktree parent: %w", err)
		}
		plan.PlannedWorktreePath = filepath.Join(parent, filepath.Base(plan.PlannedWorktreePath))
		if err := requirePathWithin(root, plan.PlannedWorktreePath); err != nil {
			return plan, fmt.Errorf("revalidate planned worktree path: %w", err)
		}
		plan.WorktreePath = plan.PlannedWorktreePath
		parentFD, err := openDirectoryWithoutSymlinks(parent)
		if err != nil {
			return plan, errors.New("open planned worktree parent without symlinks")
		}
		parentDirectory := os.NewFile(uintptr(parentFD), parent)
		defer parentDirectory.Close()
		leaf := filepath.Base(plan.PlannedWorktreePath)
		var destination unix.Stat_t
		if statErr := unix.Fstatat(parentFD, leaf, &destination, unix.AT_SYMLINK_NOFOLLOW); statErr == nil {
			return plan, errors.New("planned worktree destination already exists")
		} else if !errors.Is(statErr, unix.ENOENT) {
			return plan, errors.New("inspect planned worktree destination")
		}
		if m.beforeAdd != nil {
			m.beforeAdd()
		}
		if err := m.runGitWithFile(ctx, parentDirectory, "-C", source, "worktree", "add", "-b", branch, "/proc/self/fd/3/"+leaf, baseBranch); err != nil {
			return plan, fmt.Errorf("create git worktree: %w", err)
		}
		canonicalCreated, canonicalErr := canonicalExistingPath(plan.WorktreePath)
		registered, registeredErr := m.registeredWorktreePaths(ctx, source)
		if canonicalErr != nil || canonicalCreated != plan.WorktreePath || registeredErr != nil {
			_ = m.runGitWithFile(ctx, parentDirectory, "-C", source, "worktree", "remove", "--force", "/proc/self/fd/3/"+leaf)
			return plan, errors.New("created worktree is not registered at the canonical intended path")
		}
		if _, ok := registered[plan.WorktreePath]; !ok {
			_ = m.runGitWithFile(ctx, parentDirectory, "-C", source, "worktree", "remove", "--force", "/proc/self/fd/3/"+leaf)
			return plan, errors.New("created worktree is not registered at the canonical intended path")
		}
		plan.Executed = true
		return plan, nil
	default:
		return plan, fmt.Errorf("unsupported OPEN_PILOT_WORKTREE_MODE %q", mode)
	}
}

func (m *GitOpenPilotWorktreeManager) Cleanup(ctx context.Context, plan OpenPilotWorktreePlan, durableHandoff bool) (OpenPilotWorktreeCleanupPlan, error) {
	mode := m.cleanupMode
	if mode == "" {
		mode = OpenPilotWorktreeCleanupDisabled
	}
	cleanup := OpenPilotWorktreeCleanupPlan{Mode: string(mode)}
	if plan.WorktreePath != "" {
		cleanup.WorktreePath = plan.WorktreePath
	}
	if mode == OpenPilotWorktreeCleanupDisabled {
		cleanup.Skipped = true
		cleanup.Reason = "OPEN_PILOT_WORKTREE_CLEANUP=disabled"
		return cleanup, nil
	}
	if mode != OpenPilotWorktreeCleanupOnSuccess {
		return cleanup, fmt.Errorf("unsupported OPEN_PILOT_WORKTREE_CLEANUP %q", mode)
	}
	if !plan.Executed || strings.TrimSpace(plan.WorktreePath) == "" {
		cleanup.Skipped = true
		cleanup.Reason = "no executed worktree"
		return cleanup, nil
	}
	if !durableHandoff {
		cleanup.Skipped = true
		cleanup.Reason = "branch not pushed or published"
		return cleanup, nil
	}
	if strings.TrimSpace(m.repoPath) == "" {
		return cleanup, errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR is required for worktree cleanup")
	}
	source, root, err := m.validatedPaths(ctx, true)
	if err != nil {
		return cleanup, err
	}
	if strings.TrimSpace(plan.RepoPath) == "" {
		return cleanup, errors.New("persisted source repository path is required for worktree cleanup")
	}
	persistedSource, err := canonicalExistingPath(plan.RepoPath)
	if err != nil || filepath.Clean(plan.RepoPath) != persistedSource || persistedSource != source {
		return cleanup, errors.New("persisted source repository path no longer matches the configured repository")
	}
	if !filepath.IsAbs(plan.WorktreePath) {
		return cleanup, errors.New("persisted worktree path must be absolute")
	}
	worktreePath := filepath.Clean(plan.WorktreePath)
	canonicalWorktreePath, err := canonicalExistingPath(worktreePath)
	if err != nil {
		return cleanup, fmt.Errorf("canonicalize persisted worktree path: %w", err)
	}
	if worktreePath != canonicalWorktreePath {
		return cleanup, errors.New("persisted worktree lexical path no longer matches its canonical path")
	}
	if strings.TrimSpace(plan.PlannedWorktreePath) == "" || filepath.Clean(plan.PlannedWorktreePath) != worktreePath {
		return cleanup, errors.New("persisted planned worktree path does not match the executed worktree path")
	}
	if err := requirePathWithin(root, worktreePath); err != nil {
		return cleanup, fmt.Errorf("validate persisted worktree path: %w", err)
	}
	cleanup.WorktreePath = worktreePath
	registered, err := m.registeredWorktreePaths(ctx, source)
	if err != nil {
		return cleanup, fmt.Errorf("list registered worktrees: %w", err)
	}
	if _, ok := registered[worktreePath]; !ok {
		return cleanup, errors.New("persisted worktree path is not registered in the source repository")
	}
	actualBranch, err := m.runGitOutput(ctx, "-C", worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return cleanup, fmt.Errorf("inspect worktree branch before cleanup: %w", err)
	}
	if strings.TrimSpace(actualBranch) != plan.Branch {
		return cleanup, fmt.Errorf("worktree branch %q does not match planned branch %q", strings.TrimSpace(actualBranch), plan.Branch)
	}
	status, err := m.runGitOutput(ctx, "-C", worktreePath, "status", "--porcelain=v1")
	if err != nil {
		return cleanup, fmt.Errorf("revalidate worktree before cleanup: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		cleanup.Skipped = true
		cleanup.Reason = "worktree has uncommitted changes"
		return cleanup, nil
	}
	if err := m.runGit(ctx, "-C", source, "worktree", "remove", worktreePath); err != nil {
		return cleanup, fmt.Errorf("remove git worktree: %w", err)
	}
	cleanup.Executed = true
	return cleanup, nil
}

func validateOpenPilotWorktreeIdentity(ctx context.Context, repo, repoPath, worktreePath, branch string, requireOrigin bool) (gitworktree.Identity, error) {
	if strings.TrimSpace(repoPath) == "" {
		return gitworktree.Identity{}, errors.New("persisted source repository path is required")
	}
	if !filepath.IsAbs(repoPath) || filepath.Clean(repoPath) != repoPath {
		return gitworktree.Identity{}, errors.New("source repository path must be an exact absolute path")
	}
	canonicalRepo, err := filepath.EvalSymlinks(repoPath)
	if err != nil || canonicalRepo != repoPath {
		return gitworktree.Identity{}, errors.New("source repository lexical path does not match its canonical path")
	}
	return gitworktree.ValidatePath(ctx, gitworktree.Expected{
		WorktreePath:  worktreePath,
		CommonDir:     filepath.Join(repoPath, ".git"),
		Branch:        branch,
		Repo:          repo,
		RequireOrigin: requireOrigin,
		OriginBaseURL: os.Getenv("GITEA_BASE_URL"),
	})
}

func (m *GitOpenPilotWorktreeManager) registeredWorktreePaths(ctx context.Context, source string) (map[string]struct{}, error) {
	output, err := m.runGitOutput(ctx, "-C", source, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	paths := make(map[string]struct{})
	for _, field := range strings.Split(output, "\x00") {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			paths[path] = struct{}{}
		}
	}
	return paths, nil
}

func (m *GitOpenPilotWorktreeManager) runGit(ctx context.Context, args ...string) error {
	_, err := m.runGitOutput(ctx, args...)
	return err
}

func (m *GitOpenPilotWorktreeManager) runGitOutput(ctx context.Context, args ...string) (string, error) {
	timeout := m.timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", append(gitworktree.SafeCommandArgs(false), args...)...)
	cmd.Env = gitworktree.SanitizedEnvironment(nil)
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(message)
	}
	return string(output), nil
}

func (m *GitOpenPilotWorktreeManager) runGitWithFile(ctx context.Context, file *os.File, args ...string) error {
	timeout := m.timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", append(gitworktree.SafeCommandArgs(false), args...)...)
	cmd.ExtraFiles = []*os.File{file}
	cmd.Env = gitworktree.SanitizedEnvironment(nil)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
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

func (m *GitOpenPilotWorktreeManager) validateRepoMatchesRequest(ctx context.Context, repoPath string, repo string) error {
	remote, err := m.runGitOutput(ctx, "-C", repoPath, "config", "--get", "remote.origin.url")
	if err != nil {
		return errors.New("OPEN_PILOT_REPO_PATH origin is missing or unreadable")
	}
	requested, ok := exactOpenPilotRepoSlug(repo)
	if !ok {
		return fmt.Errorf("requested repo %q must be exact owner/repo", repo)
	}
	remoteRepo, ok := openPilotRepoSlugFromRemote(remote)
	if !ok {
		return errors.New("OPEN_PILOT_REPO_PATH origin is unparseable")
	}
	if requested != remoteRepo {
		return fmt.Errorf("OPEN_PILOT_REPO_PATH origin %q does not match requested repo %q", remoteRepo, repo)
	}
	return nil
}

func (m *GitOpenPilotWorktreeManager) validatedPaths(ctx context.Context, requireRoot bool) (string, string, error) {
	if !filepath.IsAbs(m.repoPath) {
		return "", "", errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR must be absolute")
	}
	if !filepath.IsAbs(m.root) {
		return "", "", errors.New("OPEN_PILOT_WORKTREE_ROOT must be absolute")
	}
	source, err := canonicalExistingPath(m.repoPath)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize source repository: %w", err)
	}
	var root string
	if requireRoot {
		root, err = canonicalExistingPath(m.root)
	} else {
		root, err = canonicalPath(m.root)
	}
	if err != nil {
		return "", "", fmt.Errorf("canonicalize OPEN_PILOT_WORKTREE_ROOT: %w", err)
	}
	if root == string(filepath.Separator) {
		return "", "", errors.New("OPEN_PILOT_WORKTREE_ROOT must not be /")
	}
	if source == root {
		return "", "", errors.New("source repository and OPEN_PILOT_WORKTREE_ROOT must differ")
	}
	topLevel, err := m.runGitOutput(ctx, "-C", source, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", fmt.Errorf("validate source repository: %w", err)
	}
	topLevel, err = canonicalExistingPath(strings.TrimSpace(topLevel))
	if err != nil {
		return "", "", fmt.Errorf("canonicalize source repository top-level: %w", err)
	}
	if topLevel != source {
		return "", "", fmt.Errorf("source repository %q is not Git top-level %q", source, topLevel)
	}
	if err := gitworktree.RejectUnsafeLocalConfig(ctx, filepath.Join(source, ".git")); err != nil {
		return "", "", err
	}
	return source, root, nil
}

func canonicalExistingPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	return filepath.EvalSymlinks(filepath.Clean(path))
}

func canonicalPath(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	current := filepath.Clean(path)
	missing := make([]string, 0)
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func requirePathWithin(root string, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q must be below root %q", path, root)
	}
	return nil
}

func openPilotRepoSlugFromRemote(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}
	if !strings.Contains(remote, "://") {
		host, path, ok := strings.Cut(remote, ":")
		if !ok || !strings.HasPrefix(host, "git@") || len(host) == len("git@") {
			return "", false
		}
		return exactOpenPilotRepoSlug(strings.TrimSuffix(path, ".git"))
	}
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return exactOpenPilotRepoSlug(strings.TrimSuffix(strings.TrimPrefix(parsed.EscapedPath(), "/"), ".git"))
}

func exactOpenPilotRepoSlug(repo string) (string, bool) {
	if repo == "" || repo != strings.TrimSpace(repo) || repo != strings.Trim(repo, "/") {
		return "", false
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return repo, true
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "run"
	}
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		isWord := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if isWord {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteRune('-')
			lastDash = true
		}
	}
	trimmed := strings.Trim(builder.String(), "-")
	if trimmed == "" {
		return "run"
	}
	return trimmed
}
