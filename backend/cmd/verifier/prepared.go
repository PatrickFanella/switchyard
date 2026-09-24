package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"
)

// profiles.json is the single machine-readable preparation contract. It is embedded
// here and read by scripts/prepare_verifier.py so both sides pin the same toolchains,
// dependency inputs, and supported commands.
//
//go:embed profiles.json
var preparedProfilesJSON []byte

type preparedProfile struct {
	ManifestVersion int               `json:"manifest_version"`
	Reuse           string            `json:"reuse"`
	Toolchains      map[string]string `json:"toolchains"`
	Inputs          []string          `json:"inputs"`
	PreparedInputs  []string          `json:"prepared_inputs"`
	Commands        [][]string        `json:"commands"`
}

type preparedProfiles struct {
	Version  int                        `json:"version"`
	Profiles map[string]preparedProfile `json:"profiles"`
}

func loadPreparedProfiles() (preparedProfiles, error) {
	var profiles preparedProfiles
	if err := json.Unmarshal(preparedProfilesJSON, &profiles); err != nil {
		return preparedProfiles{}, fmt.Errorf("decode embedded verification profiles: %w", err)
	}
	if profiles.Version != 1 || len(profiles.Profiles) == 0 {
		return preparedProfiles{}, errors.New("unsupported embedded verification profile contract")
	}
	for name, profile := range profiles.Profiles {
		if profile.ManifestVersion < 1 || len(profile.Inputs) == 0 || len(profile.Commands) == 0 {
			return preparedProfiles{}, fmt.Errorf("invalid embedded verification profile %s", name)
		}
	}
	return profiles, nil
}

func mustLoadPreparedProfiles() preparedProfiles {
	profiles, err := loadPreparedProfiles()
	if err != nil {
		panic(err)
	}
	return profiles
}

type preparedManifest struct {
	Version  int                `json:"version"`
	BaseSHA  string             `json:"base_sha"`
	Files    map[string]*string `json:"files"`
	Commands [][]string         `json:"commands"`
	Profile  string             `json:"profile"`
}

// preparedIdentity is the preparation provenance attached to every verification result.
type preparedIdentity struct {
	Profile         string `json:"profile"`
	ManifestVersion int    `json:"manifest_version"`
	BaseSHA         string `json:"base_sha"`
}

// preparedStatus is the machine-readable answer of GET /prepared.
type preparedStatus struct {
	Prepared        bool                       `json:"prepared"`
	Profile         string                     `json:"profile,omitempty"`
	ManifestVersion int                        `json:"manifest_version,omitempty"`
	BaseSHA         string                     `json:"base_sha,omitempty"`
	Reuse           string                     `json:"reuse,omitempty"`
	Toolchains      map[string]string          `json:"toolchains,omitempty"`
	Commands        [][]string                 `json:"commands"`
	Inputs          map[string]*string         `json:"inputs,omitempty"`
	Profiles        map[string]preparedProfile `json:"profiles"`
	Error           string                     `json:"error,omitempty"`
}

func readPreparedManifest(bundle string) (preparedManifest, error) {
	data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return preparedManifest{}, fmt.Errorf("read prepared verification manifest: %w", err)
	}
	var m preparedManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return preparedManifest{}, fmt.Errorf("decode prepared verification manifest: %w", err)
	}
	return m, nil
}

// preparedStatusFor reports the bundle bound into this verifier without reading any
// repository source. Missing or invalid preparation is reported, not hidden.
func preparedStatusFor(bundle string, profiles preparedProfiles) preparedStatus {
	status := preparedStatus{Commands: [][]string{}, Profiles: profiles.Profiles}
	if bundle == "" {
		status.Error = "no prepared verification bundle is configured; commands run with ambient toolchains only"
		return status
	}
	m, err := readPreparedManifest(bundle)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	profile, ok := profiles.Profiles[m.Profile]
	if !ok || profile.ManifestVersion != m.Version {
		status.Error = fmt.Sprintf("prepared verification profile %q (manifest version %d) is not supported by this verifier", m.Profile, m.Version)
		return status
	}
	status.Prepared = true
	status.Profile = m.Profile
	status.ManifestVersion = m.Version
	status.BaseSHA = m.BaseSHA
	status.Reuse = profile.Reuse
	status.Toolchains = profile.Toolchains
	status.Inputs = m.Files
	if m.Commands != nil {
		status.Commands = m.Commands
	}
	return status
}

func newPreparedHandler(config verifierConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(preparedStatusFor(config.bundle, config.profiles))
	})
}

