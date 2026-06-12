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

type OCQRequest struct {
	Model     string
	Prompt    string
	SessionID string
}

type OCQResponse struct {
	Content        string
	SessionID      string
	RequestID      string
	CorrelationIDs map[string]string
}

type OCQExecutor interface {
	SendPrompt(ctx context.Context, request OCQRequest) (OCQResponse, error)
}

type EnvOCQExecutor struct {
	baseURL    string
	gatewayKey string
	client     *http.Client
}

func NewEnvOCQExecutor() *EnvOCQExecutor {
	return &EnvOCQExecutor{baseURL: strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"), gatewayKey: os.Getenv("OCQ_GATEWAY_KEY"), client: &http.Client{Timeout: 30 * time.Second}}
}

func (e *EnvOCQExecutor) SendPrompt(ctx context.Context, request OCQRequest) (OCQResponse, error) {
	if e.baseURL == "" {
		return OCQResponse{}, fmt.Errorf("OCQ_BASE_URL is not configured")
	}
	endpoint, err := url.JoinPath(e.baseURL, "/v1/chat/completions")
	if err != nil {
		return OCQResponse{}, err
	}
	body := map[string]any{"model": request.Model, "stream": false, "messages": []map[string]string{{"role": "user", "content": request.Prompt}}}
	if request.SessionID != "" {
		body["ocq_session_id"] = request.SessionID
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return OCQResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return OCQResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.gatewayKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.gatewayKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return OCQResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return OCQResponse{}, fmt.Errorf("ocq request failed: %s", resp.Status)
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
		return OCQResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return OCQResponse{}, fmt.Errorf("ocq response contained no choices")
	}
	sessionID := firstNonEmpty(resp.Header.Get("X-OCQ-Session"), decoded.OCQSessionID, request.SessionID)
	requestID := resp.Header.Get("X-OCQ-Request-ID")
	correlation := map[string]string{}
	if sessionID != "" {
		correlation["ocq_session_id"] = sessionID
	}
	if requestID != "" {
		correlation["ocq_request_id"] = requestID
	}
	return OCQResponse{Content: decoded.Choices[0].Message.Content, SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, nil
}
