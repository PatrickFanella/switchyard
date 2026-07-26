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
