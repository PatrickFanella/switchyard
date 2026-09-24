package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testProfile = "go1.26.6-node24-pnpm10.33.0-v2"

func TestPreparedDependenciesRejectDriftAndMissingInputs(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "lock")
	if err := os.WriteFile(p, []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned")))
	files := map[string]*string{"lock": &hash, "absent": nil}
	if err := validatePreparedFiles(root, files); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedFiles(root, files); err == nil || !strings.Contains(err.Error(), "stale prepared dependency input lock") {
		t.Fatalf("accepted stale dependencies: %v", err)
	}
	_ = os.Remove(p)
	if err := validatePreparedFiles(root, files); err == nil || !strings.Contains(err.Error(), "missing prepared dependency input lock") {
		t.Fatalf("accepted missing input: %v", err)
	}
	_ = os.WriteFile(p, []byte("pinned"), 0600)
	_ = os.WriteFile(filepath.Join(root, "absent"), []byte("new"), 0600)
	if err := validatePreparedFiles(root, files); err == nil || !strings.Contains(err.Error(), "unexpected dependency input absent") {
		t.Fatalf("accepted newly introduced configuration: %v", err)
	}
	if err := validatePreparedFiles(root, map[string]*string{"../escape": &hash}); err == nil {
		t.Fatal("accepted escaping path")
	}
}

