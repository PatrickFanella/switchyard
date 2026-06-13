package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	Mode         string `json:"mode"`
	Remote       string `json:"remote"`
	Branch       string `json:"branch"`
	WorktreePath string `json:"worktree_path,omitempty"`
	Executed     bool   `json:"executed"`
	DryRun       bool   `json:"dry_run,omitempty"`
}

func (p OpenPilotPushPlan) StepInputs() map[string]string {
	inputs := map[string]string{"mode": p.Mode, "remote": p.Remote, "branch": p.Branch}
	if p.WorktreePath != "" {
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

type GitOpenPilotWorktreeManager struct {
	mode        OpenPilotWorktreeMode
	cleanupMode OpenPilotWorktreeCleanupMode
	repoPath    string
	root        string
	timeout     time.Duration
}

func NewEnvOpenPilotWorktreeManager() *GitOpenPilotWorktreeManager {
	repoPath := firstNonEmpty(os.Getenv("OPEN_PILOT_REPO_PATH"), os.Getenv("OPEN_PILOT_WORKDIR"))
	root := os.Getenv("OPEN_PILOT_WORKTREE_ROOT")
	if root == "" {
		root = filepath.Join(os.TempDir(), "switchyard-open-pilot-worktrees")
	}
	timeout := 120 * time.Second
	if raw := os.Getenv("OPEN_PILOT_WORKTREE_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			timeout = parsed
		}
	}
	return &GitOpenPilotWorktreeManager{
		mode:        OpenPilotWorktreeMode(firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_MODE"), string(OpenPilotWorktreeDisabled))),
		cleanupMode: OpenPilotWorktreeCleanupMode(firstNonEmpty(os.Getenv("OPEN_PILOT_WORKTREE_CLEANUP"), string(OpenPilotWorktreeCleanupDisabled))),
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
		plan.PlannedWorktreePath = filepath.Join(m.root, safePathSegment(repo), safePathSegment(run.ID+"-"+branch))
		plan.DryRun = true
		return plan, nil
	case OpenPilotWorktreeAllow:
		plan.PlannedWorktreePath = filepath.Join(m.root, safePathSegment(repo), safePathSegment(run.ID+"-"+branch))
		plan.WorktreePath = plan.PlannedWorktreePath
		if strings.TrimSpace(m.repoPath) == "" {
			return plan, errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR is required when OPEN_PILOT_WORKTREE_MODE=allow")
		}
		if err := os.MkdirAll(filepath.Dir(plan.WorktreePath), 0o755); err != nil {
			return plan, err
		}
		if err := m.runGit(ctx, "-C", m.repoPath, "rev-parse", "--is-inside-work-tree"); err != nil {
			return plan, fmt.Errorf("validate source repository: %w", err)
		}
		if err := m.runGit(ctx, "-C", m.repoPath, "worktree", "add", "-b", branch, plan.WorktreePath, baseBranch); err != nil {
			return plan, fmt.Errorf("create git worktree: %w", err)
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
	status, err := m.runGitOutput(ctx, "-C", plan.WorktreePath, "status", "--porcelain=v1")
	if err != nil {
		return cleanup, fmt.Errorf("inspect worktree before cleanup: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		cleanup.Skipped = true
		cleanup.Reason = "worktree has uncommitted changes"
		return cleanup, nil
	}
	if strings.TrimSpace(m.repoPath) == "" {
		return cleanup, errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR is required for worktree cleanup")
	}
	if err := m.runGit(ctx, "-C", m.repoPath, "worktree", "remove", plan.WorktreePath); err != nil {
		return cleanup, fmt.Errorf("remove git worktree: %w", err)
	}
	cleanup.Executed = true
	return cleanup, nil
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
	cmd := exec.CommandContext(runCtx, "git", args...)
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
