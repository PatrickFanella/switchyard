package controlplane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	openPilotRepositoryContractVersion = 1
	maxOpenPilotRepositories           = 32
	maxOpenPilotAliases                = 32
	maxOpenPilotManifestBytes          = 256 << 10
)

var (
	openPilotRepositoryIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	openPilotRepositorySlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}/[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	openPilotIssuePattern          = regexp.MustCompile(`^[0-9]+$`)

	// ErrOpenPilotRepositoryUnknown means no registered canonical slug or alias matched.
	ErrOpenPilotRepositoryUnknown = errors.New("open pilot repository is not registered")
)

// OpenPilotOperatorRegistry is the operator-owned, non-secret repository source document.
type OpenPilotOperatorRegistry struct {
	Version      int                           `json:"version"`
	Generation   string                        `json:"generation"`
	Repositories []OpenPilotOperatorRepository `json:"repositories"`
}

// OpenPilotOperatorRepository describes one operator-prepared repository workspace.
type OpenPilotOperatorRepository struct {
	ID                 string   `json:"id"`
	Slug               string   `json:"slug"`
	Aliases            []string `json:"aliases"`
	ExecutorGeneration string   `json:"executor_generation"`
	SourcePath         string   `json:"source_path"`
	WorktreeRoot       string   `json:"worktree_root"`
	ConfigPath         string   `json:"config_path"`
	DataPath           string   `json:"data_path"`
	PasswordFile       string   `json:"password_file"`
}

// OpenPilotRuntimeManifest is the generated, read-only repository view consumed by the runtime.
type OpenPilotRuntimeManifest struct {
	Version             int                          `json:"version"`
	RegistryGeneration  string                       `json:"registry_generation"`
	RegistryFingerprint string                       `json:"registry_fingerprint"`
	Repositories        []OpenPilotRuntimeRepository `json:"repositories"`
}

// OpenPilotRuntimeRepository identifies one immutable repository executor generation.
type OpenPilotRuntimeRepository struct {
	ID                 string   `json:"id"`
	Slug               string   `json:"slug"`
	Aliases            []string `json:"aliases"`
	ExecutorGeneration string   `json:"executor_generation"`
	SourcePath         string   `json:"source_path,omitempty"`
	WorktreeRoot       string   `json:"worktree_root,omitempty"`
	OpenCodeEndpoint   string   `json:"opencode_endpoint,omitempty"`
	VerifierEndpoint   string   `json:"verifier_endpoint,omitempty"`
	PasswordFile       string   `json:"password_file,omitempty"`
}

// OpenPilotTarget is the canonical durable identity and immutable executor affinity for a run.
type OpenPilotTarget struct {
	RepositoryID         string
	RepositorySlug       string
	RepositoryCandidates []string
	Issue                string
	ExecutorGeneration   string
}

// LegacyOpenPilotRuntimeBinding contains the exact singleton values used to synthesize a manifest.
type LegacyOpenPilotRuntimeBinding struct {
	Slug               string
	SourcePath         string
	WorktreeRoot       string
	OpenCodeEndpoint   string
	VerifierEndpoint   string
	PasswordFile       string
	ExecutorGeneration string
}

// OpenPilotRepositoryRegistry resolves external repository names to immutable manifest entries.
type OpenPilotRepositoryRegistry struct {
	manifest OpenPilotRuntimeManifest
	byName   map[string]int
	byID     map[string]int
}

// LoadOpenPilotRuntimeManifest reads and strictly validates a runtime manifest.
func LoadOpenPilotRuntimeManifest(path string) (OpenPilotRuntimeManifest, error) {
	raw, err := readBoundedOpenPilotManifest(path)
	if err != nil {
		return OpenPilotRuntimeManifest{}, err
	}
	return DecodeOpenPilotRuntimeManifest(raw)
}