func TestPreparedProfileReusesDependenciesAcrossSourceRevisions(t *testing.T) {
	profiles := mustLoadPreparedProfiles()
	root := t.TempDir()
	files := map[string]*string{}
	for _, name := range profiles.Profiles[testProfile].Inputs {
		files[name] = nil
	}
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0700); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("pinned")))
	files["backend/go.mod"] = &hash
	if err := os.WriteFile(filepath.Join(root, "backend/go.mod"), []byte("pinned"), 0600); err != nil {
		t.Fatal(err)
	}
	m := preparedManifest{Version: 2, Profile: testProfile, BaseSHA: strings.Repeat("a", 40), Files: files, Commands: [][]string{{"make", "verify"}}}
	head := strings.Repeat("b", 40)
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(profiles, m, head, root, []string{"sh"}); err == nil || !strings.Contains(err.Error(), "supported: make verify") {
		t.Fatalf("accepted unsupported command: %v", err)
	}
	m.Version = 1
	m.Profile = "go1.26.6-node24-pnpm10.33.0-v1"
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil || !strings.Contains(err.Error(), "revision mismatch") {
		t.Fatalf("relaxed legacy image binding: %v", err)
	}
	m.Version = 1
	m.Profile = testProfile
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("accepted profile with mismatched manifest version")
	}
	m.Version = 2
	m.Profile = "go1.99-unknown"
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil || !strings.Contains(err.Error(), "unsupported prepared verification profile") {
		t.Fatalf("accepted unknown profile: %v", err)
	}
	m.Profile = testProfile
	m.Commands = [][]string{{"make", "verify"}, {"sh", "-c", "curl example.invalid"}}
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil || !strings.Contains(err.Error(), "not supported by profile") {
		t.Fatalf("manifest widened the profile command allowlist: %v", err)
	}
	m.Commands = [][]string{{"make", "verify"}}
	delete(m.Files, ".npmrc")
	m.Files["unrelated"] = nil
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("accepted incomplete dependency contract")
	}
	delete(m.Files, "unrelated")
	m.Files[".npmrc"] = nil
	if err := os.WriteFile(filepath.Join(root, "backend/go.mod"), []byte("new dependency"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validatePreparedManifest(profiles, m, head, root, []string{"make", "verify"}); err == nil {
		t.Fatal("accepted dependency change across revisions")
	}
}

func TestEmbeddedProfilesMatchPreparationScript(t *testing.T) {
	profiles := mustLoadPreparedProfiles()
	profile, ok := profiles.Profiles[testProfile]
	if !ok || profile.ManifestVersion != 2 || profile.Reuse != "dependency-inputs" {
		t.Fatalf("current profile = %+v", profile)
	}
	if !reflect.DeepEqual(profile.Commands, [][]string{{"make", "verify"}}) {
		t.Fatalf("commands = %v", profile.Commands)
	}
	for _, name := range profile.PreparedInputs {
		if !containsString(profile.Inputs, name) {
			t.Fatalf("prepared input %s is not bound", name)
		}
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "prepare_verifier.py"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(script, []byte("backend/cmd/verifier/profiles.json")) {
		t.Fatal("scripts/prepare_verifier.py must read the embedded profile contract")
	}
}

func TestPreparedStatusReportsProfileAndMissingBundle(t *testing.T) {
	profiles := mustLoadPreparedProfiles()
	status := preparedStatusFor("", profiles)
	if status.Prepared || !strings.Contains(status.Error, "no prepared verification bundle") || len(status.Profiles) == 0 {
		t.Fatalf("unprepared status = %+v", status)
	}
	bundle := t.TempDir()
	if s := preparedStatusFor(bundle, profiles); s.Prepared || !strings.Contains(s.Error, "read prepared verification manifest") {
		t.Fatalf("missing manifest status = %+v", s)
	}
	files := map[string]*string{}
	for _, name := range profiles.Profiles[testProfile].Inputs {
		files[name] = nil
	}
	writePreparedManifest(t, bundle, preparedManifest{Version: 2, Profile: testProfile, BaseSHA: strings.Repeat("c", 40), Files: files, Commands: [][]string{{"make", "verify"}}})
	recorder := httptest.NewRecorder()
	newPreparedHandler(verifierConfig{bundle: bundle, profiles: profiles}).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/prepared", nil))
	var decoded preparedStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Prepared || decoded.Profile != testProfile || decoded.Toolchains["go"] != "1.26.6" || decoded.Toolchains["pnpm"] != "10.33.0" || !reflect.DeepEqual(decoded.Commands, [][]string{{"make", "verify"}}) || len(decoded.Inputs) != 11 {
		t.Fatalf("prepared status = %s", recorder.Body.String())
	}
	writePreparedManifest(t, bundle, preparedManifest{Version: 2, Profile: "go1.99-unknown", BaseSHA: strings.Repeat("c", 40), Files: files})
	if s := preparedStatusFor(bundle, profiles); s.Prepared || !strings.Contains(s.Error, "not supported by this verifier") {
		t.Fatalf("unsupported status = %+v", s)
	}
}

func TestPreparedEnvironmentPinsBothPnpmConfigurationPrefixes(t *testing.T) {
	env := strings.Join(preparedEnvironment("/bundle", "/cache/attempt"), "\n")
	for _, want := range []string{"GOPROXY=off", "GOTOOLCHAIN=local", "GOMODCACHE=/bundle/go", "npm_config_offline=true", "pnpm_config_offline=true", "npm_config_store_dir=/cache/attempt/pnpm", "pnpm_config_store_dir=/cache/attempt/pnpm"} {
		if !strings.Contains(env, want) {
			t.Fatalf("prepared environment lacks %s:\n%s", want, env)
		}
	}
}

// Fixture repository checks. Preparation (fixture proxy and registry access) runs in
// the test before any verification request, standing in for the operator image build.
// Verification then runs through the real handler with GOPROXY=off and pnpm offline.

const fixtureMakefile = "verify:\n\tcd backend && go test ./...\n\tpnpm --dir web install --frozen-lockfile\n\tpnpm --dir web test\n"

func TestPreparedGoFixturePassesOfflineAndRejectsStaleDependencies(t *testing.T) {
	requireTools(t, "go", "make", "git")
	hermeticToolEnvironment(t)
	proxy := goFixtureProxy(t, "v1.0.0", "v1.1.0")
	repo, root, workdir := verifierTestWorktree(t)
	writeFixtureFile(t, workdir, "Makefile", "verify:\n\tcd backend && go test ./...\n")
	writeGoFixture(t, workdir, "v1.0.0")
	bundle := t.TempDir()
	prepareGoModules(t, filepath.Join(workdir, "backend"), proxy, filepath.Join(bundle, "go"))
	if err := os.MkdirAll(filepath.Join(bundle, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := worktreeHead(t, workdir)
	writePreparedManifest(t, bundle, preparedManifestFor(t, workdir, head))
	cacheRoot := t.TempDir()
	handler := preparedTestHandler(root, filepath.Join(repo, ".git"), bundle, cacheRoot)

	response := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if response.status != http.StatusOK || !strings.Contains(response.Output, "ok  \tfixture.test/backend") {
		t.Fatalf("Go fixture status = %d, body = %+v", response.status, response.verifyResponse)
	}
	if response.Prepared == nil || response.Prepared.Profile != testProfile || response.Prepared.BaseSHA != head || response.Manifest == "" {
		t.Fatalf("result is not bound to its preparation and source: %+v", response.verifyResponse)
	}
	assertDisposableOutputsRemoved(t, cacheRoot, workdir)

	unsupported := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"go", "test", "./..."}, Workdir: workdir, Timeout: "1m"}))
	if unsupported.status != http.StatusUnprocessableEntity || !strings.Contains(unsupported.Error, "supported: make verify") {
		t.Fatalf("unsupported command = %d %+v", unsupported.status, unsupported.verifyResponse)
	}

	// A dependency upgrade committed after preparation is rejected before any command runs.
	elsewhere := t.TempDir()
	writeGoFixture(t, workdir, "v1.1.0")
	prepareGoModules(t, filepath.Join(workdir, "backend"), proxy, elsewhere)
	stale := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if stale.status != http.StatusUnprocessableEntity || !strings.Contains(stale.Error, "stale prepared dependency input backend/go.mod") || stale.Output != "" {
		t.Fatalf("stale dependency status = %d %+v", stale.status, stale.verifyResponse)
	}
	assertDisposableOutputsRemoved(t, cacheRoot, workdir)

	// Even a manifest rebound to the new inputs without re-preparing cannot pass on the
	// old module cache: the offline toolchain fails instead of silently reusing v1.0.0.
	writePreparedManifest(t, bundle, preparedManifestFor(t, workdir, head))
	rebound := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if rebound.status != http.StatusUnprocessableEntity || rebound.Error != "command failed" || !strings.Contains(rebound.Output, "GOPROXY=off") {
		t.Fatalf("stale module cache status = %d %+v", rebound.status, rebound.verifyResponse)
	}
}

