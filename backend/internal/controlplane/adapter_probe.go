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

type AdapterProbe interface {
	Payload(ctx context.Context) AdapterPayload
}

type EnvAdapterProbe struct {
	llamaLineBaseURL string
	ocqBaseURL       string
	ocqGatewayKey    string
	client           *http.Client
}

func NewEnvAdapterProbe() *EnvAdapterProbe {
	return &EnvAdapterProbe{
		llamaLineBaseURL: strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		ocqBaseURL:       strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:    os.Getenv("OCQ_GATEWAY_KEY"),
		client:           &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvAdapterProbe) Payload(ctx context.Context) AdapterPayload {
	payload := staticAdapterPayload()
	if p.llamaLineBaseURL == "" {
		if p.ocqBaseURL == "" {
			return payload
		}
	} else {
		p.applyLlamaLineStatus(ctx, &payload)
	}
	if p.ocqBaseURL != "" {
		p.applyOCQStatus(ctx, &payload)
	}
	return payload
}

type llamaLineBrokerStatus struct {
	QueueDepth       int                       `json:"queue_depth"`
	MaxDepth         int                       `json:"max_depth"`
	InFlightClient   string                    `json:"in_flight_client"`
	ConnectedClients int                       `json:"connected_clients"`
	Upstreams        []llamaLineStatusUpstream `json:"upstreams"`
}

type llamaLineStatusUpstream struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Healthy     *bool  `json:"healthy"`
	LastProbeAt string `json:"last_probe_at"`
}

func (p *EnvAdapterProbe) applyLlamaLineStatus(ctx context.Context, payload *AdapterPayload) {
	statusURL, err := url.JoinPath(p.llamaLineBaseURL, "/broker/status")
	if err != nil {
		payload.LlamaLine.Upstreams = []AdapterLlamaUpstream{{Name: "llama-line", URL: p.llamaLineBaseURL, Status: "failed", LastSeenAt: time.Now().UTC()}}
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return
	}
	resp, err := p.client.Do(req)
	if err != nil {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "llama-line", Status: "unreachable", Details: err.Error()})
		payload.LlamaLine.Upstreams = []AdapterLlamaUpstream{{Name: "llama-line", URL: p.llamaLineBaseURL, Status: "unreachable", LastSeenAt: time.Now().UTC()}}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "llama-line", Status: "failed", Details: resp.Status})
		return
	}
	var broker llamaLineBrokerStatus
	if err := json.NewDecoder(resp.Body).Decode(&broker); err != nil {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "llama-line", Status: "failed", Details: err.Error()})
		return
	}
	payload.Queues = []AdapterQueue{{
		ID:        "llama-line",
		Name:      "llama-line broker queue",
		Status:    "healthy",
		Depth:     broker.QueueDepth,
		Capacity:  broker.MaxDepth,
		Workers:   max(1, broker.ConnectedClients),
		UpdatedAt: time.Now().UTC(),
	}}
	payload.LlamaLine.Stats.ActiveRequests = boolString(broker.InFlightClient != "")
	payload.LlamaLine.Stats.QueuedRequests = broker.QueueDepth
	payload.LlamaLine.Upstreams = make([]AdapterLlamaUpstream, 0, len(broker.Upstreams))
	for _, upstream := range broker.Upstreams {
		status := "unknown"
		if upstream.Healthy != nil && *upstream.Healthy {
			status = "healthy"
		} else if upstream.Healthy != nil {
			status = "failed"
		}
		lastSeen := time.Now().UTC()
		if parsed, err := time.Parse(time.RFC3339, upstream.LastProbeAt); err == nil {
			lastSeen = parsed
		}
		payload.LlamaLine.Upstreams = append(payload.LlamaLine.Upstreams, AdapterLlamaUpstream{Name: upstream.Name, URL: upstream.URL, Status: status, Pending: broker.QueueDepth, LastSeenAt: lastSeen})
	}
	payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "llama-line", Status: "healthy", Details: p.llamaLineBaseURL})
}

func (p *EnvAdapterProbe) applyOCQStatus(ctx context.Context, payload *AdapterPayload) {
	if err := p.getJSON(ctx, p.ocqBaseURL, "/healthz", nil); err != nil {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "ocq", Status: "unreachable", Details: err.Error()})
		return
	}
	payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "ocq", Status: "healthy", Details: p.ocqBaseURL})
	payload.OCQ.Settings.Provider = "ocq"
	payload.OCQ.Settings.Workspace = p.ocqBaseURL

	var sessions struct {
		Sessions []struct {
			ID        string `json:"id"`
			Title     string `json:"title"`
			Name      string `json:"name"`
			Status    string `json:"status"`
			User      string `json:"user"`
			CreatedAt string `json:"created_at"`
			UpdatedAt string `json:"updated_at"`
		} `json:"sessions"`
	}
	if err := p.getJSON(ctx, p.ocqBaseURL, "/ocq/sessions", &sessions); err == nil {
		payload.OCQ.Sessions = make([]AdapterOCQSession, 0, len(sessions.Sessions))
		payload.Sessions = make([]AdapterSession, 0, len(sessions.Sessions))
		for _, session := range sessions.Sessions {
			started := parseTimeOrNow(session.CreatedAt)
			updated := parseTimeOrNow(session.UpdatedAt)
			title := firstNonEmpty(session.Title, session.Name, session.ID)
			status := firstNonEmpty(session.Status, "active")
			payload.OCQ.Sessions = append(payload.OCQ.Sessions, AdapterOCQSession{ID: session.ID, Title: title, Status: status, User: firstNonEmpty(session.User, "operator"), StartedAt: started, UpdatedAt: updated})
			payload.Sessions = append(payload.Sessions, AdapterSession{ID: session.ID, Name: title, Surface: "ocq", Status: status, StartedAt: started, UpdatedAt: updated})
		}
	}
}

func (p *EnvAdapterProbe) getJSON(ctx context.Context, baseURL string, path string, target any) error {
	requestURL, err := url.JoinPath(baseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	if p.ocqGatewayKey != "" && strings.Contains(baseURL, p.ocqBaseURL) {
		req.Header.Set("Authorization", "Bearer "+p.ocqGatewayKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &adapterHTTPError{Status: resp.Status}
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

type adapterHTTPError struct {
	Status string
}

func (e *adapterHTTPError) Error() string { return e.Status }

func parseTimeOrNow(value string) time.Time {
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	return time.Now().UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func boolString(ok bool) int {
	if ok {
		return 1
	}
	return 0
}