// DecodeOpenPilotRuntimeManifest strictly decodes and validates one runtime manifest document.
func DecodeOpenPilotRuntimeManifest(raw []byte) (OpenPilotRuntimeManifest, error) {
	var manifest OpenPilotRuntimeManifest
	if err := decodeOpenPilotRepositoryDocument(raw, &manifest); err != nil {
		return OpenPilotRuntimeManifest{}, fmt.Errorf("decode open pilot runtime manifest: %w", err)
	}
	if err := ValidateOpenPilotRuntimeManifest(manifest); err != nil {
		return OpenPilotRuntimeManifest{}, err
	}
	return manifest, nil
}

// DecodeOpenPilotOperatorRegistry strictly decodes and validates one operator registry document.
func DecodeOpenPilotOperatorRegistry(raw []byte) (OpenPilotOperatorRegistry, error) {
	var registry OpenPilotOperatorRegistry
	if err := decodeOpenPilotRepositoryDocument(raw, &registry); err != nil {
		return OpenPilotOperatorRegistry{}, fmt.Errorf("decode open pilot operator registry: %w", err)
	}
	if err := ValidateOpenPilotOperatorRegistry(registry); err != nil {
		return OpenPilotOperatorRegistry{}, err
	}
	return registry, nil
}

// ValidateOpenPilotOperatorRegistry checks semantic constraints not expressible in JSON Schema.
func ValidateOpenPilotOperatorRegistry(registry OpenPilotOperatorRegistry) error {
	if registry.Version != openPilotRepositoryContractVersion || !validOpenPilotGeneration(registry.Generation) {
		return errors.New("invalid open pilot operator registry version or generation")
	}
	if openPilotDocumentContainsSecret(registry) {
		return errors.New("open pilot operator registry contains secret-looking material")
	}
	entries := make([]openPilotRepositoryIdentity, 0, len(registry.Repositories))
	for index, repository := range registry.Repositories {
		if !validAbsoluteOpenPilotPath(repository.SourcePath) ||
			!validAbsoluteOpenPilotPath(repository.WorktreeRoot) ||
			!validAbsoluteOpenPilotPath(repository.ConfigPath) ||
			!validAbsoluteOpenPilotPath(repository.DataPath) ||
			!validAbsoluteOpenPilotPath(repository.PasswordFile) {
			return fmt.Errorf("invalid open pilot operator repository %d path", index)
		}
		entries = append(entries, openPilotRepositoryIdentity{
			id: repository.ID, slug: repository.Slug, aliases: repository.Aliases, generation: repository.ExecutorGeneration,
		})
	}
	if err := validateOpenPilotRepositoryIdentities(entries); err != nil {
		return fmt.Errorf("invalid open pilot operator registry: %w", err)
	}
	return nil
}

// ValidateOpenPilotRuntimeManifest checks semantic constraints not expressible in JSON Schema.
func ValidateOpenPilotRuntimeManifest(manifest OpenPilotRuntimeManifest) error {
	if manifest.Version != openPilotRepositoryContractVersion || !validOpenPilotGeneration(manifest.RegistryGeneration) {
		return errors.New("invalid open pilot runtime manifest version or generation")
	}
	if openPilotDocumentContainsSecret(manifest) {
		return errors.New("open pilot runtime manifest contains secret-looking material")
	}
	if len(manifest.RegistryFingerprint) != sha256.Size*2 {
		return errors.New("invalid open pilot runtime manifest fingerprint")
	}
	if _, err := hex.DecodeString(manifest.RegistryFingerprint); err != nil || manifest.RegistryFingerprint != strings.ToLower(manifest.RegistryFingerprint) {
		return errors.New("invalid open pilot runtime manifest fingerprint")
	}
	entries := make([]openPilotRepositoryIdentity, 0, len(manifest.Repositories))
	for index, repository := range manifest.Repositories {
		for _, path := range []string{repository.SourcePath, repository.WorktreeRoot, repository.PasswordFile} {
			if path != "" && !validAbsoluteOpenPilotPath(path) {
				return fmt.Errorf("invalid open pilot runtime repository %d path", index)
			}
		}
		for _, endpoint := range []string{repository.OpenCodeEndpoint, repository.VerifierEndpoint} {
			if endpoint != "" && !validOpenPilotEndpoint(endpoint) {
				return fmt.Errorf("invalid open pilot runtime repository %d endpoint", index)
			}
		}
		entries = append(entries, openPilotRepositoryIdentity{
			id: repository.ID, slug: repository.Slug, aliases: repository.Aliases, generation: repository.ExecutorGeneration,
		})
	}
	if err := validateOpenPilotRepositoryIdentities(entries); err != nil {
		return fmt.Errorf("invalid open pilot runtime manifest: %w", err)
	}
	return nil
}