func TestPreparedGoFixtureMissingDependencyFailsOffline(t *testing.T) {
	requireTools(t, "go", "make", "git")
	hermeticToolEnvironment(t)
	proxy := goFixtureProxy(t, "v1.0.0")
	repo, root, workdir := verifierTestWorktree(t)
	writeFixtureFile(t, workdir, "Makefile", "verify:\n\tcd backend && go test ./...\n")
	writeGoFixture(t, workdir, "v1.0.0")
	prepareGoModules(t, filepath.Join(workdir, "backend"), proxy, t.TempDir())
	bundle := t.TempDir() // Prepared without downloading the module.
	for _, name := range []string{"go", "pnpm"} {
		if err := os.MkdirAll(filepath.Join(bundle, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writePreparedManifest(t, bundle, preparedManifestFor(t, workdir, worktreeHead(t, workdir)))
	cacheRoot := t.TempDir()
	handler := preparedTestHandler(root, filepath.Join(repo, ".git"), bundle, cacheRoot)
	response := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if response.status != http.StatusUnprocessableEntity || response.Error != "command failed" || !strings.Contains(response.Output, "example.test/dep") || !strings.Contains(response.Output, "GOPROXY=off") {
		t.Fatalf("missing Go dependency status = %d %+v", response.status, response.verifyResponse)
	}
	assertDisposableOutputsRemoved(t, cacheRoot, workdir)
}

func TestPreparedNodeFixturePassesOfflineAndMissingStoreFails(t *testing.T) {
	requireTools(t, "go", "make", "git", "node", "pnpm")
	hermeticToolEnvironment(t)
	registry := npmFixtureRegistry(t)
	repo, root, workdir := verifierTestWorktree(t)
	writeFixtureFile(t, workdir, "Makefile", fixtureMakefile)
	writeFixtureFile(t, workdir, "backend/go.mod", "module fixture.test/backend\n\ngo 1.22\n")
	writeFixtureFile(t, workdir, "backend/backend_test.go", "package backend\n\nimport \"testing\"\n\nfunc TestFixture(t *testing.T) {}\n")
	writeFixtureFile(t, workdir, "web/package.json", `{"name":"node-fixture","version":"1.0.0","private":true,"scripts":{"test":"node test.js"},"dependencies":{"fixture-dep":"1.0.0"}}`+"\n")
	writeFixtureFile(t, workdir, "web/test.js", "const dep = require('fixture-dep');\nif (dep.answer !== 42) { throw new Error('wrong dependency'); }\nconsole.log('node fixture ok');\n")
	store := t.TempDir()
	preparePnpmStore(t, filepath.Join(workdir, "web"), registry.URL, store)
	registry.Close() // Verification must not depend on the registry.
	head := worktreeHead(t, workdir)

	prepared := t.TempDir()
	copyTree(t, store, filepath.Join(prepared, "pnpm"))
	if err := os.MkdirAll(filepath.Join(prepared, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	writePreparedManifest(t, prepared, preparedManifestFor(t, workdir, head))
	cacheRoot := t.TempDir()
	handler := preparedTestHandler(root, filepath.Join(repo, ".git"), prepared, cacheRoot)
	response := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if response.status != http.StatusOK || !strings.Contains(response.Output, "node fixture ok") {
		t.Fatalf("Node fixture status = %d, body = %+v", response.status, response.verifyResponse)
	}
	assertDisposableOutputsRemoved(t, cacheRoot, workdir)

	missing := t.TempDir()
	for _, name := range []string{"go", "pnpm"} {
		if err := os.MkdirAll(filepath.Join(missing, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writePreparedManifest(t, missing, preparedManifestFor(t, workdir, head))
	handler = preparedTestHandler(root, filepath.Join(repo, ".git"), missing, cacheRoot)
	failed := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if failed.status != http.StatusUnprocessableEntity || failed.Error != "command failed" || strings.Contains(failed.Output, "node fixture ok") || !strings.Contains(strings.ToLower(failed.Output), "offline") {
		t.Fatalf("missing Node dependency status = %d %+v", failed.status, failed.verifyResponse)
	}
	assertDisposableOutputsRemoved(t, cacheRoot, workdir)

	// Changing the lockfile after preparation is rejected before pnpm runs.
	lockfile := filepath.Join(workdir, "web", "pnpm-lock.yaml")
	data, err := os.ReadFile(lockfile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockfile, append(data, []byte("\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	handler = preparedTestHandler(root, filepath.Join(repo, ".git"), prepared, cacheRoot)
	stale := decodeVerify(t, callVerifyWithContext(t, handler, context.Background(), verifyRequest{Command: []string{"make", "verify"}, Workdir: workdir, Timeout: "5m"}))
	if stale.status != http.StatusUnprocessableEntity || !strings.Contains(stale.Error, "stale prepared dependency input web/pnpm-lock.yaml") || stale.Output != "" {
		t.Fatalf("stale lockfile status = %d %+v", stale.status, stale.verifyResponse)
	}
}

type decodedVerify struct {
	verifyResponse
	status int
}

func decodeVerify(t *testing.T, recorder *httptest.ResponseRecorder) decodedVerify {
	t.Helper()
	var response verifyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return decodedVerify{verifyResponse: response, status: recorder.Code}
}

func preparedTestHandler(root, common, bundle, cacheRoot string) http.Handler {
	return newVerifyHandler(verifierConfig{
		bundle: bundle, cacheRoot: cacheRoot, profiles: mustLoadPreparedProfiles(),
		worktreeRoot: root, gitCommonDir: common, outputBytes: 1 << 20,
		snapshotFiles: 1_000, snapshotBytes: 16 << 20, limiter: make(chan struct{}, 1),
	})
}

func requireTools(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required for prepared fixture verification: %v", name, err)
		}
	}
}

// hermeticToolEnvironment keeps ambient user caches and configuration out of both
// the simulated preparation and the verifier's inherited allowlisted variables.
func hermeticToolEnvironment(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	// XDG directories stay ambient: host package-manager shims resolve their own
	// installation there. Store, cache, and registry settings are pinned explicitly.
	for name, value := range map[string]string{
		"GOCACHE": filepath.Join(home, "go-build"), "GOMODCACHE": filepath.Join(home, "gomod"), "GOTMPDIR": "",
		"GOFLAGS": "", "GOPROXY": "off", "GONOSUMDB": "", "GOPRIVATE": "", "PNPM_HOME": "", "npm_config_cache": filepath.Join(home, "npm"),
	} {
		t.Setenv(name, value)
	}
}

func writeFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGoFixture(t *testing.T, workdir, version string) {
	t.Helper()
	writeFixtureFile(t, workdir, "backend/go.mod", "module fixture.test/backend\n\ngo 1.22\n\nrequire example.test/dep "+version+"\n")
	writeFixtureFile(t, workdir, "backend/backend_test.go", "package backend\n\nimport (\n\t\"testing\"\n\n\t\"example.test/dep\"\n)\n\nfunc TestFixture(t *testing.T) {\n\tif dep.Version() == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n")
	_ = os.Remove(filepath.Join(workdir, "backend", "go.sum"))
}

// goFixtureProxy writes a file:// GOPROXY holding example.test/dep at the given versions.
func goFixtureProxy(t *testing.T, versions ...string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "example.test", "dep", "@v")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	modFile := "module example.test/dep\n\ngo 1.22\n"
	for _, version := range versions {
		writeFixtureFile(t, directory, version+".info", `{"Version":"`+version+`","Time":"2024-01-01T00:00:00Z"}`)
		writeFixtureFile(t, directory, version+".mod", modFile)
		var archive bytes.Buffer
		writer := zip.NewWriter(&archive)
		for name, contents := range map[string]string{"go.mod": modFile, "dep.go": "package dep\n\nfunc Version() string { return \"" + version + "\" }\n"} {
			entry, err := writer.Create("example.test/dep@" + version + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = entry.Write([]byte(contents))
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, version+".zip"), archive.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureFile(t, directory, "list", strings.Join(versions, "\n")+"\n")
	return "file://" + filepath.ToSlash(root)
}

func prepareGoModules(t *testing.T, backend, proxy, modCache string) {
	t.Helper()
	command := exec.Command("go", "mod", "tidy")
	command.Dir = backend
	command.Env = append(os.Environ(), "GOPROXY="+proxy, "GOSUMDB=off", "GOFLAGS=-modcacherw", "GOMODCACHE="+modCache, "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare Go modules: %v\n%s", err, output)
	}
}

type fixtureRegistry struct {
	*httptest.Server
}

// npmFixtureRegistry serves one package so preparation can populate a real pnpm store.
func npmFixtureRegistry(t *testing.T) fixtureRegistry {
	t.Helper()
	var tarball bytes.Buffer
	gz := gzip.NewWriter(&tarball)
	archive := tar.NewWriter(gz)
	for name, contents := range map[string]string{
		"package/package.json": `{"name":"fixture-dep","version":"1.0.0","main":"index.js"}`,
		"package/index.js":     "module.exports = { answer: 42 };\n",
	} {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = archive.Write([]byte(contents))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	data := tarball.Bytes()
	sha512sum := sha512.Sum512(data)
	sha1sum := sha1.Sum(data)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(sha512sum[:])
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixture-dep":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "fixture-dep", "dist-tags": map[string]string{"latest": "1.0.0"},
				"time":     map[string]string{"created": "2024-01-01T00:00:00.000Z", "modified": "2024-01-01T00:00:00.000Z", "1.0.0": "2024-01-01T00:00:00.000Z"},
				"versions": map[string]any{"1.0.0": map[string]any{"name": "fixture-dep", "version": "1.0.0", "main": "index.js", "dist": map[string]string{"tarball": server.URL + "/fixture-dep/-/fixture-dep-1.0.0.tgz", "integrity": integrity, "shasum": hex.EncodeToString(sha1sum[:])}}},
			})
		case "/fixture-dep/-/fixture-dep-1.0.0.tgz":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return fixtureRegistry{server}
}

// preparePnpmStore resolves the lockfile and fills a store in a scratch copy, as the
// operator image build does, then copies only the lockfile back into the source.
func preparePnpmStore(t *testing.T, web, registry, store string) {
	t.Helper()
	scratch := t.TempDir()
	data, err := os.ReadFile(filepath.Join(web, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, scratch, "package.json", string(data))
	command := exec.Command("pnpm", "install", "--ignore-scripts", "--registry="+registry+"/", "--store-dir="+store)
	command.Dir = scratch
	command.Env = append(os.Environ(), "HOME="+t.TempDir(),
		"npm_config_manage_package_manager_versions=false", "pnpm_config_manage_package_manager_versions=false")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare pnpm store: %v\n%s", err, output)
	}
	lock, err := os.ReadFile(filepath.Join(scratch, "pnpm-lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, web, "pnpm-lock.yaml", string(lock))
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	if output, err := exec.Command("cp", "-a", source, destination).CombinedOutput(); err != nil {
		t.Fatalf("copy %s: %v\n%s", source, err, output)
	}
}

func worktreeHead(t *testing.T, workdir string) string {
	t.Helper()
	output, err := exec.Command("git", "-C", workdir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

// preparedManifestFor hashes the current profile inputs, as scripts/prepare_verifier.py does.
func preparedManifestFor(t *testing.T, workdir, head string) preparedManifest {
	t.Helper()
	files := map[string]*string{}
	for _, name := range mustLoadPreparedProfiles().Profiles[testProfile].Inputs {
		data, err := os.ReadFile(filepath.Join(workdir, name))
		if os.IsNotExist(err) {
			files[name] = nil
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		value := hex.EncodeToString(sum[:])
		files[name] = &value
	}
	return preparedManifest{Version: 2, Profile: testProfile, BaseSHA: head, Files: files, Commands: [][]string{{"make", "verify"}}}
}

func writePreparedManifest(t *testing.T, bundle string, m preparedManifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertDisposableOutputsRemoved(t *testing.T, cacheRoot, workdir string) {
	t.Helper()
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("disposable verification cache survived the attempt: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(workdir, "web", "node_modules")); err == nil {
		t.Fatal("verification output web/node_modules escaped into the source worktree")
	}
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
