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

type OpenCodeExecutorStatusProvider interface {
	Status(ctx context.Context) OpenCodeExecutorStatus
}

type OpenCodeExecutorStatus struct {
	ID                  string                          `json:"id"`
	Status              string                          `json:"status"`
	Configured          bool                            `json:"configured"`
	Backend             string                          `json:"backend"`
	TransitionalBackend string                          `json:"transitional_backend,omitempty"`
	BaseURL             string                          `json:"base_url,omitempty"`
	Healthy             bool                            `json:"healthy"`
	SessionCount        int                             `json:"session_count"`
	ActiveSessionCount  int                             `json:"active_session_count"`
	Sessions            []OpenCodeExecutorStatusSession `json:"sessions"`
	CheckedAt           time.Time                       `json:"checked_at"`
	Error               string                          `json:"error,omitempty"`
}

type OpenCodeExecutorStatusSession struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	User      string     `json:"user,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type EnvOpenCodeExecutorStatusProvider struct {
	ocqBaseURL    string
	ocqGatewayKey string
	openCodeURL   string
	username      string
	password      string
	backend       string
	client        *http.Client
}

func NewEnvOpenCodeExecutorStatusProvider() *EnvOpenCodeExecutorStatusProvider {
	return &EnvOpenCodeExecutorStatusProvider{
		ocqBaseURL:    strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey: os.Getenv("OCQ_GATEWAY_KEY"),
		openCodeURL:   strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/"),
		username:      os.Getenv("OPENCODE_SERVER_USERNAME"),
		password:      os.Getenv("OPENCODE_SERVER_PASSWORD"),
		backend:       firstNonEmpty(os.Getenv("OPENCODE_EXECUTOR_BACKEND"), "native"),
		client:        &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvOpenCodeExecutorStatusProvider) Status(ctx context.Context) OpenCodeExecutorStatus {
	now := time.Now().UTC()
	if !strings.EqualFold(strings.TrimSpace(p.backend), "ocq") {
		return p.nativeStatus(ctx, now)
	}
	status := OpenCodeExecutorStatus{
		ID:                  "switchyard-opencode-executor",
		Status:              "not_configured",
		Configured:          strings.TrimSpace(p.ocqBaseURL) != "",
		Backend:             "switchyard-opencode-executor",
		TransitionalBackend: "ocq",
		BaseURL:             p.ocqBaseURL,
		Sessions:            []OpenCodeExecutorStatusSession{},
		CheckedAt:           now,
	}
	if !status.Configured {
		status.Error = "OCQ_BASE_URL is not configured"
		return status
	}
	if err := p.getJSON(ctx, "/healthz", nil); err != nil {
		status.Status = "unreachable"
		status.Error = err.Error()
		return status
	}
	var sessions ocqSessionsResponse
	if err := p.getJSON(ctx, "/ocq/sessions", &sessions); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	status.Healthy = true
	status.Status = "healthy"
	status.Sessions = make([]OpenCodeExecutorStatusSession, 0, len(sessions.Sessions))
	for _, session := range sessions.Sessions {
		sessionStatus := firstNonEmpty(session.Status, "active")
		if isOpenCodeActiveSessionStatus(sessionStatus) {
			status.ActiveSessionCount++
		}
		status.Sessions = append(status.Sessions, OpenCodeExecutorStatusSession{
			ID:        session.ID,
			Title:     firstNonEmpty(session.Title, session.Name, session.ID),
			Status:    sessionStatus,
			User:      session.User,
			CreatedAt: parseOptionalRFC3339(firstNonEmpty(string(session.CreatedAt), string(session.CreatedAtLegacy))),
			UpdatedAt: parseOptionalRFC3339(firstNonEmpty(string(session.UpdatedAt), string(session.UpdatedAtLegacy))),
		})
	}
	status.SessionCount = len(status.Sessions)
	return status
}

func (p *EnvOpenCodeExecutorStatusProvider) nativeStatus(ctx context.Context, now time.Time) OpenCodeExecutorStatus {
	status := OpenCodeExecutorStatus{ID: "switchyard-opencode-executor", Status: "not_configured", Configured: strings.TrimSpace(p.openCodeURL) != "", Backend: "switchyard-opencode-executor", BaseURL: p.openCodeURL, Sessions: []OpenCodeExecutorStatusSession{}, CheckedAt: now}
	if !status.Configured {
		status.Error = "OPENCODE_SERVER_URL is not configured"
		return status
	}
	endpoint, err := url.Parse(p.openCodeURL + "/session")
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	q := endpoint.Query()
	q.Set("directory", firstNonEmpty(os.Getenv("OCQ_DEFAULT_DIRECTORY"), "/workspace"))
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	if p.username != "" || p.password != "" {
		req.SetBasicAuth(firstNonEmpty(p.username, "switchyard"), p.password)
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
	var sessions []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Name      string `json:"name"`
		Status    string `json:"status"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sessions); err != nil {
		status.Status = "failed"
		status.Error = err.Error()
		return status
	}
	status.Healthy = true
	status.Status = "healthy"
	for _, session := range sessions {
		sessionStatus := firstNonEmpty(session.Status, "active")
		if isOpenCodeActiveSessionStatus(sessionStatus) {
			status.ActiveSessionCount++
		}
		status.Sessions = append(status.Sessions, OpenCodeExecutorStatusSession{ID: session.ID, Title: firstNonEmpty(session.Title, session.Name, session.ID), Status: sessionStatus, CreatedAt: parseOptionalRFC3339(session.CreatedAt), UpdatedAt: parseOptionalRFC3339(session.UpdatedAt)})
	}
	status.SessionCount = len(status.Sessions)
	return status
}

func (p *EnvOpenCodeExecutorStatusProvider) getJSON(ctx context.Context, path string, target any) error {
	endpoint, err := url.JoinPath(p.ocqBaseURL, path)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if p.ocqGatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.ocqGatewayKey)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusCodeError{Status: resp.Status, StatusCode: resp.StatusCode}
	}
	if target == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

type ocqSessionsResponse struct {
	Sessions []struct {
		ID              string            `json:"id"`
		Title           string            `json:"title"`
		Name            string            `json:"name"`
		Status          string            `json:"status"`
		User            string            `json:"user"`
		CreatedAt       ocqFlexibleString `json:"created_at"`
		UpdatedAt       ocqFlexibleString `json:"updated_at"`
		CreatedAtLegacy ocqFlexibleString `json:"createdAt"`
		UpdatedAtLegacy ocqFlexibleString `json:"updatedAt"`
	} `json:"sessions"`
}

type ocqFlexibleString string

func (s *ocqFlexibleString) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = ocqFlexibleString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*s = ocqFlexibleString(number.String())
		return nil
	}
	return nil
}

type statusCodeError struct {
	Status     string
	StatusCode int
}

func (e *statusCodeError) Error() string { return e.Status }

func isOpenCodeActiveSessionStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active", "running", "ready", "idle":
		return true
	default:
		return false
	}
}
