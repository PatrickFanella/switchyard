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
	client           *http.Client
}

func NewEnvAdapterProbe() *EnvAdapterProbe {
	return &EnvAdapterProbe{
		llamaLineBaseURL: strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		client:           &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvAdapterProbe) Payload(ctx context.Context) AdapterPayload {
	payload := staticAdapterPayload()
	if p.llamaLineBaseURL == "" {
		return payload
	}
	p.applyLlamaLineStatus(ctx, &payload)
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

func boolString(ok bool) int {
	if ok {
		return 1
	}
	return 0
}
