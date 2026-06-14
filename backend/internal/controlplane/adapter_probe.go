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
	llamaLineBaseURL   string
	ocqBaseURL         string
	ocqGatewayKey      string
	blacktowerBaseURL  string
	changemakerBaseURL string
	changemakerToken   string
	client             *http.Client
}

func NewEnvAdapterProbe() *EnvAdapterProbe {
	return &EnvAdapterProbe{
		llamaLineBaseURL:   strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		ocqBaseURL:         strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:      os.Getenv("OCQ_GATEWAY_KEY"),
		blacktowerBaseURL:  strings.TrimRight(os.Getenv("BLACKTOWER_BASE_URL"), "/"),
		changemakerBaseURL: strings.TrimRight(os.Getenv("CHANGEMAKER_BASE_URL"), "/"),
		changemakerToken:   os.Getenv("CHANGEMAKER_ADMIN_TOKEN"),
		client:             &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvAdapterProbe) Payload(ctx context.Context) AdapterPayload {
	payload := staticAdapterPayload()
	if p.llamaLineBaseURL != "" {
		p.applyLlamaLineStatus(ctx, &payload)
	}
	if p.ocqBaseURL != "" {
		p.applyOCQStatus(ctx, &payload)
	}
	if p.blacktowerBaseURL != "" {
		p.applyBlacktowerStatus(ctx, &payload)
	}
	if p.changemakerBaseURL != "" {
		p.applyChangemakerStatus(ctx, &payload)
	}
	return payload
}

type llamaLineBrokerStatus struct {
	QueueDepth       int                       `json:"queue_depth"`
	MaxDepth         int                       `json:"max_depth"`
	InFlightClient   string                    `json:"in_flight_client"`
	InFlightID       string                    `json:"in_flight_id"`
	ConnectedClients int                       `json:"connected_clients"`
	Upstreams        []llamaLineStatusUpstream `json:"upstreams"`
}

type llamaLineStatusUpstream struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Healthy     *bool  `json:"healthy"`
	LastProbeAt string `json:"last_probe_at"`
	QueueDepth  int    `json:"queue_depth"`
	MaxDepth    int    `json:"max_depth"`
	InFlightID  string `json:"in_flight_id"`
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

type blacktowerSnapshot struct {
	Tasks []struct {
		Name      string   `json:"name"`
		Badges    []string `json:"badges"`
		Warnings  []string `json:"warnings"`
		NextRunAt string   `json:"nextRunAt"`
		LatestRun struct {
			ID          string `json:"id"`
			Status      string `json:"status"`
			StartedAt   string `json:"startedAt"`
			CompletedAt string `json:"completedAt"`
			SessionID   string `json:"sessionId"`
		} `json:"latestRun"`
	} `json:"tasks"`
	SelectedTask struct {
		Report struct {
			Path      string `json:"path"`
			UpdatedAt string `json:"updatedAt"`
			Content   string `json:"content"`
		} `json:"report"`
	} `json:"selectedTask"`
	Health struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
		Tasks   struct {
			Total   int `json:"total"`
			Enabled int `json:"enabled"`
			Invalid int `json:"invalid"`
		} `json:"tasks"`
		RecentFailureCount int    `json:"recentFailureCount"`
		CheckedAt          string `json:"checkedAt"`
	} `json:"health"`
	SchedulerEvents []struct {
		Source    string `json:"source"`
		Severity  string `json:"severity"`
		Text      string `json:"text"`
		TaskName  string `json:"taskName"`
		Timestamp string `json:"timestamp"`
	} `json:"schedulerEvents"`
	GeneratedAt string `json:"generatedAt"`
}

func (p *EnvAdapterProbe) applyBlacktowerStatus(ctx context.Context, payload *AdapterPayload) {
	var snapshot blacktowerSnapshot
	if err := p.getJSON(ctx, p.blacktowerBaseURL, "/api/snapshot", &snapshot); err != nil {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "blacktower", Status: "unreachable", Details: err.Error()})
		return
	}
	now := parseTimeOrNow(firstNonEmpty(snapshot.GeneratedAt, snapshot.Health.CheckedAt))
	payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "blacktower", Status: normalizeHealthStatus(snapshot.Health.Status), Details: firstNonEmpty(snapshot.Health.Summary, p.blacktowerBaseURL)})
	payload.Blacktower.Tasks = make([]AdapterBlacktowerTask, 0, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		updated := parseTimeOrNow(firstNonEmpty(task.LatestRun.CompletedAt, task.LatestRun.StartedAt, task.NextRunAt, snapshot.GeneratedAt))
		status := firstNonEmpty(task.LatestRun.Status, statusFromWarnings(task.Warnings), "scheduled")
		progress := progressFromStatus(status)
		payload.Blacktower.Tasks = append(payload.Blacktower.Tasks, AdapterBlacktowerTask{ID: firstNonEmpty(task.LatestRun.ID, task.Name), Name: task.Name, Status: status, Owner: "blacktower", Progress: progress, UpdatedAt: updated})
		if task.LatestRun.SessionID != "" {
			payload.Sessions = append(payload.Sessions, AdapterSession{ID: task.LatestRun.SessionID, Name: task.Name, Surface: "blacktower", Status: status, StartedAt: parseTimeOrNow(task.LatestRun.StartedAt), UpdatedAt: updated})
		}
	}
	if snapshot.SelectedTask.Report.Path != "" || snapshot.SelectedTask.Report.Content != "" {
		payload.Blacktower.Reports = []AdapterBlacktowerReport{{ID: firstNonEmpty(snapshot.SelectedTask.Report.Path, "blacktower-report"), Title: firstReportLine(snapshot.SelectedTask.Report.Content), Type: "control-center", Status: "ready", Findings: reportFindings(snapshot.SelectedTask.Report.Content), GeneratedAt: parseTimeOrNow(firstNonEmpty(snapshot.SelectedTask.Report.UpdatedAt, snapshot.GeneratedAt))}}
	}
	for index, event := range snapshot.SchedulerEvents {
		payload.Logs = append(payload.Logs, AdapterLog{ID: firstNonEmpty(event.TaskName, event.Source, "blacktower") + "-" + stringID(index), Level: firstNonEmpty(event.Severity, "info"), Source: "blacktower", Message: event.Text, CreatedAt: parseTimeOrNow(event.Timestamp)})
	}
	payload.Blacktower.SchedulerHealth = AdapterSchedulerHealth{Status: normalizeHealthStatus(snapshot.Health.Status), LastTickAt: now, NextRunAt: now, SkewSeconds: snapshot.Health.RecentFailureCount}
}

