package controlplane

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

type CommandPolicyMode string

const (
	CommandPolicyDeny   CommandPolicyMode = "deny"
	CommandPolicyDryRun CommandPolicyMode = "dry-run"
	CommandPolicyAllow  CommandPolicyMode = "allow"
)

type CommandPolicy struct {
	Mode            CommandPolicyMode
	AllowedCommands map[string]bool
	AllowedGitVerbs map[string]bool
	DeniedCommands  map[string]bool
	Workdir         string
	Timeout         time.Duration
}

type CommandDecision struct {
	Allowed bool
	Reason  string
	Command []string
	DryRun  bool
}

type CommandResult struct {
	Decision CommandDecision
	Output   string
	Executed bool
}

type CommandRunner interface {
	Run(ctx context.Context, command string, workdir ...string) (CommandResult, error)
}

type PolicyCommandRunner struct {
	policy CommandPolicy
}

func NewEnvCommandRunner() *PolicyCommandRunner {
	mode := CommandPolicyMode(firstNonEmpty(os.Getenv("OPEN_PILOT_COMMAND_POLICY_MODE"), string(CommandPolicyDeny)))
	allowedCommands := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_COMMANDS"), "git,go,make,pnpm,npm,cargo"))
	allowedGitVerbs := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_GIT_VERBS"), "status,diff,checkout,switch,branch,add,commit"))
	deniedCommands := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_DENIED_COMMANDS"), "rm,sudo,curl,wget,ssh,scp,docker,chmod,chown,dd,mkfs,bash,sh,zsh,fish"))
	if openPilotAutonomyFullEnabled() {
		mode = CommandPolicyMode(firstNonEmpty(os.Getenv("OPEN_PILOT_COMMAND_POLICY_MODE"), string(CommandPolicyAllow)))
		allowedCommands = setFromCSV(os.Getenv("OPEN_PILOT_ALLOWED_COMMANDS"))
		allowedGitVerbs = setFromCSV(os.Getenv("OPEN_PILOT_ALLOWED_GIT_VERBS"))
		deniedCommands = setFromCSV(os.Getenv("OPEN_PILOT_DENIED_COMMANDS"))
	}
	timeout := 120 * time.Second
	if raw := os.Getenv("OPEN_PILOT_COMMAND_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			timeout = parsed
		}
	}
	return &PolicyCommandRunner{policy: CommandPolicy{
		Mode:            mode,
		AllowedCommands: allowedCommands,
		AllowedGitVerbs: allowedGitVerbs,
		DeniedCommands:  deniedCommands,
		Workdir:         os.Getenv("OPEN_PILOT_WORKDIR"),
		Timeout:         timeout,
	}}
}

func NewPolicyCommandRunner(policy CommandPolicy) *PolicyCommandRunner {
	if policy.Timeout <= 0 {
		policy.Timeout = 120 * time.Second
	}
	return &PolicyCommandRunner{policy: policy}
}

func (r *PolicyCommandRunner) Run(ctx context.Context, command string, workdir ...string) (CommandResult, error) {
	decision := r.Decide(command)
	result := CommandResult{Decision: decision}
	if !decision.Allowed {
		return result, errors.New(decision.Reason)
	}
	if decision.DryRun {
		result.Output = "dry-run: " + strings.Join(decision.Command, " ")
		return result, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, r.policy.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, decision.Command[0], decision.Command[1:]...)
	cmdDir := r.policy.Workdir
	if len(workdir) > 0 && strings.TrimSpace(workdir[0]) != "" {
		cmdDir = strings.TrimSpace(workdir[0])
	}
	if cmdDir != "" {
		cmd.Dir = cmdDir
	}
	output, err := cmd.CombinedOutput()
	result.Executed = true
	result.Output = strings.TrimSpace(string(output))
	if err != nil {
		return result, err
	}
	return result, nil
}

func (r *PolicyCommandRunner) Decide(command string) CommandDecision {
	if r.policy.Mode == "" || r.policy.Mode == CommandPolicyDeny {
		return CommandDecision{Allowed: false, Reason: "command policy mode is deny"}
	}
	if r.policy.Mode == CommandPolicyAllow {
		if strings.TrimSpace(command) == "" {
			return CommandDecision{Allowed: false, Reason: "command is empty"}
		}
		return CommandDecision{Allowed: true, Reason: "allowed", Command: []string{"sh", "-lc", command}}
	}
	if containsShellSyntax(command) {
		return CommandDecision{Allowed: false, Reason: "command contains shell syntax"}
	}
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return CommandDecision{Allowed: false, Reason: "command is empty"}
	}
	exe := parts[0]
	if r.policy.DeniedCommands[exe] {
		return CommandDecision{Allowed: false, Reason: "command is explicitly denied: " + exe, Command: parts}
	}
	if len(r.policy.AllowedCommands) > 0 && !r.policy.AllowedCommands[exe] {
		return CommandDecision{Allowed: false, Reason: "command is not allowed: " + exe, Command: parts}
	}
	if exe == "git" {
		if len(parts) < 2 {
			return CommandDecision{Allowed: false, Reason: "git subcommand is required", Command: parts}
		}
		verb := parts[1]
		if len(r.policy.AllowedGitVerbs) > 0 && !r.policy.AllowedGitVerbs[verb] {
			return CommandDecision{Allowed: false, Reason: "git subcommand is not allowed: " + verb, Command: parts}
		}
	}
	return CommandDecision{Allowed: true, Reason: "allowed", Command: parts, DryRun: r.policy.Mode == CommandPolicyDryRun}
}

func containsShellSyntax(command string) bool {
	return strings.ContainsAny(command, "|&;<>`$()\n\r")
}

func setFromCSV(value string) map[string]bool {
	out := map[string]bool{}
	for _, item := range splitCSV(value) {
		out[item] = true
	}
	return out
}
