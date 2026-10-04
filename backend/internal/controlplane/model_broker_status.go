package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type ModelBrokerStatusProvider interface {
	Status(ctx context.Context) ModelBrokerStatus
}

type ModelBrokerStatus struct {
	ID                   string                      `json:"id"`
	Status               string                      `json:"status"`
	Configured           bool                        `json:"configured"`
	Backend              string                      `json:"backend"`
	TransitionalBackend  string                      `json:"transitional_backend,omitempty"`
	EffectiveSelector    string                      `json:"effective_selector"`
	ConfiguredSelector   string                      `json:"configured_selector,omitempty"`
	CompatibilityWarning string                      `json:"compatibility_warning,omitempty"`
	BaseURL              string                      `json:"base_url,omitempty"`
	QueueDepth           int                         `json:"queue_depth"`
	MaxDepth             int                         `json:"max_depth"`
	ActiveRequest        bool                        `json:"active_request"`
	ConnectedClients     int                         `json:"connected_clients"`
	InFlightRequestIDs   []string                    `json:"in_flight_request_ids"`
	Upstreams            []ModelBrokerStatusUpstream `json:"upstreams"`
	CheckedAt            time.Time                   `json:"checked_at"`
	Error                string                      `json:"error,omitempty"`
}

type ModelBrokerStatusUpstream struct {
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Status      string     `json:"status"`
	QueueDepth  int        `json:"queue_depth"`
	MaxDepth    int        `json:"max_depth"`
	InFlightID  string     `json:"in_flight_id,omitempty"`
	LastProbeAt *time.Time `json:"last_probe_at,omitempty"`
}

type EnvModelBrokerStatusProvider struct {
	llamaLineBaseURL   string
	ollamaBaseURL      string
	openCodeBaseURL    string
	username           string
	password           string
	backend            string
	configuredSelector string
	client             *http.Client
}

