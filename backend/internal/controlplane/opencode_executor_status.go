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
	client        *http.Client
}

func NewEnvOpenCodeExecutorStatusProvider() *EnvOpenCodeExecutorStatusProvider {
	return &EnvOpenCodeExecutorStatusProvider{
		ocqBaseURL:    strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey: os.Getenv("OCQ_GATEWAY_KEY"),
		client:        &http.Client{Timeout: 2 * time.Second},
	}
}

func (p *EnvOpenCodeExecutorStatusProvider) Status(ctx context.Context) OpenCodeExecutorStatus {
	now := time.Now().UTC()
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
