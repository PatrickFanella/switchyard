package config

// APIKey identifies a client allowed to use the broker.
// Priority controls queue ordering: higher value = served sooner.
// Requests with equal priority are served FIFO. Default is 0.
//
// Deduplicate: if true, identical in-flight requests (same model+messages hash)
// share a single ollama call instead of being enqueued separately.
//
// CacheTTLSecs: when > 0, non-streaming responses are cached in memory for this
// many seconds. A value of 0 disables caching for this key.
//
// CacheMaxEntries: maximum number of cached responses for this key.
// Defaults to the global CacheMaxEntries if 0. Only relevant when CacheTTLSecs > 0.
type APIKey struct {
	Name            string `yaml:"name"`
	Key             string `yaml:"key"`
	Priority        int    `yaml:"priority"`
	Deduplicate     bool   `yaml:"deduplicate"`
	CacheTTLSecs    int    `yaml:"cache_ttl_secs"`
	CacheMaxEntries int    `yaml:"cache_max_entries"`
}

// UpstreamAuthConfig describes credentials llama-line adds when forwarding to an upstream.
// Type may be empty/"none" for no auth or "bearer" for Authorization: Bearer <token>.
// TokenEnv, when set, is resolved during config load and takes precedence over Token.
type UpstreamAuthConfig struct {
	Type     string `yaml:"type"`
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"token_env"`
}

// UpstreamConfig describes one upstream ollama instance.
// Models is a list of exact model names or glob patterns (e.g. "llama3*", "mistral:7b").
// Requests whose model matches any pattern are routed to this upstream.
// An upstream with an empty Models list is the fallback for unmatched models.
// MaxQueueDepth overrides the global max_queue_depth for this upstream (0 = use global).
type UpstreamConfig struct {
	Name          string   `yaml:"name"`
	URL           string   `yaml:"url"`
	Models        []string `yaml:"models"`
	MaxQueueDepth int      `yaml:"max_queue_depth"`
	// HealthPath overrides the default /api/version probe path for this upstream.
	HealthPath string             `yaml:"health_path"`
	Auth       UpstreamAuthConfig `yaml:"auth"`
}

// DatabaseConfig controls optional Postgres persistence.
type DatabaseConfig struct {
	Enabled              bool   `yaml:"enabled"`
	URL                  string `yaml:"url"`
	MaxConns             int    `yaml:"max_conns"`
	RunMigrations        bool   `yaml:"run_migrations"`
	PersistAppLogs       bool   `yaml:"persist_app_logs"`
	PersistRequestBodies bool   `yaml:"persist_request_bodies"`
	BodyReadEnabled      bool   `yaml:"body_read_enabled"`
	RetentionDays        int    `yaml:"retention_days"`
}

// EmbeddingConfig controls optional semantic indexing.
type EmbeddingConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Model         string `yaml:"model"`
	Dimension     int    `yaml:"dimension"`
	WorkerCount   int    `yaml:"worker_count"`
	MaxChunkChars int    `yaml:"max_chunk_chars"`
}

// PhoenixConfig controls optional OTLP trace export to Phoenix.
type PhoenixConfig struct {
	Enabled      bool   `yaml:"enabled"`
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	ProjectName  string `yaml:"project_name"`
	ProjectMode  string `yaml:"project_mode"` // static | client
}

// Config holds all tuneable broker settings.
type Config struct {
	ConfigPath                      string `yaml:"-"`
	ListenAddr                      string `yaml:"listen_addr"`
	OllamaURL                       string `yaml:"ollama_url"` // legacy single-upstream field
	MaxQueueDepth                   int    `yaml:"max_queue_depth"`
	RequestTimeoutSecs              int    `yaml:"request_timeout_secs"`
	QueueWaitTimeoutSecs            int    `yaml:"queue_wait_timeout_secs"`
	HeartbeatIntervalSecs           int    `yaml:"heartbeat_interval_secs"`
	OllamaRetryMaxSecs              int    `yaml:"ollama_retry_max_secs"`
	UpstreamHealthProbeIntervalSecs int    `yaml:"upstream_health_probe_interval_secs"`
	UpstreamHealthProbeTimeoutSecs  int    `yaml:"upstream_health_probe_timeout_secs"`
	LogLevel                        string `yaml:"log_level"`
	AdminKey                        string `yaml:"admin_key"`
	// Metrics
	MetricsEnabled          bool   `yaml:"metrics_enabled"`
	MetricsPath             string `yaml:"metrics_path"`
	AlertWebhookURL         string `yaml:"alert_webhook_url"`
	AlertQueueFullThreshold int    `yaml:"alert_queue_full_threshold"`
	// Audit log — when set, full request/response bodies are written to this file.
	// Each line is a JSON object with client, id, model, path, request_body,
	// response_body, status, and duration_ms fields.
	// Streaming responses are not captured in the audit log.
	AuditLogPath        string `yaml:"audit_log_path"`
	AuditLogIncludeBody bool   `yaml:"audit_log_include_body"`
	// Cache (global defaults; per-key settings override these)
	CacheMaxEntries int              `yaml:"cache_max_entries"`
	Keys            []APIKey         `yaml:"keys"`
	Upstreams       []UpstreamConfig `yaml:"upstreams"`
	Database        DatabaseConfig   `yaml:"database"`
	Embeddings      EmbeddingConfig  `yaml:"embeddings"`
	Phoenix         PhoenixConfig    `yaml:"phoenix"`
}

// ResolvedUpstreams returns the effective upstream list.
// If Upstreams is empty and OllamaURL is set, it synthesises a single
// fallback upstream from OllamaURL for backward compatibility.
func (c *Config) ResolvedUpstreams() []UpstreamConfig {
	if len(c.Upstreams) > 0 {
		return c.Upstreams
	}
	if c.OllamaURL != "" {
		return []UpstreamConfig{{Name: "default", URL: c.OllamaURL}}
	}
	return nil
}
