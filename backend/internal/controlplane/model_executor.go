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
	ollamaBaseURL    string
	openCodeBaseURL  string
	openCodeUsername string
	openCodePassword string
	client           *http.Client
}

func NewEnvModelBroker() *EnvModelBroker {
	return &EnvModelBroker{
		llamaLineBaseURL: strings.TrimRight(os.Getenv("LLAMA_LINE_BASE_URL"), "/"),
		llamaLineAPIKey:  os.Getenv("LLAMA_LINE_API_KEY"),
		ollamaBaseURL:    strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/"),
		openCodeBaseURL:  strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/"),
		openCodeUsername: os.Getenv("OPENCODE_SERVER_USERNAME"),
		openCodePassword: os.Getenv("OPENCODE_SERVER_PASSWORD"),
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
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND")), "llama-line") {
		if strings.TrimSpace(e.llamaLineBaseURL) == "" {
			return ModelResponse{}, fmt.Errorf("LLAMA_LINE_BASE_URL is not configured")
		}
		return e.generateViaLlamaLine(ctx, request)
	}
	if e.shouldGenerateViaOllama(request.Model) {
		return e.generateViaOllama(ctx, request)
	}
	return e.generateViaOpenCode(ctx, request)
}

func (e *EnvModelBroker) shouldGenerateViaOllama(model string) bool {
	backend := strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND"))
	if strings.EqualFold(backend, "ollama") {
		return true
	}
	if strings.TrimSpace(e.ollamaBaseURL) == "" {
		return false
	}
	name := strings.ToLower(strings.TrimSpace(trimModelProviderPrefix(model, "ollama")))
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "ollama/") {
		return true
	}
	for _, prefix := range strings.Split(firstNonEmpty(os.Getenv("MODEL_BROKER_OLLAMA_MODELS"), "llama,qwen,mistral,codellama,nomic-embed,bge,deepseek,gemma,phi"), ",") {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (e *EnvModelBroker) generateViaLlamaLine(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	baseURL := e.llamaLineBaseURL
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
	if e.llamaLineAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.llamaLineAPIKey)
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
	correlation := map[string]string{"model_broker": "switchyard", "model_source": "llama-line", "model_broker_backend": "compat"}
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

func (e *EnvModelBroker) generateViaOllama(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if strings.TrimSpace(e.ollamaBaseURL) == "" {
		return ModelResponse{}, fmt.Errorf("OLLAMA_BASE_URL is not configured")
	}
	endpoint, err := url.JoinPath(e.ollamaBaseURL, "/api/chat")
	if err != nil {
		return ModelResponse{}, err
	}
	body, err := json.Marshal(map[string]any{"model": ollamaModelName(request.Model), "stream": false, "messages": request.Messages})
	if err != nil {
		return ModelResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ModelResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return ModelResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return ModelResponse{}, fmt.Errorf("ollama request failed: %s", resp.Status)
	}
	var decoded struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ModelResponse{}, err
	}
	content := firstNonEmpty(decoded.Message.Content, decoded.Response)
	if strings.TrimSpace(content) == "" {
		return ModelResponse{}, fmt.Errorf("ollama response did not include content")
	}
	return ModelResponse{Content: content, CorrelationIDs: map[string]string{"model_broker": "switchyard", "model_source": "ollama", "model_broker_backend": "native", "ollama_model": ollamaModelName(request.Model)}}, nil
}

func ollamaModelName(model string) string {
	return trimModelProviderPrefix(model, "ollama")
}

func trimModelProviderPrefix(model string, provider string) string {
	trimmed := strings.TrimSpace(model)
	prefix := provider + "/"
	if strings.HasPrefix(strings.ToLower(trimmed), strings.ToLower(prefix)) {
		return strings.TrimSpace(trimmed[len(prefix):])
	}
	return trimmed
}

func (e *EnvModelBroker) generateViaOpenCode(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	executor := &EnvOpenCodeExecutor{baseURL: e.openCodeBaseURL, username: e.openCodeUsername, password: e.openCodePassword, client: e.client}
	prompt := modelMessagesToPrompt(request.Messages)
	response, err := executor.SendPrompt(ctx, OpenCodeRequest{Model: request.Model, Prompt: prompt, RequestID: "model-broker-" + shortRandomID(), WorkDir: firstNonEmpty(os.Getenv("MODEL_BROKER_WORKDIR"), os.Getenv("OCQ_DEFAULT_DIRECTORY"), "/workspace")})
	if err != nil {
		return ModelResponse{}, err
	}
	correlation := cloneMap(response.CorrelationIDs)
	if correlation == nil {
		correlation = map[string]string{}
	}
	correlation["model_broker"] = "switchyard"
	correlation["model_source"] = "opencode"
	correlation["model_broker_backend"] = "native"
	return ModelResponse{Content: response.Content, CorrelationIDs: correlation}, nil
}

func modelMessagesToPrompt(messages []ModelMessage) string {
	var builder strings.Builder
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(strings.ToUpper(role[:1]) + strings.ToLower(role[1:]) + ":\n")
		builder.WriteString(content)
	}
	return builder.String()
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
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND")), "llama-line") {
		return strings.TrimSpace(e.llamaLineBaseURL) != ""
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND")), "ollama") {
		return strings.TrimSpace(e.ollamaBaseURL) != ""
	}
	return strings.TrimSpace(e.openCodeBaseURL) != ""
}
