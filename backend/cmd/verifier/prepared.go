package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"git.subcult.tv/PatrickFanella/switchyard/internal/gitworktree"
)

type preparedManifest struct {
	Version  int                `json:"version"`
	BaseSHA  string             `json:"base_sha"`
	Files    map[string]*string `json:"files"`
	Commands [][]string         `json:"commands"`
	Profile  string             `json:"profile"`
}

// The bundle is operator-built into an immutable, read-only per-repository image.
// Repository input never selects paths or downloads dependencies inside verification.
func prepareVerification(ctx context.Context, bundle, snapshot string, identity gitworktree.Identity, argv []string) ([]string, func(), error) {
	env := nonGitEnvironment(snapshot)
	noop := func() {}
	if bundle == "" {
		return env, noop, nil
	}
	data, err := os.ReadFile(filepath.Join(bundle, "manifest.json"))
	if err != nil {
		return nil, noop, fmt.Errorf("read prepared verification manifest: %w", err)
	}
	var m preparedManifest
	if err = json.Unmarshal(data, &m); err != nil {
		return nil, noop, err
	}
	head, err := identity.Command(ctx, nil, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, noop, err
	}
	if err = validatePreparedManifest(m, strings.TrimSpace(string(head)), snapshot, argv); err != nil {
		return nil, noop, err
	}
	cache, err := os.MkdirTemp("/cache", "verification-")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { _ = os.RemoveAll(cache) }
	fail := func(e error) ([]string, func(), error) { cleanup(); return nil, noop, e }
	// The immutable store is copied per attempt: package-manager writes cannot poison
	// later runs or alter the prepared image. Never copy dependencies from the executor.
	if output, e := exec.CommandContext(ctx, "cp", "-a", filepath.Join(bundle, "pnpm"), filepath.Join(cache, "pnpm")).CombinedOutput(); e != nil {
		return fail(fmt.Errorf("copy prepared package store: %w: %s", e, output))
	}
	for _, name := range []string{"go-build", "tmp"} {
		if e := os.MkdirAll(filepath.Join(cache, name), 0700); e != nil {
			return fail(e)
		}
	}
	env = append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "VITEST_MAX_WORKERS=2", "UV_THREADPOOL_SIZE=2", "GOMAXPROCS=2", "GOFLAGS=-p=1", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOMODCACHE="+filepath.Join(bundle, "go"), "GOCACHE="+filepath.Join(cache, "go-build"), "GOTMPDIR="+filepath.Join(cache, "tmp"), "npm_config_offline=true", "npm_config_store_dir="+filepath.Join(cache, "pnpm"), "npm_config_manage_package_manager_versions=false", "POSTGRES_PASSWORD=isolated-verification-placeholder")
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
	return env, cleanup, nil
}

// Version 1 images retain their exact-base restriction. Version 2 binds reusable
// dependencies to every supported input while the verifier's snapshot manifest
// binds the command and result to the exact current source bytes.
func validatePreparedManifest(m preparedManifest, head, snapshot string, argv []string) error {
	if len(m.BaseSHA) != 40 || len(head) != 40 {
		return fmt.Errorf("invalid prepared verification source revision")
	}
	if _, err := hex.DecodeString(m.BaseSHA); err != nil {
		return fmt.Errorf("invalid prepared verification source revision")
	}
	if _, err := hex.DecodeString(head); err != nil {
		return fmt.Errorf("invalid verification source revision")
	}
	switch m.Version {
	case 1:
		if m.Profile != "go1.26.6-node24-pnpm10.33.0-v1" || head != m.BaseSHA {
			return fmt.Errorf("prepared verification source revision mismatch")
		}
	case 2:
		if m.Profile != "go1.26.6-node24-pnpm10.33.0-v2" {
			return fmt.Errorf("unsupported prepared verification profile")
		}
	default:
		return fmt.Errorf("unsupported prepared verification manifest version")
	}
	inputs := []string{"backend/go.mod", "backend/go.sum", "web/package.json", "web/pnpm-lock.yaml", "package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", ".npmrc", "web/.npmrc", "go.work", "go.work.sum"}
	if len(m.Files) != len(inputs) {
		return fmt.Errorf("incomplete prepared dependency inputs")
	}
	for _, name := range inputs {
		if _, ok := m.Files[name]; !ok {
			return fmt.Errorf("missing prepared dependency contract for %s", name)
		}
	}
	allowed := false
	for _, c := range m.Commands {
		if reflect.DeepEqual(c, argv) {
			allowed = true
		}
	}
	if !allowed {
		return fmt.Errorf("command is not supported by prepared verification profile")
	}
	return validatePreparedFiles(snapshot, m.Files)
}

func validatePreparedFiles(snapshot string, files map[string]*string) error {
	for name, want := range files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("invalid prepared input path")
		}
		data, err := os.ReadFile(filepath.Join(snapshot, name))
		if want == nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("unexpected dependency input %s", name)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("missing prepared dependency input %s", name)
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != *want {
			return fmt.Errorf("stale prepared dependency input %s", name)
		}
	}
	return nil
}
