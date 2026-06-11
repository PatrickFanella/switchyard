package server

import (
	"encoding/json"
	"net/http"
	"time"
)

type brokerStatusQueueItem struct {
	ID          string    `json:"id"`
	Client      string    `json:"client"`
	Priority    int       `json:"priority"`
	Position    int       `json:"position"`
	EnqueuedAt  time.Time `json:"enqueued_at"`
	WaitSeconds int       `json:"wait_seconds"`
}

type brokerStatusUpstream struct {
	Name                 string                  `json:"name"`
	URL                  string                  `json:"url"`
	Models               []string                `json:"models,omitempty"`
	Healthy              *bool                   `json:"healthy,omitempty"`
	LastProbeAt          string                  `json:"last_probe_at,omitempty"`
	LastProbeMS          *int64                  `json:"last_probe_ms,omitempty"`
	LastProbeError       *string                 `json:"last_probe_error,omitempty"`
	ConsecutiveFailures  *int                    `json:"consecutive_failures,omitempty"`
	QueueDepth           int                     `json:"queue_depth"`
	MaxDepth             int                     `json:"max_depth"`
	InFlightClient       string                  `json:"in_flight_client,omitempty"`
	InFlightID           string                  `json:"in_flight_id,omitempty"`
	InFlightPriority     int                     `json:"in_flight_priority,omitempty"`
	InFlightEnqueuedAt   time.Time               `json:"in_flight_enqueued_at,omitempty"`
	InFlightStartedAt    time.Time               `json:"in_flight_started_at,omitempty"`
	InFlightJobSeconds   int                     `json:"in_flight_job_seconds,omitempty"`
	InFlightTotalSeconds int                     `json:"in_flight_total_seconds,omitempty"`
	Queue                []brokerStatusQueueItem `json:"queue"`
}

type brokerStatusResponse struct {
	// Primary queue totals (sum across all upstreams) for backward compat.
	QueueDepth       int                     `json:"queue_depth"`
	MaxDepth         int                     `json:"max_depth"`
	InFlightClient   string                  `json:"in_flight_client"`
	InFlightID       string                  `json:"in_flight_id,omitempty"`
	ConnectedClients int32                   `json:"connected_clients"`
	Queue            []brokerStatusQueueItem `json:"queue"`
	// Per-upstream breakdown (v2).
	Upstreams []brokerStatusUpstream `json:"upstreams,omitempty"`
	// Cache stats (v2, optional).
	CacheStats []clientCacheStats `json:"cache_stats,omitempty"`
}

type clientCacheStats struct {
	Client string `json:"client"`
	Hits   int64  `json:"hits"`
	Misses int64  `json:"misses"`
	Size   int    `json:"size"`
}

// HandleBrokerStatus returns current queue state. Does not require auth.
func (s *Server) HandleBrokerStatus(w http.ResponseWriter, r *http.Request) {
	// Build per-upstream data.
	upstreams := make([]brokerStatusUpstream, 0, len(s.upstreamEntries))
	totalDepth := 0
	totalMax := 0
	var primaryInFlightClient, primaryInFlightID string
	var primaryItems []brokerStatusQueueItem
	now := time.Now()

	for i := range s.upstreamEntries {
		e := &s.upstreamEntries[i]
		snap := e.queue.Snapshot()
		health := e.healthSnapshot()
		items := make([]brokerStatusQueueItem, len(snap.Items))
		for j, it := range snap.Items {
			items[j] = brokerStatusQueueItem{
				ID:          it.ID,
				Client:      it.ClientName,
				Priority:    it.Priority,
				Position:    it.Position,
				EnqueuedAt:  it.EnqueuedAt,
				WaitSeconds: int(now.Sub(it.EnqueuedAt).Seconds()),
			}
		}
		jobSeconds := 0
		if !snap.InFlightStartedAt.IsZero() {
			jobSeconds = int(now.Sub(snap.InFlightStartedAt).Seconds())
		}
		totalSeconds := 0
		if !snap.InFlightEnqueuedAt.IsZero() {
			totalSeconds = int(now.Sub(snap.InFlightEnqueuedAt).Seconds())
		}
		upstreams = append(upstreams, brokerStatusUpstream{
			Name:                 e.cfg.Name,
			URL:                  e.cfg.URL,
			Models:               e.cfg.Models,
			QueueDepth:           snap.Depth,
			MaxDepth:             snap.MaxDepth,
			InFlightClient:       snap.InFlightClient,
			InFlightID:           snap.InFlightID,
			InFlightPriority:     snap.InFlightPriority,
			InFlightEnqueuedAt:   snap.InFlightEnqueuedAt,
			InFlightStartedAt:    snap.InFlightStartedAt,
			InFlightJobSeconds:   jobSeconds,
			InFlightTotalSeconds: totalSeconds,
			Queue:                items,
		})
		if health.Healthy != nil {
			lastProbeMS := health.LastProbeMS
			lastProbeError := health.LastProbeError
			consecutiveFailures := health.ConsecutiveFailures
			upstreams[len(upstreams)-1].Healthy = health.Healthy
			upstreams[len(upstreams)-1].LastProbeAt = formatProbeTime(health.LastProbeAt)
			upstreams[len(upstreams)-1].LastProbeMS = &lastProbeMS
			upstreams[len(upstreams)-1].LastProbeError = &lastProbeError
			upstreams[len(upstreams)-1].ConsecutiveFailures = &consecutiveFailures
		}
		totalDepth += snap.Depth
		totalMax += snap.MaxDepth
		if i == 0 {
			primaryInFlightClient = snap.InFlightClient
			primaryInFlightID = snap.InFlightID
			primaryItems = items
		}
	}

	resp := brokerStatusResponse{
		QueueDepth:       totalDepth,
		MaxDepth:         totalMax,
		InFlightClient:   primaryInFlightClient,
		InFlightID:       primaryInFlightID,
		ConnectedClients: s.connectedClients.Load(),
		Queue:            primaryItems,
		Upstreams:        upstreams,
		CacheStats:       s.cacheStats(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Ollama-Broker", "true")
	json.NewEncoder(w).Encode(resp)
}

func formatProbeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// cacheStats returns per-client cache statistics.
func (s *Server) cacheStats() []clientCacheStats {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if len(s.caches) == 0 {
		return nil
	}
	out := make([]clientCacheStats, 0, len(s.caches))
	for name, c := range s.caches {
		hits, misses, size := c.Stats()
		out = append(out, clientCacheStats{
			Client: name,
			Hits:   hits,
			Misses: misses,
			Size:   size,
		})
	}
	return out
}
