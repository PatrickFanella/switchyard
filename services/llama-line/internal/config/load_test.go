package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

const validYAML = `
listen_addr: "0.0.0.0:11434"
ollama_url: "http://127.0.0.1:11435"
max_queue_depth: 10
request_timeout_secs: 120
queue_wait_timeout_secs: 240
heartbeat_interval_secs: 3
ollama_retry_max_secs: 30
log_level: debug
phoenix:
  enabled: false
  otlp_endpoint: "http://localhost:6006/v1/traces"
keys:
  - name: test-app
    key: secret123
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestLoad_PopulatesAllFields(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:11434" {
		t.Errorf("ListenAddr = %q", cfg.ListenAddr)
	}
	if cfg.OllamaURL != "http://127.0.0.1:11435" {
		t.Errorf("OllamaURL = %q", cfg.OllamaURL)
	}
	if cfg.MaxQueueDepth != 10 {
		t.Errorf("MaxQueueDepth = %d", cfg.MaxQueueDepth)
	}
	if cfg.RequestTimeoutSecs != 120 {
		t.Errorf("RequestTimeoutSecs = %d", cfg.RequestTimeoutSecs)
	}
	if cfg.QueueWaitTimeoutSecs != 240 {
		t.Errorf("QueueWaitTimeoutSecs = %d", cfg.QueueWaitTimeoutSecs)
	}
	if cfg.HeartbeatIntervalSecs != 3 {
		t.Errorf("HeartbeatIntervalSecs = %d", cfg.HeartbeatIntervalSecs)
	}
	if cfg.OllamaRetryMaxSecs != 30 {
		t.Errorf("OllamaRetryMaxSecs = %d", cfg.OllamaRetryMaxSecs)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q", cfg.LogLevel)
	}
	if len(cfg.Keys) != 1 || cfg.Keys[0].Name != "test-app" || cfg.Keys[0].Key != "secret123" {
		t.Errorf("Keys = %+v", cfg.Keys)
	}
	if cfg.Phoenix.Enabled {
		t.Errorf("Phoenix.Enabled = %v", cfg.Phoenix.Enabled)
	}
	if cfg.Phoenix.OTLPEndpoint != "http://localhost:6006/v1/traces" {
		t.Errorf("Phoenix.OTLPEndpoint = %q", cfg.Phoenix.OTLPEndpoint)
	}
}

func TestLoad_AppliesDefaults(t *testing.T) {
	path := writeTemp(t, "listen_addr: \":8080\"\nollama_url: \"http://localhost:11434\"\n")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxQueueDepth != 20 {
		t.Errorf("default MaxQueueDepth = %d, want 20", cfg.MaxQueueDepth)
	}
	if cfg.RequestTimeoutSecs != 300 {
		t.Errorf("default RequestTimeoutSecs = %d, want 300", cfg.RequestTimeoutSecs)
	}
	if cfg.QueueWaitTimeoutSecs != 600 {
		t.Errorf("default QueueWaitTimeoutSecs = %d, want 600", cfg.QueueWaitTimeoutSecs)
	}
	if cfg.HeartbeatIntervalSecs != 5 {
		t.Errorf("default HeartbeatIntervalSecs = %d, want 5", cfg.HeartbeatIntervalSecs)
	}
	if cfg.OllamaRetryMaxSecs != 60 {
		t.Errorf("default OllamaRetryMaxSecs = %d, want 60", cfg.OllamaRetryMaxSecs)
	}
	if cfg.UpstreamHealthProbeIntervalSecs != 15 {
		t.Errorf("default UpstreamHealthProbeIntervalSecs = %d, want 15", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 2 {
		t.Errorf("default UpstreamHealthProbeTimeoutSecs = %d, want 2", cfg.UpstreamHealthProbeTimeoutSecs)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("default LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoad_HealthProbeYAML(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":8080"
ollama_url: "http://localhost:11434"
upstream_health_probe_interval_secs: 30
upstream_health_probe_timeout_secs: 4
upstreams:
  - name: ocq
    url: http://ocq.local:8088
    health_path: /healthz
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.UpstreamHealthProbeIntervalSecs != 30 {
		t.Errorf("UpstreamHealthProbeIntervalSecs = %d, want 30", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 4 {
		t.Errorf("UpstreamHealthProbeTimeoutSecs = %d, want 4", cfg.UpstreamHealthProbeTimeoutSecs)
	}
	if len(cfg.Upstreams) != 1 || cfg.Upstreams[0].HealthPath != "/healthz" {
		t.Fatalf("upstream health_path not loaded: %+v", cfg.Upstreams)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.yaml")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeTemp(t, ":\tinvalid: [yaml")
	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestApplyEnv_OverridesYAML(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, _ := config.Load(path)

	t.Setenv("LLAMA_LINE_LOG_LEVEL", "warn")
	t.Setenv("LLAMA_LINE_MAX_QUEUE_DEPTH", "5")
	config.ApplyEnv(cfg)

	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn", cfg.LogLevel)
	}
	if cfg.MaxQueueDepth != 5 {
		t.Errorf("MaxQueueDepth = %d, want 5", cfg.MaxQueueDepth)
	}
}

func TestApplyEnv_HealthProbeOverridesYAML(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":8080"
ollama_url: "http://localhost:11434"
upstream_health_probe_interval_secs: 30
upstream_health_probe_timeout_secs: 4
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Setenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS", "45")
	t.Setenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS", "6")
	config.ApplyEnv(cfg)
	if cfg.UpstreamHealthProbeIntervalSecs != 45 {
		t.Errorf("UpstreamHealthProbeIntervalSecs = %d, want 45", cfg.UpstreamHealthProbeIntervalSecs)
	}
	if cfg.UpstreamHealthProbeTimeoutSecs != 6 {
		t.Errorf("UpstreamHealthProbeTimeoutSecs = %d, want 6", cfg.UpstreamHealthProbeTimeoutSecs)
	}
}

func TestApplyEnv_UnsetPreservesYAML(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, _ := config.Load(path)
	os.Unsetenv("LLAMA_LINE_LOG_LEVEL")
	config.ApplyEnv(cfg)
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (from YAML)", cfg.LogLevel)
	}
}

func TestApplyEnv_InvalidIntPreservesYAML(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, _ := config.Load(path)
	t.Setenv("LLAMA_LINE_MAX_QUEUE_DEPTH", "notanumber")
	config.ApplyEnv(cfg)
	if cfg.MaxQueueDepth != 10 {
		t.Errorf("MaxQueueDepth = %d, want 10 (YAML value preserved)", cfg.MaxQueueDepth)
	}
}

func TestApplyCLI_OverridesEnv(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, _ := config.Load(path)
	t.Setenv("LLAMA_LINE_LOG_LEVEL", "debug")
	config.ApplyEnv(cfg)
	config.ApplyCLI(cfg, config.CLIFlags{LogLevel: "warn"})
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want warn (CLI wins)", cfg.LogLevel)
	}
}

func TestApplyCLI_EmptyFlagPreservesValue(t *testing.T) {
	path := writeTemp(t, validYAML)
	cfg, _ := config.Load(path)
	config.ApplyCLI(cfg, config.CLIFlags{})
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug (unchanged)", cfg.LogLevel)
	}
}

func TestLoadDatabaseAndEmbeddingConfig(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":11434"
ollama_url: "http://127.0.0.1:11435"
database:
  enabled: true
  url: "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"
  max_conns: 8
  run_migrations: true
  persist_app_logs: true
  persist_request_bodies: true
  body_read_enabled: true
  retention_days: 30
embeddings:
  enabled: true
  model: "qwen3-embedding:0.6b"
  dimension: 768
  worker_count: 1
  max_chunk_chars: 4000
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Database.Enabled || cfg.Database.URL == "" || cfg.Database.MaxConns != 8 {
		t.Fatalf("database config not loaded: %+v", cfg.Database)
	}
	if !cfg.Database.PersistAppLogs || !cfg.Database.PersistRequestBodies || !cfg.Database.BodyReadEnabled {
		t.Fatalf("database persistence flags not loaded: %+v", cfg.Database)
	}
	if !cfg.Embeddings.Enabled || cfg.Embeddings.Model != "qwen3-embedding:0.6b" || cfg.Embeddings.Dimension != 768 {
		t.Fatalf("embedding config not loaded: %+v", cfg.Embeddings)
	}
}

func TestLoad_PhoenixEnabledRequiresEndpoint(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":11434"
ollama_url: "http://127.0.0.1:11435"
phoenix:
  enabled: true
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("expected error when phoenix enabled without otlp_endpoint")
	}
}

func TestPhoenixProjectModeDefault(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":0"
ollama_url: "http://127.0.0.1:11434"
phoenix:
  enabled: true
  otlp_endpoint: "http://phoenix:6006/v1/traces"
keys: []
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Phoenix.ProjectMode != "static" {
		t.Fatalf("mode=%q", cfg.Phoenix.ProjectMode)
	}
	if cfg.Phoenix.ProjectName != "llama-line" {
		t.Fatalf("project=%q", cfg.Phoenix.ProjectName)
	}
}

func TestPhoenixProjectModeValidation(t *testing.T) {
	path := writeTemp(t, `listen_addr: ":0"