// The bundle is operator-built into an immutable, read-only per-repository image.
// Repository input never selects paths or downloads dependencies inside verification.
func prepareVerification(ctx context.Context, config verifierConfig, snapshot string, identity gitworktree.Identity, argv []string) ([]string, *preparedIdentity, func(), error) {
	env := nonGitEnvironment(snapshot)
	noop := func() {}
	if config.bundle == "" {
		return env, nil, noop, nil
	}
	m, err := readPreparedManifest(config.bundle)
	if err != nil {
		return nil, nil, noop, err
	}
	head, err := identity.Command(ctx, nil, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, nil, noop, err
	}
	if err = validatePreparedManifest(config.profiles, m, strings.TrimSpace(string(head)), snapshot, argv); err != nil {
		return nil, nil, noop, err
	}
	cacheRoot := config.cacheRoot
	if cacheRoot == "" {
		cacheRoot = defaultCacheRoot
	}
	cache, err := os.MkdirTemp(cacheRoot, "verification-")
	if err != nil {
		return nil, nil, noop, fmt.Errorf("create disposable verification cache: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(cache) }
	fail := func(e error) ([]string, *preparedIdentity, func(), error) { cleanup(); return nil, nil, noop, e }
	// The immutable store is copied per attempt: package-manager writes cannot poison
	// later runs or alter the prepared image. Never copy dependencies from the executor.
	if output, e := exec.CommandContext(ctx, "cp", "-a", filepath.Join(config.bundle, "pnpm"), filepath.Join(cache, "pnpm")).CombinedOutput(); e != nil {
		return fail(fmt.Errorf("copy prepared package store: %w: %s", e, output))
	}
	for _, name := range []string{"go-build", "tmp"} {
		if e := os.MkdirAll(filepath.Join(cache, name), 0700); e != nil {
			return fail(e)
		}
	}
	env = append(env, preparedEnvironment(config.bundle, cache)...)
	// Only disposable metadata is initialized. No source Git config, hooks, credentials,
	// or object database is copied into the command's working directory.
	for _, args := range [][]string{{"-c", "init.defaultBranch=verification", "init", "--quiet"}, {"-c", "core.hooksPath=/dev/null", "add", "--all"}, {"-c", "core.hooksPath=/dev/null", "-c", "user.name=Isolated Verification", "-c", "user.email=verification@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "Disposable verification snapshot"}} {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = snapshot
		c.Env = env
		if output, e := c.CombinedOutput(); e != nil {
			return fail(fmt.Errorf("initialize disposable Git context: %w: %s", e, output))
		}
	}
	return env, &preparedIdentity{Profile: m.Profile, ManifestVersion: m.Version, BaseSHA: m.BaseSHA}, cleanup, nil
}

// preparedEnvironment pins every toolchain to the immutable bundle and a per-attempt
// cache. pnpm 10 reads npm_config_* while pnpm 11+ reads only pnpm_config_*, so both
// prefixes carry the offline and store settings.
func preparedEnvironment(bundle, cache string) []string {
	env := []string{
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"VITEST_MAX_WORKERS=2", "UV_THREADPOOL_SIZE=2", "GOMAXPROCS=2", "GOFLAGS=-p=1",
		"GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off",
		"GOMODCACHE=" + filepath.Join(bundle, "go"), "GOCACHE=" + filepath.Join(cache, "go-build"), "GOTMPDIR=" + filepath.Join(cache, "tmp"),
		"POSTGRES_PASSWORD=isolated-verification-placeholder",
	}
	for _, prefix := range []string{"npm_config_", "pnpm_config_"} {
		env = append(env, prefix+"offline=true", prefix+"store_dir="+filepath.Join(cache, "pnpm"), prefix+"manage_package_manager_versions=false")
	}
	return env
}

// Version 1 images retain their exact-base restriction. Version 2 binds reusable
// dependencies to every supported input while the verifier's snapshot manifest
// binds the command and result to the exact current source bytes.
func validatePreparedManifest(profiles preparedProfiles, m preparedManifest, head, snapshot string, argv []string) error {
	if len(m.BaseSHA) != 40 || len(head) != 40 {
		return fmt.Errorf("invalid prepared verification source revision")
	}
	if _, err := hex.DecodeString(m.BaseSHA); err != nil {
		return fmt.Errorf("invalid prepared verification source revision")
	}
	if _, err := hex.DecodeString(head); err != nil {
		return fmt.Errorf("invalid verification source revision")
	}
	profile, ok := profiles.Profiles[m.Profile]
	if !ok {
		return fmt.Errorf("unsupported prepared verification profile %q; rebuild with scripts/prepare_verifier.py", m.Profile)
	}
	if profile.ManifestVersion != m.Version {
		return fmt.Errorf("unsupported prepared verification manifest version %d for profile %s", m.Version, m.Profile)
	}
	if profile.Reuse == "exact-revision" && head != m.BaseSHA {
		return fmt.Errorf("prepared verification source revision mismatch: image prepared for %s, worktree is %s", m.BaseSHA, head)
	}
	if len(m.Files) != len(profile.Inputs) {
		return fmt.Errorf("incomplete prepared dependency inputs: profile %s binds %d inputs, manifest lists %d", m.Profile, len(profile.Inputs), len(m.Files))
	}
	for _, name := range profile.Inputs {
		if _, ok := m.Files[name]; !ok {
			return fmt.Errorf("missing prepared dependency contract for %s", name)
		}
	}
	for _, c := range m.Commands {
		if !containsCommand(profile.Commands, c) {
			return fmt.Errorf("prepared manifest command %q is not supported by profile %s", strings.Join(c, " "), m.Profile)
		}
	}
	if !containsCommand(m.Commands, argv) {
		return fmt.Errorf("command %q is not supported by prepared verification profile %s (supported: %s)", strings.Join(argv, " "), m.Profile, describeCommands(m.Commands))
	}
	return validatePreparedFiles(snapshot, m.Files)
}

func containsCommand(commands [][]string, argv []string) bool {
	for _, c := range commands {
		if reflect.DeepEqual(c, argv) {
			return true
		}
	}
	return false
}

func describeCommands(commands [][]string) string {
	parts := make([]string, 0, len(commands))
	for _, c := range commands {
		parts = append(parts, strings.Join(c, " "))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

func validatePreparedFiles(snapshot string, files map[string]*string) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := files[name]
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid prepared input path")
		}
		data, err := os.ReadFile(filepath.Join(snapshot, name))
		if want == nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("unexpected dependency input %s: the prepared image was built without it; rebuild with scripts/prepare_verifier.py", name)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("missing prepared dependency input %s: the prepared image expects it; rebuild with scripts/prepare_verifier.py", name)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != *want {
			return fmt.Errorf("stale prepared dependency input %s: content differs from the prepared image; rebuild with scripts/prepare_verifier.py", name)
		}
	}
	return nil
}