// NewOpenPilotRepositoryRegistry creates an immutable resolver from a validated manifest.
func NewOpenPilotRepositoryRegistry(manifest OpenPilotRuntimeManifest) (*OpenPilotRepositoryRegistry, error) {
	if err := ValidateOpenPilotRuntimeManifest(manifest); err != nil {
		return nil, err
	}
	copyManifest := manifest
	copyManifest.Repositories = append([]OpenPilotRuntimeRepository(nil), manifest.Repositories...)
	registry := &OpenPilotRepositoryRegistry{
		manifest: copyManifest,
		byName:   make(map[string]int),
		byID:     make(map[string]int),
	}
	for index := range copyManifest.Repositories {
		repository := &copyManifest.Repositories[index]
		repository.Aliases = append([]string(nil), repository.Aliases...)
		registry.byID[repository.ID] = index
		registry.byName[foldOpenPilotRepositoryName(repository.Slug)] = index
		for _, alias := range repository.Aliases {
			registry.byName[foldOpenPilotRepositoryName(alias)] = index
		}
	}
	return registry, nil
}

// ResolveTarget canonicalizes a repository alias and positive decimal issue number.
func (r *OpenPilotRepositoryRegistry) ResolveTarget(rawRepository, rawIssue string) (OpenPilotTarget, error) {
	if r == nil || !validOpenPilotRepositorySlug(rawRepository) {
		return OpenPilotTarget{}, ErrOpenPilotRepositoryUnknown
	}
	index, ok := r.byName[foldOpenPilotRepositoryName(rawRepository)]
	if !ok {
		return OpenPilotTarget{}, ErrOpenPilotRepositoryUnknown
	}
	issue, err := ParseOpenPilotIssue(rawIssue)
	if err != nil {
		return OpenPilotTarget{}, err
	}
	repository := r.manifest.Repositories[index]
	candidates := make([]string, 0, len(repository.Aliases)+1)
	for _, name := range append([]string{repository.Slug}, repository.Aliases...) {
		candidates = append(candidates, foldOpenPilotRepositoryName(name))
	}
	sort.Strings(candidates)
	return OpenPilotTarget{
		RepositoryID:         repository.ID,
		RepositorySlug:       repository.Slug,
		RepositoryCandidates: candidates,
		Issue:                issue,
		ExecutorGeneration:   repository.ExecutorGeneration,
	}, nil
}

// RepositoryByID returns a defensive copy of one immutable runtime entry.
func (r *OpenPilotRepositoryRegistry) RepositoryByID(id string) (OpenPilotRuntimeRepository, bool) {
	if r == nil {
		return OpenPilotRuntimeRepository{}, false
	}
	index, ok := r.byID[id]
	if !ok {
		return OpenPilotRuntimeRepository{}, false
	}
	repository := r.manifest.Repositories[index]
	repository.Aliases = append([]string(nil), repository.Aliases...)
	return repository, true
}

func (r *OpenPilotRepositoryRegistry) Repositories() []OpenPilotRuntimeRepository {
	if r == nil {
		return []OpenPilotRuntimeRepository{}
	}
	out := make([]OpenPilotRuntimeRepository, len(r.manifest.Repositories))
	for index, repository := range r.manifest.Repositories {
		out[index] = OpenPilotRuntimeRepository{ID: repository.ID, Slug: repository.Slug, Aliases: append([]string(nil), repository.Aliases...), ExecutorGeneration: repository.ExecutorGeneration}
	}
	return out
}

