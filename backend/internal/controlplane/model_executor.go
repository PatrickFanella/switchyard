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

type ModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ModelRequest struct {
	Model    string         `json:"model"`
	Messages []ModelMessage `json:"messages"`
}

type ModelResponse struct {
	Content        string
	CorrelationIDs map[string]string
}

type ModelExecutor interface {
	Generate(ctx context.Context, request ModelRequest) (ModelResponse, error)
}

type EnvModelExecutor struct {
	llamaLineBaseURL string
	llamaLineAPIKey  string
	ocqBaseURL       string
	ocqGatewayKey    string
	client           *http.Client
}

func NewEnvModelExecutor() *EnvModelExecutor {
	return &EnvModelExecutor{
		llamaLineBaseURL: strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:  os.Getenv("LLAMA_LINE_API_KEY"),
		ocqBaseURL:       strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/"),
		ocqGatewayKey:    os.Getenv("OCQ_GATEWAY_KEY"),
		client:           &http.Client{Timeout: 10 * time.Second},
	}
}

func (e *EnvModelExecutor) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	baseURL := e.llamaLineBaseURL
	token := e.llamaLineAPIKey
	source := "llama-line"
	if baseURL == "" {
		baseURL = e.ocqBaseURL
		token = e.ocqGatewayKey
		source = "ocq"
	}
	if baseURL == "" {
		return ModelResponse{}, fmt.Errorf("no model executor base URL configured")
	}
	endpoint, err := url.JoinPath(baseURL, "/v1/chat/completions")
	if err != nil {
		return ModelResponse{}, err
	}
	body, err := json.Marshal(map[string]any{"model": request.Model, "stream": false, "messages": request.Messages})
	if err != nil {
		return ModelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ModelResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return ModelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ModelResponse{}, fmt.Errorf("model request failed: %s", resp.Status)
	}
	var decoded struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ModelResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return ModelResponse{}, fmt.Errorf("model response contained no choices")
	}
	correlation := map[string]string{"model_source": source}
	if requestID := resp.Header.Get("X-Llama-Line-Request-ID"); requestID != "" {
		correlation["llama_line_request_id"] = requestID
	}
	if requestID := resp.Header.Get("X-OCQ-Request-ID"); requestID != "" {
		correlation["ocq_request_id"] = requestID
	}
	if sessionID := resp.Header.Get("X-OCQ-Session"); sessionID != "" {
		correlation["ocq_session_id"] = sessionID
	}
	return ModelResponse{Content: decoded.Choices[0].Message.Content, CorrelationIDs: correlation}, nil
}
