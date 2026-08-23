package controlplane

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMetricsRepublishBackupSuccessTimestamp(t *testing.T) {
	metricsFile := filepath.Join(t.TempDir(), "backup.prom")
	if err := os.WriteFile(metricsFile, []byte("switchyard_postgres_backup_last_success_timestamp_seconds 1785102299\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWITCHYARD_BACKUP_METRICS_FILE", metricsFile)

	store := NewStore(nil)
	api := NewAPI(store, NewRunner(store))
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	api.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "switchyard_postgres_backup_last_success_timestamp_seconds 1785102299") {
		t.Fatalf("metrics did not contain backup timestamp:\n%s", rec.Body.String())
	}
}

func TestBackupSuccessTimestampRejectsMalformedMetric(t *testing.T) {
	metricsFile := filepath.Join(t.TempDir(), "backup.prom")
	if err := os.WriteFile(metricsFile, []byte("switchyard_postgres_backup_last_success_timestamp_seconds not-a-number\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := backupSuccessTimestamp(metricsFile); ok {
		t.Fatal("malformed backup timestamp was accepted")
	}
}

func TestRestoreDrillMetricsStrictParsing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "restore-drill.prom")
	valid := "switchyard_restore_drill_last_attempt_timestamp_seconds 1785102299\n" +
		"switchyard_restore_drill_last_result 0\n" +
		"switchyard_restore_drill_next_deadline_timestamp_seconds 1785105899\n"
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	metrics, ok := restoreDrillMetrics(path)
	if !ok || metrics["switchyard_restore_drill_last_result"] != 0 {
		t.Fatalf("valid restore metrics rejected: %#v", metrics)
	}
	for _, content := range []string{
		"switchyard_restore_drill_last_result NaN\n",
		"switchyard_restore_drill_last_result 2\n",
		valid + "unapproved_metric 1\n",
		valid + "switchyard_restore_drill_last_result 1\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := restoreDrillMetrics(path); ok {
			t.Fatalf("malformed restore metrics accepted: %q", content)
		}
	}
}

func TestRestoreDrillMetricsRejectSymlinkAndSeeAtomicReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "restore-drill.prom")
	target := filepath.Join(directory, "target.prom")
	if err := os.WriteFile(target, []byte("switchyard_restore_drill_last_result 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, ok := restoreDrillMetrics(path); ok {
		t.Fatal("symlinked restore metrics accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("switchyard_restore_drill_last_result 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(directory, ".replacement")
	if err := os.WriteFile(replacement, []byte("switchyard_restore_drill_last_result 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	metrics, ok := restoreDrillMetrics(path)
	if !ok || metrics["switchyard_restore_drill_last_result"] != 1 {
		t.Fatalf("atomic replacement not observed: %#v", metrics)
	}
}
