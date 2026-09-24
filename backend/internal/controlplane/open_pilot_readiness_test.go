package controlplane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type readinessFixture struct {
	store    *MemoryStore
	api      *API
	manifest OpenPilotRuntimeManifest
	servers  []*httptest.Server
}

// newReadinessFixture builds a durable two-repository runtime from env with
// healthy fake OpenCode/verifier endpoints, an allowlisted command policy, and
// an executor registry whose bundles hold private passwords.
func newReadinessFixture(t *testing.T) readinessFixture {
	t.Helper()
	servers := make([]*httptest.Server, 0, 2)
	for index := range 2 {
		password := strings.Repeat(string(rune('a'+index)), 40)
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/session":
				if _, supplied, ok := request.BasicAuth(); !ok || supplied != password {
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte("[]"))
			case "/healthz":
				response.WriteHeader(http.StatusOK)
			default:
				response.WriteHeader(http.StatusNotFound)
			}
		}))
		servers = append(servers, server)
		t.Cleanup(server.Close)
	}
	manifest, manifestPath := testMultiRepositoryRuntimeManifest(t)
	// Use a fingerprint that cannot collide with the 40-byte fixture passwords
	// so the redaction assertions stay meaningful.
	manifest.RegistryFingerprint = strings.Repeat("0f", 32)
	for index := range manifest.Repositories {
		manifest.Repositories[index].OpenCodeEndpoint = servers[index].URL
		manifest.Repositories[index].VerifierEndpoint = servers[index].URL
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", manifestPath)
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "durable")
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	t.Setenv("OPEN_PILOT_AUTONOMY_MODE", "")
	t.Setenv("OPEN_PILOT_COMMAND_POLICY_MODE", "allow")
	t.Setenv("OPEN_PILOT_ALLOWED_COMMANDS", "go,make")
	t.Setenv("OPEN_PILOT_GITEA_WRITE_MODE", "allow")
	t.Setenv("OPEN_PILOT_GIT_PUSH_MODE", "allow")
	t.Setenv("OPEN_PILOT_AUTO_COMMIT_MODE", "allow")
	t.Setenv("OPEN_PILOT_CI_CHECK_MODE", "require-pass")
	t.Setenv("OPEN_PILOT_MERGE_MODE", "allow")
	t.Setenv("OPEN_PILOT_CLOSE_ISSUE_MODE", "disabled")
	store := NewStore(nil)
	runner := NewRunner(store)
	if runner.openPilotGateError != nil {
		t.Fatal(runner.openPilotGateError)
	}
	env, ok := runner.executors.(*EnvWorkflowExecutors)
	if !ok || env.openPilotGateError != nil || len(env.openPilotBundles) != 2 {
		t.Fatalf("executors = %T bundles=%d err=%v", runner.executors, len(env.openPilotBundles), env.openPilotGateError)
	}
	return readinessFixture{store: store, api: NewAPI(store, runner), manifest: manifest, servers: servers}
}

func (f readinessFixture) openProof(t *testing.T, fingerprint string) {
	t.Helper()
	expires := time.Now().Add(time.Hour)
	if _, err := f.store.ConfigureOpenPilotRollout(ConfigureOpenPilotRolloutInput{Mode: "proof", RegistryFingerprint: fingerprint, ExpiresAt: &expires, ProofTargets: []OpenPilotProofTarget{
		{RepositoryID: "alpha", ExecutorGeneration: "generation-alpha", RepositorySlug: "owner/alpha", Issue: "1"},
		{RepositoryID: "beta", ExecutorGeneration: "generation-beta", RepositorySlug: "owner/beta", Issue: "1"},
	}}); err != nil {
		t.Fatal(err)
	}
}

func (f readinessFixture) readiness(t *testing.T, query string) (int, string, OpenPilotReadinessReport) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/readiness"+query, nil)
	response := httptest.NewRecorder()
	f.api.routes().ServeHTTP(response, request)
	var report OpenPilotReadinessReport
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode readiness: %v: %s", err, response.Body.String())
		}
	}
	return response.Code, response.Body.String(), report
}

func readinessCheck(report OpenPilotReadinessReport, name string) OpenPilotReadinessCheck {
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	return OpenPilotReadinessCheck{}
}

func assertReadinessRedacted(t *testing.T, body string, manifest OpenPilotRuntimeManifest) {
	t.Helper()
	for _, repository := range manifest.Repositories {
		for _, forbidden := range []string{repository.PasswordFile, repository.SourcePath, repository.WorktreeRoot, strings.Repeat("a", 40), strings.Repeat("b", 40)} {
			if forbidden != "" && strings.Contains(body, forbidden) {
				t.Fatalf("readiness leaked %q: %s", forbidden, body)
			}
		}
	}
}

