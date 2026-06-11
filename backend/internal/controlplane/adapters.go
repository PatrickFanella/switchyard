package controlplane

import "time"

var adapterSampleTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

type AdapterPayload struct {
	Queues      []AdapterQueue     `json:"queues"`
	Sessions    []AdapterSession   `json:"sessions"`
	Logs        []AdapterLog       `json:"logs"`
	Prompts     []AdapterPrompt    `json:"prompts"`
	Config      AdapterConfig      `json:"config"`
	Agents      []AdapterAgent     `json:"agents"`
	Skills      []AdapterSkill     `json:"skills"`
	Health      AdapterHealth      `json:"health"`
	LlamaLine   AdapterLlamaLine   `json:"llama_line"`
	OCQ         AdapterOCQ         `json:"ocq"`
	Blacktower  AdapterBlacktower  `json:"blacktower"`
	Changemaker AdapterChangemaker `json:"changemaker"`
}

type AdapterQueue struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Depth     int       `json:"depth"`
	Capacity  int       `json:"capacity"`
	Workers   int       `json:"workers"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterSession struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Surface    string    `json:"surface"`
	Status     string    `json:"status"`
	WorkflowID string    `json:"workflow_id,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type AdapterLog struct {
	ID        string    `json:"id"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AdapterPrompt struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Surface     string   `json:"surface"`
	Template    string   `json:"template"`
	Variables   []string `json:"variables"`
	Description string   `json:"description"`
}

type AdapterConfig struct {
	Environment string         `json:"environment"`
	Version     string         `json:"version"`
	BaseURL     string         `json:"base_url"`
	Features    []string       `json:"features"`
	Limits      map[string]int `json:"limits"`
}

type AdapterAgent struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Status       string   `json:"status"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}

type AdapterSkill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Category    string `json:"category"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

type AdapterHealth struct {
	Status    string           `json:"status"`
	Services  []AdapterService `json:"services"`
	CheckedAt time.Time        `json:"checked_at"`
}

type AdapterService struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Details string `json:"details,omitempty"`
}

type AdapterLlamaLine struct {
	Requests  []AdapterLlamaRequest      `json:"requests"`
	Stats     AdapterLlamaStats          `json:"stats"`
	Search    []AdapterLlamaSearchResult `json:"search"`
	Upstreams []AdapterLlamaUpstream     `json:"upstreams"`
}

type AdapterLlamaRequest struct {
	ID         string    `json:"id"`
	Model      string    `json:"model"`
	Route      string    `json:"route"`
	Status     string    `json:"status"`
	LatencyMS  int       `json:"latency_ms"`
	QueueDepth int       `json:"queue_depth"`
	CreatedAt  time.Time `json:"created_at"`
}

type AdapterLlamaStats struct {
	ActiveRequests int     `json:"active_requests"`
	QueuedRequests int     `json:"queued_requests"`
	AvgLatencyMS   int     `json:"avg_latency_ms"`
	SuccessRate    float64 `json:"success_rate"`
}

type AdapterLlamaSearchResult struct {
	Query  string  `json:"query"`
	Match  string  `json:"match"`
	Source string  `json:"source"`
	Score  float64 `json:"score"`
}

type AdapterLlamaUpstream struct {
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	Status     string    `json:"status"`
	Pending    int       `json:"pending"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type AdapterOCQ struct {
	Requests []AdapterOCQRequest `json:"requests"`
	Logs     []AdapterOCQLog     `json:"logs"`
	Sessions []AdapterOCQSession `json:"sessions"`
	Settings AdapterOCQSettings  `json:"settings"`
}

type AdapterOCQRequest struct {
	ID        string    `json:"id"`
	Workflow  string    `json:"workflow"`
	Trigger   string    `json:"trigger"`
	Status    string    `json:"status"`
	Actor     string    `json:"actor"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterOCQLog struct {
	ID        string    `json:"id"`
	Level     string    `json:"level"`
	Channel   string    `json:"channel"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AdapterOCQSession struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	User      string    `json:"user"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterOCQSettings struct {
	Workspace     string   `json:"workspace"`
	Provider      string   `json:"provider"`
	RetentionDays int      `json:"retention_days"`
	CORSAllowed   []string `json:"cors_allowed"`
}

type AdapterBlacktower struct {
	Tasks           []AdapterBlacktowerTask   `json:"tasks"`
	Reports         []AdapterBlacktowerReport `json:"reports"`
	SchedulerHealth AdapterSchedulerHealth    `json:"scheduler_health"`
}

type AdapterBlacktowerTask struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Owner     string    `json:"owner"`
	Progress  int       `json:"progress"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterBlacktowerReport struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	Findings    []string  `json:"findings"`
	GeneratedAt time.Time `json:"generated_at"`
}

type AdapterSchedulerHealth struct {
	Status      string    `json:"status"`
	NextRunAt   time.Time `json:"next_run_at"`
	LastTickAt  time.Time `json:"last_tick_at"`
	SkewSeconds int       `json:"skew_seconds"`
}

type AdapterChangemaker struct {
	Posts      []AdapterChangemakerPost     `json:"posts"`
	Service    AdapterChangemakerService    `json:"service"`
	RunControl AdapterChangemakerRunControl `json:"run_control"`
}

type AdapterChangemakerPost struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Status      string    `json:"status"`
	Tags        []string  `json:"tags"`
	PublishedAt time.Time `json:"published_at"`
}

type AdapterChangemakerService struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Version   string    `json:"version"`
	Healthy   bool      `json:"healthy"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AdapterChangemakerRunControl struct {
	Enabled     bool   `json:"enabled"`
	Mode        string `json:"mode"`
	ActiveRunID string `json:"active_run_id,omitempty"`
	QueuedRuns  int    `json:"queued_runs"`
}