type changemakerRunStatus struct {
	State      string   `json:"state"`
	StartedAt  string   `json:"startedAt"`
	FinishedAt string   `json:"finishedAt"`
	Error      string   `json:"error"`
	Logs       []string `json:"logs"`
}

type changemakerPost struct {
	FileName string   `json:"fileName"`
	Date     string   `json:"date"`
	Title    string   `json:"title"`
	Status   string   `json:"status"`
	Tags     []string `json:"tags"`
}

func (p *EnvAdapterProbe) applyChangemakerStatus(ctx context.Context, payload *AdapterPayload) {
	if err := p.getJSON(ctx, p.changemakerBaseURL, "/health", nil); err != nil {
		payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "changemaker", Status: "unreachable", Details: err.Error()})
		return
	}
	payload.Health.Services = append(payload.Health.Services, AdapterService{Name: "changemaker", Status: "healthy", Details: p.changemakerBaseURL})
	payload.Changemaker.Service = AdapterChangemakerService{Name: "changemaker", Status: "healthy", Version: "external", Healthy: true, UpdatedAt: time.Now().UTC()}

	var runStatus changemakerRunStatus
	if err := p.getJSON(ctx, p.changemakerBaseURL, "/api/runs/status", &runStatus); err == nil {
		payload.Changemaker.RunControl = AdapterChangemakerRunControl{Enabled: true, Mode: firstNonEmpty(runStatus.State, "idle"), ActiveRunID: activeRunID(runStatus), QueuedRuns: 0}
		for index, line := range runStatus.Logs {
			payload.Logs = append(payload.Logs, AdapterLog{ID: "changemaker-log-" + stringID(index), Level: levelFromRunState(runStatus.State), Source: "changemaker", Message: line, CreatedAt: parseTimeOrNow(firstNonEmpty(runStatus.FinishedAt, runStatus.StartedAt))})
		}
	}

	var posts []changemakerPost
	if err := p.getJSON(ctx, p.changemakerBaseURL, "/api/posts", &posts); err == nil {
		payload.Changemaker.Posts = make([]AdapterChangemakerPost, 0, len(posts))
		for _, post := range posts {
			payload.Changemaker.Posts = append(payload.Changemaker.Posts, AdapterChangemakerPost{ID: firstNonEmpty(post.FileName, post.Title), Title: firstNonEmpty(post.Title, post.FileName), Status: firstNonEmpty(post.Status, "draft"), Tags: post.Tags, PublishedAt: parseDateOrTime(post.Date)})
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
	if p.ocqGatewayKey != "" && baseURL == p.ocqBaseURL {
		req.Header.Set("Authorization", "Bearer "+p.ocqGatewayKey)
	}
	if p.changemakerToken != "" && baseURL == p.changemakerBaseURL {
		req.Header.Set("Authorization", "Bearer "+p.changemakerToken)
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

func normalizeHealthStatus(status string) string {
	switch strings.ToLower(status) {
	case "ok":
		return "healthy"
	case "warning":
		return "degraded"
	case "error":
		return "failed"
	case "":
		return "unknown"
	default:
		return status
	}
}

func statusFromWarnings(warnings []string) string {
	if len(warnings) > 0 {
		return "warning"
	}
	return ""
}

func progressFromStatus(status string) int {
	switch strings.ToLower(status) {
	case "completed", "succeeded", "healthy", "ok":
		return 100
	case "running":
		return 50
	case "failed", "cancelled", "canceled", "error":
		return 0
	default:
		return 10
	}
}

func firstReportLine(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
		if line != "" {
			return line
		}
	}
	return "Blacktower report"
}

func reportFindings(content string) []string {
	findings := []string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "-* "))
		if line != "" && !strings.HasPrefix(line, "#") {
			findings = append(findings, line)
		}
		if len(findings) == 3 {
			break
		}
	}
	if len(findings) == 0 {
		return []string{"report available"}
	}
	return findings
}

func stringID(index int) string {
	return strings.TrimPrefix(time.Unix(int64(index), 0).UTC().Format("150405"), "00")
}

func activeRunID(status changemakerRunStatus) string {
	if status.State == "running" {
		return "changemaker-active"
	}
	return ""
}

func levelFromRunState(state string) string {
	if state == "failed" {
		return "error"
	}
	return "info"
}

func parseDateOrTime(value string) time.Time {
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed
	}
	return parseTimeOrNow(value)
}
