package controlplane

import "testing"

func TestEnvWorkflowExecutorsCIAutoRepairConfig(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "manual")
	t.Setenv("OPEN_PILOT_CI_AUTO_REPAIR_MODE", "")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "")
	t.Setenv("OPEN_PILOT_WORKTREE_CLEANUP", "")

	executors := NewEnvWorkflowExecutors()
	if executors.openPilotCIAutoRepairMode != "disabled" {
		t.Fatalf("openPilotCIAutoRepairMode = %q", executors.openPilotCIAutoRepairMode)
	}
	if executors.openPilotWorktreeMode != "disabled" || executors.openPilotWorktreeCleanupMode != "disabled" {
		t.Fatalf("worktree modes = %q, %q", executors.openPilotWorktreeMode, executors.openPilotWorktreeCleanupMode)
	}

	t.Setenv("OPEN_PILOT_CI_AUTO_REPAIR_MODE", "allow")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "allow")
	t.Setenv("OPEN_PILOT_WORKTREE_CLEANUP", "on-success")
	executors = NewEnvWorkflowExecutors()
	if executors.openPilotCIAutoRepairMode != "allow" {
		t.Fatalf("openPilotCIAutoRepairMode = %q", executors.openPilotCIAutoRepairMode)
	}
	if executors.openPilotWorktreeMode != "allow" || executors.openPilotWorktreeCleanupMode != "on-success" {
		t.Fatalf("worktree modes = %q, %q", executors.openPilotWorktreeMode, executors.openPilotWorktreeCleanupMode)
	}
}

func TestEnvWorkflowExecutorsFullAutonomyEffectiveWorktreeConfig(t *testing.T) {
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "full")
	t.Setenv("OPEN_PILOT_CI_AUTO_REPAIR_MODE", "")
	t.Setenv("OPEN_PILOT_WORKTREE_MODE", "")
	t.Setenv("OPEN_PILOT_WORKTREE_CLEANUP", "")

	executors := NewEnvWorkflowExecutors()
	if executors.openPilotCIAutoRepairMode != "disabled" {
		t.Fatalf("full autonomy enabled CI auto-repair without opt-in: %q", executors.openPilotCIAutoRepairMode)
	}
	if executors.openPilotWorktreeMode != "allow" || executors.openPilotWorktreeCleanupMode != "on-success" {
		t.Fatalf("worktree modes = %q, %q", executors.openPilotWorktreeMode, executors.openPilotWorktreeCleanupMode)
	}
}
