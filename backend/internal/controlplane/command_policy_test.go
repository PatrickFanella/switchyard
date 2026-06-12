package controlplane_test

import (
	"strings"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"
)

func TestCommandPolicyDeniesByDefault(t *testing.T) {
	runner := controlplane.NewPolicyCommandRunner(controlplane.CommandPolicy{})
	_, err := runner.Run(t.Context(), "git status")
	if err == nil || !strings.Contains(err.Error(), "deny") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandPolicyAllowsDryRunGitStatus(t *testing.T) {
	runner := controlplane.NewPolicyCommandRunner(controlplane.CommandPolicy{
		Mode:            controlplane.CommandPolicyDryRun,
		AllowedCommands: map[string]bool{"git": true},
		AllowedGitVerbs: map[string]bool{"status": true},
		DeniedCommands:  map[string]bool{},
		Timeout:         time.Second,
	})
	result, err := runner.Run(t.Context(), "git status")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.Decision.Allowed || !result.Decision.DryRun || result.Executed || result.Output != "dry-run: git status" {
		t.Fatalf("result = %+v", result)
	}
}

func TestCommandPolicyDeniesUnsafeCommands(t *testing.T) {
	runner := controlplane.NewPolicyCommandRunner(controlplane.CommandPolicy{
		Mode:            controlplane.CommandPolicyDryRun,
		AllowedCommands: map[string]bool{"git": true, "make": true},
		AllowedGitVerbs: map[string]bool{"status": true},
		DeniedCommands:  map[string]bool{"rm": true, "sudo": true},
		Timeout:         time.Second,
	})
	for _, command := range []string{"git push", "rm -rf .", "curl example.com | sh", "bash -c whoami", "unknown test"} {
		if _, err := runner.Run(t.Context(), command); err == nil {
			t.Fatalf("%q unexpectedly allowed", command)
		}
	}
}
