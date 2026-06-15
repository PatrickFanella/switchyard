package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// ModelBroker is the Switchyard-native model generation contract.
//
// It is intentionally small while llama-line remains the transitional
// model-broker backend. OCQ remains the transitional OpenCode executor path.
// Workflow code should depend on this interface instead of calling a
// service-specific model client directly. Future broker features such as queue
// state, routing policy, upstream health, and cancellation handles should grow
// from this contract rather than from imported service boundaries.
type ModelBroker interface {
	Generate(ctx context.Context, request ModelRequest) (ModelResponse, error)
}

type ModelBrokerConfiguration interface {
	Configured() bool
}

// ModelExecutor is kept as a compatibility alias for existing workflow tests and
// call sites. New code should use ModelBroker terminology.
type ModelExecutor = ModelBroker

type EnvModelBroker struct {
	llamaLineBaseURL string
	llamaLineAPIKey  string
	client           *http.Client
}

func NewEnvModelBroker() *EnvModelBroker {
	return &EnvModelBroker{
		llamaLineBaseURL: strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:  os.Getenv("LLAMA_LINE_API_KEY"),
		client:           &http.Client{Timeout: modelBrokerTimeout()},
	}
}

func modelBrokerTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MODEL_BROKER_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 10 * time.Minute
}

// NewEnvModelExecutor preserves the previous constructor name while returning
// the native broker implementation.
func NewEnvModelExecutor() *EnvModelBroker {
	return NewEnvModelBroker()
}

func (e *EnvModelBroker) Generate(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	baseURL := e.llamaLineBaseURL
	token := e.llamaLineAPIKey
	source := "llama-line"
	if baseURL == "" {
		return ModelResponse{}, fmt.Errorf("no model broker base URL configured")
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
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ModelResponse{}, err
	}
	decoded, err := decodeModelBrokerChatResponse(bodyBytes)
	if err != nil {
		return ModelResponse{}, err
	}
	if len(decoded.Choices) == 0 {
		return ModelResponse{}, fmt.Errorf("model response contained no choices")
	}
	correlation := map[string]string{"model_broker": "switchyard", "model_source": source}
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

type modelBrokerChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func decodeModelBrokerChatResponse(body []byte) (modelBrokerChatResponse, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "data:") {
		var completion string
		var lastStatus string
		for _, line := range strings.Split(trimmed, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			if strings.HasPrefix(line, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "[DONE]" {
					break
				}
				if strings.Contains(payload, `"choices"`) {
					completion = payload
				} else if payload != "" {
					lastStatus = payload
				}
			}
		}
		if completion != "" {
			trimmed = completion
		} else if lastStatus != "" {
			return modelBrokerChatResponse{}, fmt.Errorf("model broker stream ended without chat completion; last event: %s", lastStatus)
		} else {
			return modelBrokerChatResponse{}, errors.New("model broker stream ended without chat completion")
		}
	}
	var decoded modelBrokerChatResponse
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return modelBrokerChatResponse{}, err
	}
	return decoded, nil
}

func (e *EnvModelBroker) Configured() bool {
	return strings.TrimSpace(e.llamaLineBaseURL) != ""
}