// ParseOpenPilotIssue returns the canonical positive unsigned decimal issue number.
func ParseOpenPilotIssue(raw string) (string, error) {
	if !openPilotIssuePattern.MatchString(raw) {
		return "", errors.New("open pilot issue must be an unsigned ASCII decimal integer")
	}
	canonical := strings.TrimLeft(raw, "0")
	if canonical == "" {
		return "", errors.New("open pilot issue must be a positive signed 64-bit decimal integer")
	}
	issue, err := strconv.ParseUint(canonical, 10, 63)
	if err != nil || issue == 0 {
		return "", errors.New("open pilot issue must be a positive signed 64-bit decimal integer")
	}
	return strconv.FormatUint(issue, 10), nil
}

// ParseOpenPilotRepositorySlug validates and preserves an exact ASCII owner/repository slug.
func ParseOpenPilotRepositorySlug(raw string) (string, error) {
	if !validOpenPilotRepositorySlug(raw) {
		return "", errors.New("open pilot repository must be an ASCII owner/repository slug")
	}
	return raw, nil
}

// SynthesizeLegacyOpenPilotRuntimeManifest builds exactly one compatibility entry.
func SynthesizeLegacyOpenPilotRuntimeManifest(binding LegacyOpenPilotRuntimeBinding) (OpenPilotRuntimeManifest, error) {
	manifest := OpenPilotRuntimeManifest{
		Version:            openPilotRepositoryContractVersion,
		RegistryGeneration: binding.ExecutorGeneration,
		Repositories: []OpenPilotRuntimeRepository{{
			ID:                 "legacy",
			Slug:               binding.Slug,
			Aliases:            []string{},
			ExecutorGeneration: binding.ExecutorGeneration,
			SourcePath:         binding.SourcePath,
			WorktreeRoot:       binding.WorktreeRoot,
			OpenCodeEndpoint:   binding.OpenCodeEndpoint,
			VerifierEndpoint:   binding.VerifierEndpoint,
			PasswordFile:       binding.PasswordFile,
		}},
	}
	fingerprintInput, err := json.Marshal(manifest)
	if err != nil {
		return OpenPilotRuntimeManifest{}, fmt.Errorf("encode legacy open pilot runtime manifest: %w", err)
	}
	fingerprint := sha256.Sum256(fingerprintInput)
	manifest.RegistryFingerprint = hex.EncodeToString(fingerprint[:])
	if err := ValidateOpenPilotRuntimeManifest(manifest); err != nil {
		return OpenPilotRuntimeManifest{}, err
	}
	return manifest, nil
}

type openPilotRepositoryIdentity struct {
	id         string
	slug       string
	aliases    []string
	generation string
}

func validateOpenPilotRepositoryIdentities(entries []openPilotRepositoryIdentity) error {
	if len(entries) == 0 || len(entries) > maxOpenPilotRepositories {
		return fmt.Errorf("repository count must be between 1 and %d", maxOpenPilotRepositories)
	}
	ids := make(map[string]struct{}, len(entries))
	generations := make(map[string]struct{}, len(entries))
	names := make(map[string]struct{}, len(entries)*(maxOpenPilotAliases+1))
	for _, entry := range entries {
		if !openPilotRepositoryIDPattern.MatchString(entry.id) || !validOpenPilotRepositorySlug(entry.slug) || !validOpenPilotGeneration(entry.generation) {
			return errors.New("repository identity is malformed")
		}
		if len(entry.aliases) > maxOpenPilotAliases {
			return fmt.Errorf("repository alias count exceeds %d", maxOpenPilotAliases)
		}
		if _, exists := ids[entry.id]; exists {
			return errors.New("repository IDs must be unique")
		}
		ids[entry.id] = struct{}{}
		if _, exists := generations[entry.generation]; exists {
			return errors.New("executor generations must be unique")
		}
		generations[entry.generation] = struct{}{}
		for _, name := range append([]string{entry.slug}, entry.aliases...) {
			if !validOpenPilotRepositorySlug(name) {
				return errors.New("repository alias is malformed")
			}
			folded := foldOpenPilotRepositoryName(name)
			if _, exists := names[folded]; exists {
				return errors.New("case-folded repository slugs and aliases must be unique")
			}
			names[folded] = struct{}{}
		}
	}
	return nil
}

