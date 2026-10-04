package controlplane

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// OpenPilotAdmissionGate resolves immutable affinity and enforces the activation mode.
type OpenPilotAdmissionGate struct {
	registry *OpenPilotRepositoryRegistry
	bound    OpenPilotRuntimeRepository
	mode     string
	rollout  Store
}

const (
	openPilotRepositoryIDInputKey       = "_switchyard.open_pilot.repository_id"
	openPilotExecutorGenerationInputKey = "_switchyard.open_pilot.executor_generation"
	openPilotLifecycleResumeInputKey    = "_switchyard.open_pilot.lifecycle_resume"
)

func NewEnvOpenPilotAdmissionGate() (*OpenPilotAdmissionGate, error) {
	manifestPath := strings.TrimSpace(os.Getenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST"))
	activationMode := firstNonEmpty(strings.TrimSpace(os.Getenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE")), "legacy-singleton")
	if activationMode != "legacy-singleton" && activationMode != "disabled" && activationMode != "isolated-test" && activationMode != "durable" {
		return nil, errors.New("invalid Open Pilot activation mode")
	}
	if manifestPath != "" && activationMode != "legacy-singleton" {
		manifest, err := LoadOpenPilotRuntimeManifest(manifestPath)
		if err != nil {
			return nil, err
		}
		for _, repository := range manifest.Repositories {
			if repository.SourcePath == "" || repository.WorktreeRoot == "" || repository.OpenCodeEndpoint == "" || repository.VerifierEndpoint == "" || repository.PasswordFile == "" {
				return nil, errors.New("multi-repository runtime manifest entry is incomplete")
			}
		}
		registry, err := NewOpenPilotRepositoryRegistry(manifest)
		return &OpenPilotAdmissionGate{registry: registry, mode: activationMode}, err
	}
	boundSlug := strings.TrimSpace(os.Getenv("OPEN_PILOT_SINGLETON_REPOSITORY"))
	sourcePath, originSlug, err := openPilotMountedOrigin()
	if err != nil {
		return nil, err
	}
	if boundSlug != "" && boundSlug != originSlug {
		return nil, errors.New("OPEN_PILOT_SINGLETON_REPOSITORY does not exactly match the mounted source origin")
	}
	boundSlug = originSlug
	generation := firstNonEmpty(strings.TrimSpace(os.Getenv("OPEN_PILOT_EXECUTOR_GENERATION")), "legacy")
	if manifestPath == "" {
		manifest, err := SynthesizeLegacyOpenPilotRuntimeManifest(LegacyOpenPilotRuntimeBinding{
			Slug: boundSlug, SourcePath: sourcePath,
			WorktreeRoot: os.Getenv("OPEN_PILOT_WORKTREE_ROOT"), OpenCodeEndpoint: os.Getenv("OPENCODE_SERVER_URL"),
			VerifierEndpoint: os.Getenv("OPEN_PILOT_VERIFIER_URL"), ExecutorGeneration: generation,
		})
		if err != nil {
			return nil, err
		}
		registry, err := NewOpenPilotRepositoryRegistry(manifest)
		return &OpenPilotAdmissionGate{registry: registry, bound: manifest.Repositories[0], mode: "legacy-singleton"}, err
	}
	manifest, err := LoadOpenPilotRuntimeManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	registry, err := NewOpenPilotRepositoryRegistry(manifest)
	if err != nil {
		return nil, err
	}
	var bound OpenPilotRuntimeRepository
	for _, repository := range manifest.Repositories {
		if repository.Slug == boundSlug {
			bound = repository
			break
		}
	}
	if bound.ID == "" {
		return nil, errors.New("runtime manifest does not contain the execution-bound singleton repository")
	}
	manifestSource, err := canonicalOpenPilotSourcePath(bound.SourcePath)
	if err != nil || bound.ExecutorGeneration != generation || manifestSource != sourcePath ||
		(bound.WorktreeRoot != "" && bound.WorktreeRoot != os.Getenv("OPEN_PILOT_WORKTREE_ROOT")) ||
		(bound.OpenCodeEndpoint != "" && strings.TrimRight(bound.OpenCodeEndpoint, "/") != strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096/api"), "/")) ||
		(bound.VerifierEndpoint != "" && strings.TrimRight(bound.VerifierEndpoint, "/") != strings.TrimRight(os.Getenv("OPEN_PILOT_VERIFIER_URL"), "/")) {
		return nil, errors.New("runtime manifest does not exactly match the execution-bound singleton")
	}
	return &OpenPilotAdmissionGate{registry: registry, bound: bound, mode: "legacy-singleton"}, nil
}

func openPilotMountedOrigin() (string, string, error) {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("OPEN_PILOT_REPO_PATH"), os.Getenv("OPEN_PILOT_WORKDIR")))
	if raw == "" {
		return "", "", errors.New("OPEN_PILOT_REPO_PATH or OPEN_PILOT_WORKDIR is required")
	}
	sourcePath, err := canonicalOpenPilotSourcePath(raw)
	if err != nil {
		return "", "", errors.New("configured Open Pilot source path is missing or unreadable")
	}
	top, err := exec.Command("git", "-C", sourcePath, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", "", errors.New("configured Open Pilot source path is not a Git worktree")
	}
	canonicalTop, err := canonicalOpenPilotSourcePath(strings.TrimSpace(string(top)))
	if err != nil || canonicalTop != sourcePath {
		return "", "", errors.New("configured Open Pilot source path is not the Git worktree root")
	}
	origin, err := exec.Command("git", "-C", sourcePath, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", "", errors.New("configured Open Pilot source origin is missing or unreadable")
	}
	slug, ok := openPilotRepoSlugFromRemote(strings.TrimSpace(string(origin)))
	if !ok && strings.EqualFold(strings.TrimSpace(os.Getenv("OPEN_PILOT_ALLOW_LOCAL_GIT_REMOTE")), "true") {
		slug, ok = exactOpenPilotRepoSlug(strings.TrimSpace(os.Getenv("OPEN_PILOT_SINGLETON_REPOSITORY")))
	}
	if !ok {
		return "", "", errors.New("configured Open Pilot source origin is not an exact repository slug")
	}
	return sourcePath, slug, nil
}

func canonicalOpenPilotSourcePath(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Clean(abs))
}

func (g *OpenPilotAdmissionGate) Resolve(repo, issue string) (OpenPilotTarget, error) {
	if g == nil || g.registry == nil {
		return OpenPilotTarget{}, ErrInvalid
	}
	target, err := g.registry.ResolveTarget(repo, issue)
	if err != nil || g.mode == "disabled" {
		return OpenPilotTarget{}, ErrInvalid
	}
	if g.mode == "durable" {
		if g.rollout == nil {
			return OpenPilotTarget{}, ErrInvalid
		}
		status, statusErr := g.rollout.OpenPilotRolloutStatus()
		if statusErr != nil || status.RegistryFingerprint != g.registry.manifest.RegistryFingerprint || status.Mode == "disabled" || status.Mode == "legacy" {
			return OpenPilotTarget{}, ErrInvalid
		}
		if status.Mode == "proof" {
			allowed := false
			for _, proof := range status.ProofTargets {
				allowed = allowed || (proof.RepositoryID == target.RepositoryID && proof.ExecutorGeneration == target.ExecutorGeneration && proof.Issue == target.Issue)
			}
			if !allowed {
				return OpenPilotTarget{}, ErrInvalid
			}
		} else if status.Mode != "allow" {
			return OpenPilotTarget{}, ErrInvalid
		}
	}
	if (g.mode == "" || g.mode == "legacy-singleton") && (target.RepositoryID != g.bound.ID || target.ExecutorGeneration != g.bound.ExecutorGeneration) {
		return OpenPilotTarget{}, ErrInvalid
	}
	return target, nil
}

func (g *OpenPilotAdmissionGate) ValidateRunState(run Run, state OpenPilotIssueState) (OpenPilotTarget, error) {
	target, err := g.ValidateState(state)
	if err != nil || state.RepositoryID == "" || state.RunID != run.ID || run.Inputs["repo"] != state.Repo || run.Inputs["issue"] != state.Issue {
		return OpenPilotTarget{}, ErrConflict
	}
	if run.Inputs[openPilotRepositoryIDInputKey] != state.RepositoryID || run.Inputs[openPilotExecutorGenerationInputKey] != state.ExecutorGeneration {
		return OpenPilotTarget{}, ErrConflict
	}
	return target, nil
}

func setOpenPilotAffinityInputs(inputs map[string]string, target OpenPilotTarget) {
	inputs[openPilotRepositoryIDInputKey] = target.RepositoryID
	inputs[openPilotExecutorGenerationInputKey] = target.ExecutorGeneration
}

func validateOpenPilotTarget(target OpenPilotTarget) error {
	if !openPilotRepositoryIDPattern.MatchString(target.RepositoryID) || !openPilotRepositoryIDPattern.MatchString(target.ExecutorGeneration) {
		return ErrInvalid
	}
	slug, err := ParseOpenPilotRepositorySlug(target.RepositorySlug)
	if err != nil || slug != target.RepositorySlug {
		return ErrInvalid
	}
	issue, err := ParseOpenPilotIssue(target.Issue)
	if err != nil || issue != target.Issue {
		return ErrInvalid
	}
	candidates := openPilotTargetCandidates(target)
	if len(candidates) == 0 || !containsOpenPilotCandidate(candidates, foldOpenPilotRepositoryName(target.RepositorySlug)) {
		return ErrInvalid
	}
	return nil
}

func openPilotTargetCandidates(target OpenPilotTarget) []string {
	names := append([]string(nil), target.RepositoryCandidates...)
	if len(names) == 0 {
		names = []string{target.RepositorySlug}
	}
	folded := make(map[string]struct{}, len(names)+1)
	for _, name := range append(names, target.RepositorySlug) {
		if validOpenPilotRepositorySlug(name) {
			folded[foldOpenPilotRepositoryName(name)] = struct{}{}
		}
	}
	out := make([]string, 0, len(folded))
	for name := range folded {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func unionOpenPilotCandidates(existing, current []string) []string {
	set := make(map[string]struct{}, len(existing)+len(current))
	for _, candidate := range append(append([]string(nil), existing...), current...) {
		candidate = foldOpenPilotRepositoryName(candidate)
		if validOpenPilotRepositorySlug(candidate) {
			set[candidate] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for candidate := range set {
		out = append(out, candidate)
	}
	sort.Strings(out)
	return out
}

func containsOpenPilotCandidate(candidates []string, name string) bool {
	index := sort.SearchStrings(candidates, name)
	return index < len(candidates) && candidates[index] == name
}

func openPilotTargetMatchesCanonical(target OpenPilotTarget, repo, issue string) bool {
	return issue == target.Issue && containsOpenPilotCandidate(openPilotTargetCandidates(target), foldOpenPilotRepositoryName(repo))
}

func validateOpenPilotRunStateTarget(run Run, state OpenPilotIssueState, target OpenPilotTarget) error {
	if err := validateOpenPilotRunAffinityTarget(run, state, target); err != nil {
		return err
	}
	if state.RunID != run.ID {
		return ErrConflict
	}
	return nil
}

func validateOpenPilotRunAffinityTarget(run Run, state OpenPilotIssueState, target OpenPilotTarget) error {
	if err := validateOpenPilotTarget(target); err != nil {
		return err
	}
	if run.WorkflowID != string(WorkflowOpenPilot) || run.Inputs["repo"] != state.Repo || run.Inputs["issue"] != state.Issue || state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue || run.Inputs[openPilotRepositoryIDInputKey] != target.RepositoryID || run.Inputs[openPilotExecutorGenerationInputKey] != target.ExecutorGeneration {
		return ErrConflict
	}
	return nil
}

func (g *OpenPilotAdmissionGate) ValidateState(state OpenPilotIssueState) (OpenPilotTarget, error) {
	target, err := g.Resolve(state.Repo, state.Issue)
	if err != nil {
		return OpenPilotTarget{}, err
	}
	if (state.RepositoryID == "") != (state.ExecutorGeneration == "") {
		return OpenPilotTarget{}, ErrConflict
	}
	if state.RepositoryID != "" && (state.RepositoryID != target.RepositoryID || state.ExecutorGeneration != target.ExecutorGeneration || state.CanonicalRepo != target.RepositorySlug || state.CanonicalIssue != target.Issue) {
		return OpenPilotTarget{}, ErrConflict
	}
	return target, nil
}

func reservedOpenPilotAffinityInput(key string) bool {
	tokens := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool { return r == '.' || r == '_' || r == '-' })
	joined := strings.Join(tokens, ".")
	if strings.HasPrefix(joined, "switchyard.open.pilot.") || strings.HasPrefix(joined, "open.pilot.") {
		return true
	}
	switch joined {
	case "repository.id", "executor.generation", "canonical.repo", "canonical.issue", "legacy.repo", "legacy.issue":
		return true
	}
	if strings.HasPrefix(joined, "resume.open.pilot.") {
		for _, suffix := range []string{"repository.id", "executor.generation", "canonical.repo", "canonical.issue", "legacy.repo", "legacy.issue"} {
			if strings.HasSuffix(joined, "."+suffix) {
				return true
			}
		}
	}
	return false
}

// ResolvePollRepository validates discovery against a real proof target rather
// than assuming issue 1 is admitted. Actual candidate admission remains separate.
func (g *OpenPilotAdmissionGate) ResolvePollRepository(repo string) (OpenPilotTarget, error) {
	if g == nil || g.registry == nil {
		return OpenPilotTarget{}, ErrInvalid
	}
	probeIssue := "1"
	if g.mode == "durable" && g.rollout != nil {
		status, err := g.rollout.OpenPilotRolloutStatus()
		if err != nil {
			return OpenPilotTarget{}, err
		}
		if status.Mode == "proof" {
			target, err := g.registry.ResolveTarget(repo, "1")
			if err != nil {
				return OpenPilotTarget{}, err
			}
			for _, proof := range status.ProofTargets {
				if proof.RepositoryID == target.RepositoryID && proof.ExecutorGeneration == target.ExecutorGeneration {
					probeIssue = proof.Issue
					break
				}
			}
		}
	}
	return g.Resolve(repo, probeIssue)
}
