package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerifierPreparationProbeReportsPreparedCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/prepared":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"prepared":true,"profile":"go1.26.6-node24-pnpm10.33.0-v2","manifest_version":2,"base_sha":"` + strings.Repeat("a", 40) + `","toolchains":{"go":"1.26.6","node":"24","pnpm":"10.33.0"},"commands":[["make","verify"]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := &envOpenPilotRepositoryStatusProvider{client: &http.Client{Timeout: 2 * time.Second}}
	preparation := provider.verifierPreparation(t.Context(), server.URL)
	if preparation == nil || !preparation.Prepared || preparation.Profile != "go1.26.6-node24-pnpm10.33.0-v2" || preparation.Toolchains["pnpm"] != "10.33.0" {
		t.Fatalf("preparation = %+v", preparation)
	}
	if !preparation.SupportsCommand("make verify") || !preparation.SupportsCommand("  make   verify ") {
		t.Fatal("exact prepared command rejected")
	}
	for _, command := range []string{"make", "make verify extra", "go test ./...", ""} {
		if preparation.SupportsCommand(command) {
			t.Fatalf("unsupported command %q accepted", command)
		}
	}
}

func TestVerifierPreparationProbeReportsOlderAndInvalidVerifiers(t *testing.T) {
	cases := map[string]struct {
		handler http.HandlerFunc
		want    string
	}{
		"older":   {func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, "does not report preparation"},
		"invalid": {func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }, "invalid preparation report"},
		"failing": {func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, "500"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			provider := &envOpenPilotRepositoryStatusProvider{client: &http.Client{Timeout: 2 * time.Second}}
			preparation := provider.verifierPreparation(t.Context(), server.URL)
			if preparation == nil || preparation.Prepared || !strings.Contains(preparation.Error, tc.want) || preparation.Commands == nil {
				t.Fatalf("preparation = %+v", preparation)
			}
			if preparation.SupportsCommand("make verify") {
				t.Fatal("unprepared verifier claimed command support")
			}
		})
	}
}

func TestReadinessComparesTestCommandWithPreparedVerifier(t *testing.T) {
	prepared := &OpenPilotVerifierPreparation{Prepared: true, Profile: "go1.26.6-node24-pnpm10.33.0-v2", Commands: [][]string{{"make", "verify"}}}
	unprepared := &OpenPilotVerifierPreparation{Commands: [][]string{}, Error: "no prepared verification bundle is configured"}
	check := func(command string, executors ...OpenPilotRepositoryExecutorStatus) *OpenPilotReadinessCheck {
		report := OpenPilotReadinessReport{Executors: executors}
		openPilotReadinessPreparationCheck(&report, command)
		for index := range report.Checks {
			if report.Checks[index].Name == "verifier_preparation" {
				return &report.Checks[index]
			}
		}
		return nil
	}
	alpha := OpenPilotRepositoryExecutorStatus{RepositoryID: "alpha", ExecutorGeneration: "g1", VerifierPreparation: prepared}
	if got := check("make verify", alpha); got == nil || got.Status != OpenPilotReadinessPass || got.Code != "verifier_command_supported" {
		t.Fatalf("supported = %+v", got)
	}
	if got := check("go test ./...", alpha); got == nil || got.Status != OpenPilotReadinessFail || got.Code != OpenPilotReasonVerifierCommandUnsupported || !strings.Contains(got.Message, `"make verify"`) {
		t.Fatalf("unsupported = %+v", got)
	}
	beta := OpenPilotRepositoryExecutorStatus{RepositoryID: "beta", ExecutorGeneration: "g1", VerifierPreparation: unprepared}
	if got := check("make verify", beta); got == nil || got.Status != OpenPilotReadinessWarn || got.Code != OpenPilotReasonVerifierUnprepared || !strings.Contains(got.Message, "beta/g1") {
		t.Fatalf("unprepared = %+v", got)
	}
	if got := check("", alpha); got != nil {
		t.Fatalf("check without a command = %+v", got)
	}
	if got := check("make verify", OpenPilotRepositoryExecutorStatus{RepositoryID: "legacy"}); got != nil {
		t.Fatalf("check without a preparation report = %+v", got)
	}
}