ollama_url: "http://127.0.0.1:11434"
phoenix:
  enabled: true
  otlp_endpoint: "http://phoenix:6006/v1/traces"
  project_mode: "bad"
keys: []
`)
	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "phoenix.project_mode") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoad_ParsesUpstreamAuth(t *testing.T) {
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "opencode*"
    auth:
      type: bearer
      token: literal-token
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Upstreams[0].Auth.Type; got != "bearer" {
		t.Fatalf("auth type = %q, want bearer", got)
	}
	if got := cfg.Upstreams[0].Auth.Token; got != "literal-token" {
		t.Fatalf("auth token = %q, want literal-token", got)
	}
}

func TestLoad_ResolvesUpstreamAuthTokenEnv(t *testing.T) {
	t.Setenv("OCQ_GATEWAY_KEY", "env-token")
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: ocq-almaz
    url: http://almaz:8088
    models:
      - "opencode*"
    auth:
      type: bearer
      token_env: OCQ_GATEWAY_KEY
      token: literal-token
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if got := cfg.Upstreams[0].Auth.Token; got != "env-token" {
		t.Fatalf("resolved auth token = %q, want env-token", got)
	}
	if got := cfg.Upstreams[0].Auth.TokenEnv; got != "OCQ_GATEWAY_KEY" {
		t.Fatalf("token env = %q, want OCQ_GATEWAY_KEY", got)
	}
}

func TestLoad_RejectsUnknownUpstreamAuthType(t *testing.T) {
	path := writeTemp(t, `
listen_addr: "127.0.0.1:11434"
upstreams:
  - name: bad
    url: http://127.0.0.1:8088
    auth:
      type: digest
      token: nope
`)

	_, err := config.Load(path)
	if err == nil || !strings.Contains(err.Error(), "upstreams[0].auth.type") {
		t.Fatalf("Load() error = %v, want auth type validation error", err)
	}
}
