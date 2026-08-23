package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"
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
	Manifest string
	Executed bool
}

type CommandRunner interface {
	Run(ctx context.Context, command string, workdir ...string) (CommandResult, error)
}

type PolicyCommandRunner struct {
	policy      CommandPolicy
	verifierURL string
	httpClient  *http.Client
}

func NewEnvCommandRunner() *PolicyCommandRunner {
	mode := CommandPolicyMode(firstNonEmpty(os.Getenv("OPEN_PILOT_COMMAND_POLICY_MODE"), string(CommandPolicyDeny)))
	allowedCommands := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_COMMANDS"), "git,go,make,pnpm,npm,cargo"))
	allowedGitVerbs := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_GIT_VERBS"), "status,diff,checkout,switch,branch,add,commit"))
	deniedCommands := setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_DENIED_COMMANDS"), "rm,sudo,curl,wget,ssh,scp,docker,chmod,chown,dd,mkfs,bash,sh,zsh,fish"))
	if openPilotAutonomyFullEnabled() {
		mode = CommandPolicyMode(firstNonEmpty(os.Getenv("OPEN_PILOT_COMMAND_POLICY_MODE"), string(CommandPolicyAllow)))
		allowedCommands = setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_COMMANDS"), "git,go,make,pnpm,npm,cargo"))
		allowedGitVerbs = setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_ALLOWED_GIT_VERBS"), "status,diff,checkout,switch,branch,add,commit"))
		deniedCommands = setFromCSV(firstNonEmpty(os.Getenv("OPEN_PILOT_DENIED_COMMANDS"), "rm,sudo,curl,wget,ssh,scp,docker,chmod,chown,dd,mkfs,bash,sh,zsh,fish"))
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
	}, verifierURL: strings.TrimSuffix(strings.TrimSpace(os.Getenv("OPEN_PILOT_VERIFIER_URL")), "/"), httpClient: &http.Client{}}
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
		result.Manifest = "dry-run"
		if cmdDir := r.commandWorkdir(workdir...); cmdDir != "" {
			if manifest, err := manifestForCommandWorkdir(ctx, cmdDir); err == nil {
				result.Manifest = manifest
			}
		}
		return result, nil
	}
	if r.verifierURL != "" {
		return r.runViaVerifier(ctx, decision, workdir...)
	}
	runCtx, cancel := context.WithTimeout(ctx, r.policy.Timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, decision.Command[0], decision.Command[1:]...)
	cmdDir := r.commandWorkdir(workdir...)
	if cmdDir != "" {
		cmd.Dir = cmdDir
	}
	output, err := cmd.CombinedOutput()
	result.Executed = true
	result.Output = strings.TrimSpace(string(output))
	if err != nil {
		return result, err
	}
	if cmdDir != "" {
		result.Manifest, err = manifestForCommandWorkdir(ctx, cmdDir)
		if err != nil {
			return result, fmt.Errorf("compute command worktree manifest: %w", err)
		}
	}
	return result, nil
}

func (r *PolicyCommandRunner) commandWorkdir(workdir ...string) string {
	cmdDir := r.policy.Workdir
	if len(workdir) > 0 && strings.TrimSpace(workdir[0]) != "" {
		cmdDir = strings.TrimSpace(workdir[0])
	}
	return cmdDir
}

func manifestForCommandWorkdir(ctx context.Context, workdir string) (string, error) {
	directory, err := os.Open(workdir)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	return gitworktree.Manifest(ctx, directory)
}

type verifierRequest struct {
	Command []string `json:"command"`
	Workdir string   `json:"workdir"`
	Timeout string   `json:"timeout"`
}

type verifierResponse struct {
	Output   string `json:"output"`
	Error    string `json:"error"`
	Manifest string `json:"manifest"`
}

func (r *PolicyCommandRunner) runViaVerifier(ctx context.Context, decision CommandDecision, workdir ...string) (CommandResult, error) {
	cmdDir := r.commandWorkdir(workdir...)
	payload, err := json.Marshal(verifierRequest{Command: decision.Command, Workdir: cmdDir, Timeout: r.policy.Timeout.String()})
	if err != nil {
		return CommandResult{Decision: decision}, fmt.Errorf("encode verifier request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.verifierURL+"/verify", bytes.NewReader(payload))
	if err != nil {
		return CommandResult{Decision: decision}, fmt.Errorf("create verifier request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.httpClient.Do(request)
	if err != nil {
		return CommandResult{Decision: decision}, fmt.Errorf("execute verifier request: %w", err)
	}
	defer response.Body.Close()
	var body verifierResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return CommandResult{Decision: decision}, fmt.Errorf("decode verifier response: %w", err)
	}
	result := CommandResult{Decision: decision, Executed: true, Output: strings.TrimSpace(body.Output), Manifest: strings.TrimSpace(body.Manifest)}
	if response.StatusCode != http.StatusOK {
		return result, fmt.Errorf("verifier rejected command: %s", firstNonEmpty(body.Error, response.Status))
	}
	return result, nil
}

func (r *PolicyCommandRunner) Decide(command string) CommandDecision {
	if r.policy.Mode == "" || r.policy.Mode == CommandPolicyDeny {
		return CommandDecision{Allowed: false, Reason: "command policy mode is deny"}
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
	if r.policy.Mode != CommandPolicyAllow && r.policy.Mode != CommandPolicyDryRun {
		return CommandDecision{Allowed: false, Reason: "unsupported command policy mode: " + string(r.policy.Mode), Command: parts}
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
