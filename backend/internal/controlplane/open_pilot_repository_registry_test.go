package controlplane

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestOpenPilotRepositoryRegistryResolveTarget(t *testing.T) {
	registry, err := NewOpenPilotRepositoryRegistry(validOpenPilotRuntimeManifest())
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		repository string
		issue      string
		wantIssue  string
	}{
		{name: "canonical case fold", repository: "SUBCULT/SWITCHYARD", issue: "1", wantIssue: "1"},
		{name: "explicit alias case fold", repository: "LEGACY/SWITCHYARD", issue: "01", wantIssue: "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			target, resolveErr := registry.ResolveTarget(test.repository, test.issue)
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			if target.RepositoryID != "switchyard" || target.RepositorySlug != "Subcult/Switchyard" || target.Issue != test.wantIssue || target.ExecutorGeneration != "generation-one" || strings.Join(target.RepositoryCandidates, ",") != "legacy/switchyard,subcult/switchyard" {
				t.Fatalf("target = %+v", target)
			}
		})
	}

	if _, err := registry.ResolveTarget("other/repo", "1"); !errors.Is(err, ErrOpenPilotRepositoryUnknown) {
		t.Fatalf("unknown repository error = %v", err)
	}
	if _, err := registry.ResolveTarget(" Subcult/Switchyard", "1"); !errors.Is(err, ErrOpenPilotRepositoryUnknown) {
		t.Fatalf("whitespace repository error = %v", err)
	}

	repository, ok := registry.RepositoryByID("switchyard")
	if !ok {
		t.Fatal("repository not found by ID")
	}
	repository.Aliases[0] = "mutated/repository"
	repository, _ = registry.RepositoryByID("switchyard")
	if repository.Aliases[0] != "legacy/switchyard" {
		t.Fatalf("registry aliases were mutated: %v", repository.Aliases)
	}
}

func TestParseOpenPilotIssue(t *testing.T) {
	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: "1", want: "1"},
		{raw: "01", want: "1"},
		{raw: strings.Repeat("0", 512) + "1", want: "1"},
		{raw: strings.Repeat("0", 512) + "9223372036854775807", want: "9223372036854775807"},
		{raw: "9223372036854775807", want: "9223372036854775807"},
	} {
		t.Run(test.raw, func(t *testing.T) {
			got, err := ParseOpenPilotIssue(test.raw)
			if err != nil || got != test.want {
				t.Fatalf("ParseOpenPilotIssue(%q) = %q, %v", test.raw, got, err)
			}
		})
	}

	for _, raw := range []string{"", "0", "+1", "-1", " 1", "1 ", "1\n", "1.0", "١", "9223372036854775808", "18446744073709551616"} {
		t.Run("reject_"+raw, func(t *testing.T) {
			if got, err := ParseOpenPilotIssue(raw); err == nil {
				t.Fatalf("ParseOpenPilotIssue(%q) = %q, want error", raw, got)
			}
		})
	}
}

func TestValidateOpenPilotRuntimeManifestRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*OpenPilotRuntimeManifest)
	}{
		{name: "version", mutate: func(manifest *OpenPilotRuntimeManifest) { manifest.Version = 2 }},
		{name: "ID", mutate: func(manifest *OpenPilotRuntimeManifest) { manifest.Repositories[0].ID = "Bad_ID" }},
		{name: "unicode slug", mutate: func(manifest *OpenPilotRuntimeManifest) { manifest.Repositories[0].Slug = "subcult/switchyård" }},
		{name: "malformed slug", mutate: func(manifest *OpenPilotRuntimeManifest) { manifest.Repositories[0].Slug = "subcult//switchyard" }},
		{name: "unclean path", mutate: func(manifest *OpenPilotRuntimeManifest) { manifest.Repositories[0].SourcePath = "/srv/../repo" }},
		{name: "endpoint credentials", mutate: func(manifest *OpenPilotRuntimeManifest) {
			manifest.Repositories[0].OpenCodeEndpoint = "http://user:pass@opencode:4096"
		}},
		{name: "secret bytes", mutate: func(manifest *OpenPilotRuntimeManifest) {
			manifest.Repositories[0].SourcePath = "/srv/token=actual-secret"
		}},
		{name: "duplicate ID", mutate: addDuplicateOpenPilotRepository(func(first, second *OpenPilotRuntimeRepository) { second.ID = first.ID })},
		{name: "duplicate repo case fold", mutate: addDuplicateOpenPilotRepository(func(first, second *OpenPilotRuntimeRepository) { second.Slug = strings.ToUpper(first.Slug) })},
		{name: "alias shadows canonical", mutate: addDuplicateOpenPilotRepository(func(first, second *OpenPilotRuntimeRepository) {
			second.Aliases = []string{strings.ToUpper(first.Slug)}
		})},
		{name: "duplicate generation", mutate: addDuplicateOpenPilotRepository(func(first, second *OpenPilotRuntimeRepository) { second.ExecutorGeneration = first.ExecutorGeneration })},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validOpenPilotRuntimeManifest()
			test.mutate(&manifest)
			if err := ValidateOpenPilotRuntimeManifest(manifest); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	manifest := validOpenPilotRuntimeManifest()
	for index := 1; index < maxOpenPilotRepositories; index++ {
		manifest.Repositories = append(manifest.Repositories, OpenPilotRuntimeRepository{
			ID: "repo-" + decimalTestValue(index), Slug: "owner/repo-" + decimalTestValue(index), Aliases: []string{}, ExecutorGeneration: "gen-" + decimalTestValue(index),
		})
	}
	if err := ValidateOpenPilotRuntimeManifest(manifest); err != nil {
		t.Fatalf("32 repositories: %v", err)
	}
	manifest.Repositories = append(manifest.Repositories, OpenPilotRuntimeRepository{ID: "overflow", Slug: "owner/overflow", Aliases: []string{}, ExecutorGeneration: "overflow"})
	if err := ValidateOpenPilotRuntimeManifest(manifest); err == nil {
		t.Fatal("expected 33 repositories to fail")
	}
}

func TestDecodeOpenPilotRuntimeManifestIsStrict(t *testing.T) {
	manifest := validOpenPilotRuntimeManifest()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOpenPilotRuntimeManifest(raw); err != nil {
		t.Fatal(err)
	}

	for name, invalid := range map[string]string{
		"unknown field":      strings.Replace(string(raw), `"version":1`, `"version":1,"unknown":true`, 1),
		"duplicate field":    strings.Replace(string(raw), `"version":1`, `"version":1,"version":1`, 1),
		"trailing JSON":      string(raw) + `{}`,
		"missing aliases":    strings.Replace(string(raw), `,"aliases":["legacy/switchyard"]`, ``, 1),
		"null aliases":       strings.Replace(string(raw), `"aliases":["legacy/switchyard"]`, `"aliases":null`, 1),
		"missing generation": strings.Replace(string(raw), `,"executor_generation":"generation-one"`, ``, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeOpenPilotRuntimeManifest([]byte(invalid)); err == nil {
				t.Fatal("expected strict decode error")
			}
		})
	}

	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadOpenPilotRuntimeManifest(path)
	if err != nil || loaded.Repositories[0].Slug != manifest.Repositories[0].Slug {
		t.Fatalf("loaded = %+v, err = %v", loaded, err)
	}
}

func TestOpenPilotManifestCredentialLocations(t *testing.T) {
	for _, endpoint := range []string{"http://user:pass@opencode:4096", "http://opencode:4096?token=secret", "http://opencode:4096/#access_token=secret"} {
		manifest := validOpenPilotRuntimeManifest()
		manifest.Repositories[0].OpenCodeEndpoint = endpoint
		if err := ValidateOpenPilotRuntimeManifest(manifest); err == nil {
			t.Fatalf("accepted endpoint %q", endpoint)
		}
	}
	for _, path := range []string{"/srv/bearer-secret-value", "/srv/access_token=secret", "/srv/sk-proj-abcdefghijklmnop", "/srv/api-key-abcdefghijklmnop", "/srv/secret-abcdefghijklmnop", "/srv/password-abcdefghijklmnop"} {
		manifest := validOpenPilotRuntimeManifest()
		manifest.Repositories[0].SourcePath = path
		if err := ValidateOpenPilotRuntimeManifest(manifest); err == nil {
			t.Fatalf("accepted credential path %q", path)
		}
	}
	manifest := validOpenPilotRuntimeManifest()
	manifest.Repositories[0].PasswordFile = "/srv/secrets/private/opencode-password"
	if err := ValidateOpenPilotRuntimeManifest(manifest); err != nil {
		t.Fatalf("legitimate password file rejected: %v", err)
	}
}