func readBoundedOpenPilotManifest(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat open pilot runtime manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxOpenPilotManifestBytes {
		return nil, errors.New("open pilot runtime manifest must be a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read open pilot runtime manifest: %w", err)
	}
	return raw, nil
}

func decodeOpenPilotRepositoryDocument(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > maxOpenPilotManifestBytes || !utf8.Valid(raw) {
		return errors.New("document must be bounded non-empty UTF-8")
	}
	if err := validateUniqueJSON(raw); err != nil {
		return err
	}
	if err := requireOpenPilotRepositoryFields(raw, destination); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return err
	}
	if openPilotDocumentContainsSecret(destination) {
		return errors.New("document contains secret-looking material")
	}
	return nil
}

func requireOpenPilotRepositoryFields(raw []byte, destination any) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	rootFields := []string{"version", "repositories"}
	entryFields := []string{"id", "slug", "aliases", "executor_generation"}
	switch destination.(type) {
	case *OpenPilotRuntimeManifest:
		rootFields = append(rootFields, "registry_generation", "registry_fingerprint")
	case *OpenPilotOperatorRegistry:
		rootFields = append(rootFields, "generation")
		entryFields = append(entryFields, "source_path", "worktree_root", "config_path", "data_path", "password_file")
	default:
		return errors.New("unsupported open pilot repository document")
	}
	for _, field := range rootFields {
		if value, ok := root[field]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q is missing or null", field)
		}
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(root["repositories"], &entries); err != nil {
		return err
	}
	for index, entry := range entries {
		for _, field := range entryFields {
			if value, ok := entry[field]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("repository %d required field %q is missing or null", index, field)
			}
		}
	}
	return nil
}

func openPilotDocumentContainsSecret(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	return embeddedSecretPattern.Match(raw)
}

func validOpenPilotGeneration(value string) bool {
	return openPilotRepositoryIDPattern.MatchString(value)
}

func validOpenPilotRepositorySlug(value string) bool {
	return openPilotRepositorySlugPattern.MatchString(value)
}

func foldOpenPilotRepositoryName(value string) string {
	return strings.ToLower(value)
}

func validAbsoluteOpenPilotPath(value string) bool {
	return len(value) <= 4096 && filepath.IsAbs(value) && filepath.Clean(value) == value && !containsOpenPilotControl(value) && !credentialLikeOpenPilotLocation(value)
}

func validOpenPilotEndpoint(value string) bool {
	if len(value) > 2048 || containsOpenPilotControl(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	return parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && !credentialLikeOpenPilotLocation(value)
}

func credentialLikeOpenPilotLocation(value string) bool {
	decoded, err := url.PathUnescape(value)
	if err != nil {
		return true
	}
	lower := strings.ToLower(decoded)
	if embeddedSecretPattern.MatchString(decoded) || strings.Contains(lower, "bearer ") || strings.Contains(lower, "bearer:") {
		return true
	}
	for _, segment := range strings.FieldsFunc(lower, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(segment, "sk-") && len(segment) >= 15 {
			return true
		}
		for _, prefix := range []string{"bearer-", "bearer_", "token-", "token_", "access-token-", "access_token_", "api-key-", "api_key_", "apikey-", "apikey_", "secret-", "secret_", "password-", "password_"} {
			if strings.HasPrefix(segment, prefix) && len(segment) > len(prefix)+5 {
				return true
			}
		}
		if strings.Contains(segment, "@") && strings.Contains(segment, ":") {
			return true
		}
	}
	return false
}

func containsOpenPilotControl(value string) bool {
	for _, character := range []byte(value) {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}