func TestOpenPilotReadinessHealthyTargetIsReady(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	code, body, report := fixture.readiness(t, "?repo=legacy/alpha&issue=01&test_command=go+test+./...")
	if code != http.StatusOK || !report.Ready || len(report.Blockers) != 0 {
		t.Fatalf("code=%d ready=%v blockers=%v body=%s", code, report.Ready, report.Blockers, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
	if report.ActivationMode != "durable" || report.RegistryFingerprint != fixture.manifest.RegistryFingerprint || len(report.Repositories) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Target == nil || !report.Target.Registered || report.Target.RepositoryID != "alpha" || report.Target.RepositorySlug != "owner/alpha" || report.Target.CanonicalIssue != "1" {
		t.Fatalf("target = %+v", report.Target)
	}
	if report.Rollout == nil || report.Rollout.Mode != "proof" || !report.Rollout.FingerprintMatches {
		t.Fatalf("rollout = %+v", report.Rollout)
	}
	if len(report.Executors) != 1 || report.Executors[0].RepositoryID != "alpha" || !report.Executors[0].Healthy {
		t.Fatalf("executors = %+v", report.Executors)
	}
	if report.Command == nil || !report.Command.Allowed || report.Command.DryRun {
		t.Fatalf("command = %+v", report.Command)
	}
	if !report.Gates.Known || report.Gates.GiteaWriteMode != "allow" || report.Gates.CICheckMode != "require-pass" || report.Gates.CommandPolicyMode != "allow" || report.Gates.CloseIssueMode != "disabled" {
		t.Fatalf("gates = %+v", report.Gates)
	}
	if check := readinessCheck(report, "close_issue_gate"); check.Code != OpenPilotReasonCloseIssueDisabled || check.Status != OpenPilotReadinessWarn || check.Remediation == "" {
		t.Fatalf("close issue gate = %+v", check)
	}
	if check := readinessCheck(report, "issue_state"); check.Code != "issue_state_new" || check.Status != OpenPilotReadinessPass {
		t.Fatalf("issue state = %+v", check)
	}
	done := map[string]bool{}
	for _, step := range report.Onboarding {
		done[step.Step] = step.Done
	}
	for _, step := range []string{"render_runtime_manifest", "register_repository", "load_execution_bundle", "executors_healthy", "open_rollout_gate", "enable_write_gates", "enable_ci_and_merge_gates", "allow_test_command"} {
		if !done[step] {
			t.Fatalf("onboarding step %s not done: %+v", step, report.Onboarding)
		}
	}
	for _, check := range report.Checks {
		if check.Status == OpenPilotReadinessFail {
			t.Fatalf("unexpected failure %+v", check)
		}
	}
}

func TestOpenPilotReadinessUnregisteredRepositoryExplainsRemediation(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	code, body, report := fixture.readiness(t, "?repo=owner/gamma&issue=1")
	if code != http.StatusOK || report.Ready || len(report.Blockers) == 0 || report.Blockers[0] != OpenPilotReasonRepositoryUnregistered {
		t.Fatalf("code=%d ready=%v blockers=%v body=%s", code, report.Ready, report.Blockers, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
	check := readinessCheck(report, "repository_registered")
	if check.Status != OpenPilotReadinessFail || !strings.Contains(check.Remediation, "prepare_open_pilot_registry.py") {
		t.Fatalf("check = %+v", check)
	}
	if report.Target == nil || report.Target.Registered || report.Target.RepositoryID != "" {
		t.Fatalf("target = %+v", report.Target)
	}
	for _, step := range report.Onboarding {
		if step.Step == "register_repository" && step.Done {
			t.Fatalf("onboarding marked unregistered repository done: %+v", report.Onboarding)
		}
	}
	if code, _, report := fixture.readiness(t, "?repo=owner/alpha&issue=abc"); code != http.StatusOK || report.Ready || report.Blockers[0] != OpenPilotReasonIssueInvalid {
		t.Fatalf("invalid issue: code=%d report=%+v", code, report)
	}
	if code, _, _ := fixture.readiness(t, "?issue=1"); code != http.StatusBadRequest {
		t.Fatalf("issue without repo code = %d", code)
	}
}

func TestOpenPilotReadinessReportsStaleFingerprintAndAffinity(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, strings.Repeat("1d", 32))
	code, body, report := fixture.readiness(t, "?repo=owner/alpha&issue=1")
	if code != http.StatusOK || report.Ready {
		t.Fatalf("code=%d body=%s", code, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
	if report.Rollout == nil || report.Rollout.FingerprintMatches || report.Rollout.RegistryFingerprint != strings.Repeat("1d", 32) {
		t.Fatalf("rollout = %+v", report.Rollout)
	}
	if check := readinessCheck(report, "rollout_gate"); check.Code != OpenPilotReasonRolloutFingerprintStale || check.Status != OpenPilotReadinessFail || !strings.Contains(check.Remediation, "registry_fingerprint") {
		t.Fatalf("rollout check = %+v", check)
	}
	if len(report.Blockers) != 1 {
		t.Fatalf("blockers = %v", report.Blockers)
	}

	// A durable state bound to an older executor generation must be reported,
	// not silently re-bound.
	if _, err := fixture.store.UpsertOpenPilotIssueState(OpenPilotIssueState{Repo: "owner/alpha", Issue: "2", Phase: OpenPilotPhaseRequested}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.BindOpenPilotIssueStateAffinity("owner/alpha", "2", OpenPilotTarget{RepositoryID: "alpha", RepositorySlug: "owner/alpha", Issue: "2", ExecutorGeneration: "generation-old"}); err != nil {
		t.Fatal(err)
	}
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	_, body, report = fixture.readiness(t, "?repo=owner/alpha&issue=2")
	check := readinessCheck(report, "issue_state")
	if report.Ready || check.Code != OpenPilotReasonIssueAffinityStale || !strings.Contains(check.Message, "generation-old") || report.IssueState == nil || report.IssueState.Phase != string(OpenPilotPhaseRequested) {
		t.Fatalf("stale affinity: check=%+v state=%+v body=%s", check, report.IssueState, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
}

func TestOpenPilotReadinessReportsUnreachableExecutors(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	fixture.servers[0].Close()
	code, body, report := fixture.readiness(t, "?repo=owner/alpha&issue=1")
	if code != http.StatusOK || report.Ready {
		t.Fatalf("code=%d body=%s", code, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
	if check := readinessCheck(report, "opencode_executor"); check.Code != OpenPilotReasonOpenCodeUnreachable || check.Status != OpenPilotReadinessFail || check.Remediation == "" {
		t.Fatalf("opencode check = %+v", check)
	}
	if check := readinessCheck(report, "verifier"); check.Code != OpenPilotReasonVerifierUnreachable || check.Status != OpenPilotReadinessFail {
		t.Fatalf("verifier check = %+v", check)
	}
	if len(report.Executors) != 1 || report.Executors[0].Healthy || report.Executors[0].OpenCode.Healthy || report.Executors[0].VerifierHealthy {
		t.Fatalf("executors = %+v", report.Executors)
	}
	// The sibling repository remains healthy and is reported independently.
	_, _, sibling := fixture.readiness(t, "?repo=owner/beta&issue=1")
	if !sibling.Ready {
		t.Fatalf("sibling = %+v", sibling)
	}
}

func TestOpenPilotReadinessRejectsUnsupportedCommandsWithoutRelaxingPolicy(t *testing.T) {
	fixture := newReadinessFixture(t)
	fixture.openProof(t, fixture.manifest.RegistryFingerprint)
	for _, test := range []struct {
		command string
		reason  string
	}{
		{command: "npm test", reason: "not allowed"},
		{command: "go test ./... | tee out", reason: "shell syntax"},
	} {
		_, body, report := fixture.readiness(t, "?repo=owner/alpha&issue=1&test_command="+strings.ReplaceAll(test.command, " ", "+"))
		check := readinessCheck(report, "test_command")
		if report.Ready || check.Code != OpenPilotReasonCommandDenied || !strings.Contains(check.Message, test.reason) || !strings.Contains(check.Remediation, "does not relax") {
			t.Fatalf("command %q: check=%+v body=%s", test.command, check, body)
		}
		if report.Command == nil || report.Command.Allowed {
			t.Fatalf("command %q projected as allowed: %+v", test.command, report.Command)
		}
	}
	// Preflighting a command must not change the runner's decision afterwards.
	env := fixture.api.runner.executors.(*EnvWorkflowExecutors)
	if decision := env.openPilotBundles[openPilotExecutionBundleKey("alpha", "generation-alpha")].commandRunner.(*PolicyCommandRunner).Decide("npm test"); decision.Allowed {
		t.Fatalf("policy relaxed: %+v", decision)
	}
	_, _, report := fixture.readiness(t, "?repo=owner/alpha&issue=1")
	if check := readinessCheck(report, "test_command"); check.Code != OpenPilotReasonCommandNotProvided || check.Status != OpenPilotReadinessSkip || !report.Ready {
		t.Fatalf("missing command check = %+v ready=%v", check, report.Ready)
	}
}

func TestOpenPilotReadinessGateAndListenerBoundaries(t *testing.T) {
	t.Setenv("OPEN_PILOT_REPO_PATH", "")
	t.Setenv("OPEN_PILOT_WORKDIR", "")
	t.Setenv("SWITCHYARD_OPEN_PILOT_RUNTIME_MANIFEST", "")
	t.Setenv("SWITCHYARD_OPEN_PILOT_ACTIVATION_MODE", "")
	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	request := httptest.NewRequest(http.MethodGet, "/api/open-pilot/readiness?repo=owner/alpha&issue=1", nil)
	response := httptest.NewRecorder()
	api.routes().ServeHTTP(response, request)
	var report OpenPilotReadinessReport
	if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil || response.Code != http.StatusOK {
		t.Fatalf("code=%d err=%v body=%s", response.Code, err, response.Body.String())
	}
	if report.Ready || len(report.Blockers) != 1 || report.Blockers[0] != OpenPilotReasonGateUnavailable || readinessCheck(report, "admission_gate").Remediation == "" || len(report.Onboarding) == 0 {
		t.Fatalf("gate-unavailable report = %+v", report)
	}

	t.Setenv("SWITCHYARD_CONTROL_API_TOKEN", "admin-token")
	t.Setenv("SWITCHYARD_CONTROL_READ_TOKEN", "read-token")
	api = NewAPI(store, NewRunner(store))
	for _, test := range []struct {
		name    string
		handler http.Handler
		method  string
		path    string
		token   string
		want    int
	}{
		{name: "control unauthenticated", handler: api.ControlPlaneHandler(), method: http.MethodGet, path: "/api/open-pilot/readiness", want: http.StatusUnauthorized},
		{name: "control read token", handler: api.ControlPlaneHandler(), method: http.MethodGet, path: "/api/open-pilot/readiness", token: "read-token", want: http.StatusOK},
		{name: "validate read token forbidden", handler: api.ControlPlaneHandler(), method: http.MethodPost, path: "/api/open-pilot/readiness/validate", token: "read-token", want: http.StatusForbidden},
		{name: "validate admin token", handler: api.ControlPlaneHandler(), method: http.MethodPost, path: "/api/open-pilot/readiness/validate", token: "admin-token", want: http.StatusOK},
		{name: "model listener readiness", handler: api.ModelIngressHandler(), method: http.MethodGet, path: "/api/open-pilot/readiness", token: "admin-token", want: http.StatusNotFound},
		{name: "model listener validate", handler: api.ModelIngressHandler(), method: http.MethodPost, path: "/api/open-pilot/readiness/validate", token: "admin-token", want: http.StatusNotFound},
	} {
		var body *strings.Reader
		if test.method == http.MethodPost {
			body = strings.NewReader(`{"manifest":{}}`)
		} else {
			body = strings.NewReader("")
		}
		request := httptest.NewRequest(test.method, test.path, body)
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		response := httptest.NewRecorder()
		test.handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s: status = %d, want %d: %s", test.name, response.Code, test.want, response.Body.String())
		}
		if test.handler != nil && test.want == http.StatusNotFound && !strings.Contains(response.Body.String(), "route not available on this listener") {
			t.Fatalf("%s: model listener served the route: %s", test.name, response.Body.String())
		}
	}
}

func TestOpenPilotReadinessDryRunValidatesConfigurationWithoutSideEffects(t *testing.T) {
	fixture := newReadinessFixture(t)
	post := func(t *testing.T, payload string) (int, OpenPilotConfigurationDryRunReport, string) {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/api/open-pilot/readiness/validate", strings.NewReader(payload))
		response := httptest.NewRecorder()
		fixture.api.routes().ServeHTTP(response, request)
		var report OpenPilotConfigurationDryRunReport
		if response.Code == http.StatusOK {
			if err := json.Unmarshal(response.Body.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
		}
		return response.Code, report, response.Body.String()
	}
	changed := fixture.manifest
	changed.Repositories = append([]OpenPilotRuntimeRepository(nil), fixture.manifest.Repositories...)
	changed.Repositories[0].ExecutorGeneration = "generation-alpha-2"
	changed.Repositories = append(changed.Repositories, OpenPilotRuntimeRepository{ID: "gamma", Slug: "owner/gamma", Aliases: []string{}, ExecutorGeneration: "generation-gamma"})
	changed.RegistryFingerprint = strings.Repeat("2c", 32)
	raw, err := json.Marshal(map[string]any{"manifest": changed})
	if err != nil {
		t.Fatal(err)
	}
	rolloutBefore, _ := fixture.store.OpenPilotRolloutStatus()
	code, report, body := post(t, string(raw))
	if code != http.StatusOK || !report.Valid || report.Kind != "runtime-manifest" || report.RuntimeFingerprintMatches || report.RuntimeFingerprint != fixture.manifest.RegistryFingerprint {
		t.Fatalf("code=%d report=%+v body=%s", code, report, body)
	}
	assertReadinessRedacted(t, body, fixture.manifest)
	matches := map[string]string{}
	for _, repository := range report.Repositories {
		matches[repository.ID] = repository.RuntimeMatch
	}
	if matches["alpha"] != "generation-differs" || matches["beta"] != "exact" || matches["gamma"] != "unregistered" {
		t.Fatalf("matches = %v", matches)
	}
	codes := map[string]string{}
	for _, check := range report.Checks {
		codes[check.Code] = check.Status
	}
	if codes[OpenPilotReasonRuntimeFingerprintStale] != OpenPilotReadinessWarn || codes[OpenPilotReasonRepositoryGenerationDiffer] != OpenPilotReadinessWarn || codes[OpenPilotReasonRepositoryNotInRuntime] != OpenPilotReadinessWarn {
		t.Fatalf("codes = %v", codes)
	}
	if rolloutAfter, _ := fixture.store.OpenPilotRolloutStatus(); rolloutAfter.Generation != rolloutBefore.Generation || fixture.api.runner.openPilotGate.registry.manifest.RegistryFingerprint != fixture.manifest.RegistryFingerprint {
		t.Fatal("dry-run validation changed runtime or rollout state")
	}

	// Secret-looking material is rejected without echoing the document.
	leaking := fixture.manifest
	leaking.Repositories = append([]OpenPilotRuntimeRepository(nil), fixture.manifest.Repositories...)
	leaking.Repositories[0].Aliases = []string{"token=hunter2-should-not-echo"}
	raw, _ = json.Marshal(map[string]any{"manifest": leaking})
	code, report, body = post(t, string(raw))
	if code != http.StatusOK || report.Valid || len(report.Blockers) != 1 || report.Blockers[0] != OpenPilotReasonDocumentContainsSecret || strings.Contains(body, "hunter2") {
		t.Fatalf("secret document: code=%d report=%+v body=%s", code, report, body)
	}

	// Slug changes for an existing ID fail because identities are immutable.
	repointed := fixture.manifest
	repointed.Repositories = append([]OpenPilotRuntimeRepository(nil), fixture.manifest.Repositories...)
	repointed.Repositories[0].Slug = "other/alpha"
	raw, _ = json.Marshal(map[string]any{"manifest": repointed})
	if _, report, _ := post(t, string(raw)); report.Valid || report.Blockers[0] != OpenPilotReasonDocumentInvalid {
		t.Fatalf("repointed slug report = %+v", report)
	}

	// Operator registries are validated with the same strict decoder.
	registry := OpenPilotOperatorRegistry{Version: 1, Generation: "registry-v3", Repositories: []OpenPilotOperatorRepository{{
		ID: "beta", Slug: "owner/beta", Aliases: []string{}, ExecutorGeneration: "generation-beta",
		SourcePath: "/srv/beta/source", WorktreeRoot: "/srv/beta/worktrees", ConfigPath: "/srv/beta/config", DataPath: "/srv/beta/data", PasswordFile: "/srv/beta/password",
	}}}
	raw, _ = json.Marshal(map[string]any{"registry": registry})
	if _, report, body := post(t, string(raw)); !report.Valid || report.Kind != "operator-registry" || report.Generation != "registry-v3" || report.Repositories[0].RuntimeMatch != "exact" || strings.Contains(body, "/srv/beta") {
		t.Fatalf("registry report = %+v body=%s", report, body)
	}
	if code, report, _ := post(t, `{"registry":{"version":1,"generation":"g","repositories":[],"unknown":true}}`); code != http.StatusOK || report.Valid || report.Blockers[0] != OpenPilotReasonDocumentDecodeFailed {
		t.Fatalf("unknown field report = %d %+v", code, report)
	}
	if _, report, _ := post(t, `{}`); report.Valid || report.Blockers[0] != OpenPilotReasonDocumentKindAmbiguous {
		t.Fatalf("ambiguous report = %+v", report)
	}
	if code, _, _ := post(t, `{"manifest":{},"extra":1}`); code != http.StatusBadRequest {
		t.Fatalf("unknown top-level field code = %d", code)
	}
}