func TestDecodeOpenPilotOperatorRegistry(t *testing.T) {
	registry := OpenPilotOperatorRegistry{
		Version: 1, Generation: "registry-one",
		Repositories: []OpenPilotOperatorRepository{{
			ID: "switchyard", Slug: "Subcult/Switchyard", Aliases: []string{"legacy/switchyard"}, ExecutorGeneration: "generation-one",
			SourcePath: "/srv/switchyard", WorktreeRoot: "/srv/worktrees", ConfigPath: "/srv/config", DataPath: "/srv/data", PasswordFile: "/run/secrets/opencode-password",
		}},
	}
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeOpenPilotOperatorRegistry(raw); err != nil {
		t.Fatal(err)
	}
	registry.Repositories[0].PasswordFile = "relative/password-file"
	if err := ValidateOpenPilotOperatorRegistry(registry); err == nil {
		t.Fatal("expected relative password file rejection")
	}
}

func TestSynthesizeLegacyOpenPilotRuntimeManifest(t *testing.T) {
	binding := LegacyOpenPilotRuntimeBinding{
		Slug: "Subcult/Switchyard", SourcePath: "/workspace/source", WorktreeRoot: "/workspace/worktrees",
		OpenCodeEndpoint: "http://opencode:4096", VerifierEndpoint: "http://verifier:8080", PasswordFile: "/run/secrets/opencode-password", ExecutorGeneration: "legacy-one",
	}
	manifest, err := SynthesizeLegacyOpenPilotRuntimeManifest(binding)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Repositories) != 1 || manifest.Repositories[0].ID != "legacy" || manifest.Repositories[0].Slug != binding.Slug || manifest.Repositories[0].SourcePath != binding.SourcePath || manifest.RegistryGeneration != binding.ExecutorGeneration {
		t.Fatalf("manifest = %+v", manifest)
	}
	if len(manifest.RegistryFingerprint) != 64 {
		t.Fatalf("fingerprint = %q", manifest.RegistryFingerprint)
	}

	second, err := SynthesizeLegacyOpenPilotRuntimeManifest(binding)
	if err != nil || second.RegistryFingerprint != manifest.RegistryFingerprint {
		t.Fatalf("non-deterministic synthesis: %q != %q, err=%v", second.RegistryFingerprint, manifest.RegistryFingerprint, err)
	}
}

func validOpenPilotRuntimeManifest() OpenPilotRuntimeManifest {
	return OpenPilotRuntimeManifest{
		Version: 1, RegistryGeneration: "registry-one", RegistryFingerprint: strings.Repeat("a", 64),
		Repositories: []OpenPilotRuntimeRepository{{
			ID: "switchyard", Slug: "Subcult/Switchyard", Aliases: []string{"legacy/switchyard"}, ExecutorGeneration: "generation-one",
			SourcePath: "/workspace/source", WorktreeRoot: "/workspace/worktrees", OpenCodeEndpoint: "http://opencode:4096", VerifierEndpoint: "http://verifier:8080", PasswordFile: "/run/secrets/opencode-password",
		}},
	}
}

func addDuplicateOpenPilotRepository(mutate func(first, second *OpenPilotRuntimeRepository)) func(*OpenPilotRuntimeManifest) {
	return func(manifest *OpenPilotRuntimeManifest) {
		second := OpenPilotRuntimeRepository{ID: "second", Slug: "owner/second", Aliases: []string{}, ExecutorGeneration: "generation-two"}
		mutate(&manifest.Repositories[0], &second)
		manifest.Repositories = append(manifest.Repositories, second)
	}
}

func decimalTestValue(value int) string {
	return strconv.Itoa(value)
}