func NewEnvModelBrokerStatusProvider() *EnvModelBrokerStatusProvider {
	return &EnvModelBrokerStatusProvider{
		llamaLineBaseURL:   strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		ollamaBaseURL:      strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/"),
		openCodeBaseURL:    strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096/api"), "/"),
		username:           os.Getenv("OPENCODE_SERVER_USERNAME"),
		password:           os.Getenv("OPENCODE_SERVER_PASSWORD"),
		backend:            effectiveModelBrokerSelector(os.Getenv("MODEL_BROKER_BACKEND")),
		configuredSelector: strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND")),
		client:             &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvModelBrokerStatusProvider) Status(ctx context.Context) ModelBrokerStatus {
	now := time.Now().UTC()
	if strings.EqualFold(strings.TrimSpace(p.backend), "ollama") {
		return p.ollamaStatus(ctx, now)
	}
	if !strings.EqualFold(strings.TrimSpace(p.backend), "llama-line") {
		return p.nativeStatus(ctx, now)
	}
	status := ModelBrokerStatus{
		ID:                   "switchyard-model-broker",
		Status:               "not_configured",
		Configured:           strings.TrimSpace(p.llamaLineBaseURL) != "",
		Backend:              "switchyard-model-broker",
		TransitionalBackend:  "llama-line",
		EffectiveSelector:    "llama-line",
		ConfiguredSelector:   p.configuredSelector,
		CompatibilityWarning: "deprecated-external-backend",
		BaseURL:              p.llamaLineBaseURL,
		InFlightRequestIDs:   []string{},
		Upstreams:            []ModelBrokerStatusUpstream{},
		CheckedAt:            now,
	}
	if !status.Configured {
		status.Error = "LLAMA_LINE_BASE_URL is not configured"
		return status
	}
	statusURL, err := url.JoinPath(p.llamaLineBaseURL, "/broker/status")
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	resp, err := p.client.Do(req)
	if err != nil {
		status.Status = "unreachable"
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		status.Status = "failed"
		status.Error = resp.Status
		return status
	}
	var broker llamaLineBrokerStatus
	if err := json.NewDecoder(resp.Body).Decode(&broker); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	status.Status = modelBrokerHealthStatus(broker.Upstreams)
	status.QueueDepth = broker.QueueDepth
	status.MaxDepth = broker.MaxDepth
	status.InFlightRequestIDs = modelBrokerInFlightIDs(broker)
	status.ActiveRequest = broker.InFlightClient != "" || len(status.InFlightRequestIDs) > 0
	status.ConnectedClients = broker.ConnectedClients
	status.Upstreams = make([]ModelBrokerStatusUpstream, 0, len(broker.Upstreams))
	for _, upstream := range broker.Upstreams {
		upstreamStatus := "unknown"
		if upstream.Healthy != nil && *upstream.Healthy {
			upstreamStatus = "healthy"
		} else if upstream.Healthy != nil {
			upstreamStatus = "failed"
		}
		status.Upstreams = append(status.Upstreams, ModelBrokerStatusUpstream{
			Name:        upstream.Name,
			URL:         upstream.URL,
			Status:      upstreamStatus,
			QueueDepth:  upstream.QueueDepth,
			MaxDepth:    upstream.MaxDepth,
			InFlightID:  upstream.InFlightID,
			LastProbeAt: parseOptionalRFC3339(upstream.LastProbeAt),
		})
	}
	return status
}

func (p *EnvModelBrokerStatusProvider) ollamaStatus(ctx context.Context, now time.Time) ModelBrokerStatus {
	status := ModelBrokerStatus{ID: "switchyard-model-broker", Status: "not_configured", Configured: strings.TrimSpace(p.ollamaBaseURL) != "", Backend: "switchyard-model-broker", EffectiveSelector: "ollama", ConfiguredSelector: p.configuredSelector, BaseURL: p.ollamaBaseURL, InFlightRequestIDs: []string{}, Upstreams: []ModelBrokerStatusUpstream{}, CheckedAt: now}
	if !status.Configured {
		status.Error = "OLLAMA_BASE_URL is not configured"
		return status
	}
	endpoint, err := url.JoinPath(p.ollamaBaseURL, "/api/tags")
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	resp, err := p.client.Do(req)
	if err != nil {
		status.Status = "unreachable"
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		status.Status = "failed"
		status.Error = resp.Status
		return status
	}
	status.Status = "healthy"
	status.ConnectedClients = 1
	status.Upstreams = []ModelBrokerStatusUpstream{{Name: "ollama", URL: p.ollamaBaseURL, Status: "healthy"}}
	return status
}

func (p *EnvModelBrokerStatusProvider) nativeStatus(ctx context.Context, now time.Time) ModelBrokerStatus {
	status := ModelBrokerStatus{ID: "switchyard-model-broker", Status: "not_configured", Configured: strings.TrimSpace(p.openCodeBaseURL) != "", Backend: "switchyard-model-broker", EffectiveSelector: "native", ConfiguredSelector: p.configuredSelector, BaseURL: p.openCodeBaseURL, InFlightRequestIDs: []string{}, Upstreams: []ModelBrokerStatusUpstream{}, CheckedAt: now}
	if nested := resolveOpenCodeExecutorBackend(); nested.Backend == "ocq" {
		status.TransitionalBackend = nested.Backend
		status.EffectiveSelector = nested.Backend
		status.CompatibilityWarning = nested.CompatibilityWarning
		status.BaseURL = strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/")
		status.Configured = status.BaseURL != ""
		executorStatus := NewEnvOpenCodeExecutorStatusProvider().Status(ctx)
		status.Status = executorStatus.Status
		status.Error = executorStatus.Error
		status.ConnectedClients = executorStatus.SessionCount
		status.Upstreams = []ModelBrokerStatusUpstream{{Name: "ocq", URL: status.BaseURL, Status: executorStatus.Status}}
		return status
	}
	if !status.Configured {
		status.Error = "OPENCODE_SERVER_URL is not configured"
		return status
	}
	endpoint, err := url.JoinPath(p.openCodeBaseURL, "/config")
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	if p.username != "" || p.password != "" {
		req.SetBasicAuth(openCodeUsername(p.openCodeBaseURL, p.username), p.password)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		status.Status = "unreachable"
		status.Error = err.Error()
		return status
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		status.Status = "failed"
		status.Error = resp.Status
		return status
	}
	status.Status = "healthy"
	status.Upstreams = []ModelBrokerStatusUpstream{{Name: "opencode", URL: p.openCodeBaseURL, Status: "healthy"}}
	status.ConnectedClients = 1
	return status
}

func modelBrokerInFlightIDs(broker llamaLineBrokerStatus) []string {
	seen := map[string]bool{}
	ids := []string{}
	if strings.TrimSpace(broker.InFlightID) != "" {
		ids = append(ids, broker.InFlightID)
		seen[broker.InFlightID] = true
	}
	for _, upstream := range broker.Upstreams {
		id := strings.TrimSpace(upstream.InFlightID)
		if id == "" || seen[id] {
			continue
		}
		ids = append(ids, id)
		seen[id] = true
	}
	return ids
}

func modelBrokerHealthStatus(upstreams []llamaLineStatusUpstream) string {
	if len(upstreams) == 0 {
		return "degraded"
	}
	unknown := false
	for _, upstream := range upstreams {
		if upstream.Healthy == nil {
			unknown = true
			continue
		}
		if !*upstream.Healthy {
			return "degraded"
		}
	}
	if unknown {
		return "unknown"
	}
	return "healthy"
}

func parseOptionalRFC3339(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}
