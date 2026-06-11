package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// CLIFlags holds values parsed from command-line flags.
type CLIFlags struct {
	ConfigPath string
	LogLevel   string // empty = not set
}

// Load reads a YAML config file and applies defaults for zero-value fields.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: cannot read %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: invalid YAML in %q: %w", path, err)
	}
	cfg.ConfigPath = path

	applyDefaults(&cfg)
	if err := resolveUpstreamAuth(&cfg); err != nil {
		return nil, err
	}
	if cfg.Phoenix.Enabled && cfg.Phoenix.OTLPEndpoint == "" {
		return nil, fmt.Errorf("config: phoenix.otlp_endpoint must be set when phoenix.enabled is true")
	}
	if cfg.Phoenix.Enabled && cfg.Phoenix.ProjectMode != "" && cfg.Phoenix.ProjectMode != "static" && cfg.Phoenix.ProjectMode != "client" {
		return nil, fmt.Errorf("config: phoenix.project_mode must be static or client")
	}
	return &cfg, nil
}

func resolveUpstreamAuth(cfg *Config) error {
	for i := range cfg.Upstreams {
		auth := &cfg.Upstreams[i].Auth
		auth.Type = strings.ToLower(strings.TrimSpace(auth.Type))
		switch auth.Type {
		case "", "none":
			auth.Type = ""
		case "bearer":
			if auth.TokenEnv != "" {
				auth.Token = os.Getenv(auth.TokenEnv)
			}
		default:
			return fmt.Errorf("config: upstreams[%d].auth.type must be bearer or none", i)
		}
	}
	return nil
}

// ApplyEnv overlays environment variables onto cfg.
// Only non-empty env vars override existing values.
func ApplyEnv(cfg *Config) {
	if v := os.Getenv("LLAMA_LINE_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("LLAMA_LINE_OLLAMA_URL"); v != "" {
		cfg.OllamaURL = v
	}
	if v := os.Getenv("LLAMA_LINE_MAX_QUEUE_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxQueueDepth = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_REQUEST_TIMEOUT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.RequestTimeoutSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_QUEUE_WAIT_TIMEOUT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.QueueWaitTimeoutSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_HEARTBEAT_INTERVAL_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.HeartbeatIntervalSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_OLLAMA_RETRY_MAX_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.OllamaRetryMaxSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_INTERVAL_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.UpstreamHealthProbeIntervalSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_UPSTREAM_HEALTH_PROBE_TIMEOUT_SECS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.UpstreamHealthProbeTimeoutSecs = n
		}
	}
	if v := os.Getenv("LLAMA_LINE_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
}

// ApplyCLI overlays CLI flag values onto cfg.
// Only non-empty flag values override existing values.
func ApplyCLI(cfg *Config, flags CLIFlags) {
	if flags.ConfigPath != "" {
		cfg.ConfigPath = flags.ConfigPath
	}
	if flags.LogLevel != "" {
		cfg.LogLevel = flags.LogLevel
	}
}

func applyDefaults(cfg *Config) {
	if cfg.MaxQueueDepth == 0 {
		cfg.MaxQueueDepth = 20
	}
	if cfg.RequestTimeoutSecs == 0 {
		cfg.RequestTimeoutSecs = 300
	}
	if cfg.QueueWaitTimeoutSecs == 0 {
		cfg.QueueWaitTimeoutSecs = 600
	}
	if cfg.HeartbeatIntervalSecs == 0 {
		cfg.HeartbeatIntervalSecs = 5
	}
	if cfg.OllamaRetryMaxSecs == 0 {
		cfg.OllamaRetryMaxSecs = 60
	}
	if cfg.UpstreamHealthProbeIntervalSecs == 0 {
		cfg.UpstreamHealthProbeIntervalSecs = 15
	}
	if cfg.UpstreamHealthProbeTimeoutSecs == 0 {
		cfg.UpstreamHealthProbeTimeoutSecs = 2
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.Database.MaxConns == 0 {
		cfg.Database.MaxConns = 4
	}
	if cfg.Database.RetentionDays == 0 {
		cfg.Database.RetentionDays = 30
	}
	if cfg.Database.Enabled && cfg.Database.URL == "" {
		cfg.Database.URL = "postgres://llama:line@localhost:5432/llama_line?sslmode=disable"
	}
	if cfg.Embeddings.Model == "" {
		cfg.Embeddings.Model = "qwen3-embedding:0.6b"
	}
	if cfg.Embeddings.Dimension == 0 {
		cfg.Embeddings.Dimension = 768
	}
	if cfg.Embeddings.WorkerCount == 0 {
		cfg.Embeddings.WorkerCount = 1
	}
	if cfg.Embeddings.MaxChunkChars == 0 {
		cfg.Embeddings.MaxChunkChars = 4000
	}
	if cfg.Phoenix.ProjectMode == "" {
		cfg.Phoenix.ProjectMode = "static"
	}
	if cfg.Phoenix.ProjectName == "" {
		cfg.Phoenix.ProjectName = "llama-line"
	}
}
