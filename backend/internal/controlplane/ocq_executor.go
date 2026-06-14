package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type OpenCodeRequest struct {
	Model     string
	Prompt    string
	SessionID string
	RequestID string
}

type OpenCodeResponse struct {
	Content        string
	SessionID      string
	RequestID      string
	CorrelationIDs map[string]string
}

type OpenCodeExecutor interface {
	SendPrompt(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error)
}

type OCQRequest = OpenCodeRequest
type OCQResponse = OpenCodeResponse
type OCQExecutor = OpenCodeExecutor

type EnvOpenCodeExecutor struct {
	baseURL    string
	gatewayKey string
	client     *http.Client
}

type EnvOCQExecutor = EnvOpenCodeExecutor

func NewEnvOpenCodeExecutor() *EnvOpenCodeExecutor {
	return &EnvOpenCodeExecutor{baseURL: strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"), gatewayKey: os.Getenv("OCQ_GATEWAY_KEY"), client: &http.Client{Timeout: 30 * time.Second}}
}

func NewEnvOCQExecutor() *EnvOpenCodeExecutor {
	return NewEnvOpenCodeExecutor()
}

func (e *EnvOpenCodeExecutor) SendPrompt(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error) {
	if e.baseURL == "" {
		return OpenCodeResponse{}, fmt.Errorf("OCQ_BASE_URL is not configured")
	}
	endpoint, err := url.JoinPath(e.baseURL, "/v1/chat/completions")
	if err != nil {
		return OpenCodeResponse{}, err
	}
	body := map[string]any{"model": request.Model, "stream": false, "messages": []map[string]string{{"role": "user", "content": request.Prompt}}}
	if request.SessionID != "" {
		body["ocq_session_id"] = request.SessionID
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return OpenCodeResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return OpenCodeResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if request.RequestID != "" {
		req.Header.Set("X-Request-ID", request.RequestID)
	}
	if e.gatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.gatewayKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return OpenCodeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return OpenCodeResponse{}, fmt.Errorf("opencode request failed through ocq: %s", resp.Status)
	}
	var decoded struct {
		OCQSessionID string `json:"ocq_session_id"`
		Choices      []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return OpenCodeResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return OpenCodeResponse{}, fmt.Errorf("opencode executor response contained no choices")
	}
	sessionID := firstNonEmpty(resp.Header.Get("X-OCQ-Session"), decoded.OCQSessionID, request.SessionID)
	requestID := firstNonEmpty(resp.Header.Get("X-OCQ-Request-ID"), request.RequestID)
	correlation := map[string]string{}
	if sessionID != "" {
		correlation["ocq_session_id"] = sessionID
	}
	if requestID != "" {
		correlation["ocq_request_id"] = requestID
	}
	correlation["opencode_executor"] = "switchyard"
	correlation["opencode_executor_backend"] = "ocq"
	return OpenCodeResponse{Content: decoded.Choices[0].Message.Content, SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, nil
}
