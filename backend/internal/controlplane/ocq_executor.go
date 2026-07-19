package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type OpenCodeRequest struct {
	Model      string
	Prompt     string
	SessionID  string
	RequestID  string
	WorkDir    string
	PreAttempt PreAttemptCallback
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
	username   string
	password   string
	client     *http.Client
}

type EnvOCQExecutor = EnvOpenCodeExecutor

func NewEnvOpenCodeExecutor() *EnvOpenCodeExecutor {
	baseURL := strings.TrimRight(firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"), "/")
	if resolveOpenCodeExecutorBackend().Backend == "ocq" {
		baseURL = strings.TrimRight(os.Getenv("OCQ_BASE_URL"), "/")
	}
	return &EnvOpenCodeExecutor{baseURL: baseURL, gatewayKey: os.Getenv("OCQ_GATEWAY_KEY"), username: os.Getenv("OPENCODE_SERVER_USERNAME"), password: os.Getenv("OPENCODE_SERVER_PASSWORD"), client: &http.Client{Timeout: openCodeExecutorTimeout()}}
}

func openCodeExecutorTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("OPENCODE_EXECUTOR_TIMEOUT")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 10 * time.Minute
}

func openCodeSessionCreateTimeout() time.Duration {
	return durationFromEnv("OPENCODE_SESSION_CREATE_TIMEOUT", 5*time.Second)
}

func openCodeMessageTimeout() time.Duration {
	return durationFromEnv("OPENCODE_MESSAGE_TIMEOUT", openCodeExecutorTimeout())
}

func openCodeAbortTimeout() time.Duration {
	return durationFromEnv("OPENCODE_ABORT_TIMEOUT", time.Second)
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	if raw := strings.TrimSpace(os.Getenv(name)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}

func NewEnvOCQExecutor() *EnvOpenCodeExecutor {
	return NewEnvOpenCodeExecutor()
}

func (e *EnvOpenCodeExecutor) SendPrompt(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error) {
	decision := resolveOpenCodeExecutorBackend()
	var response OpenCodeResponse
	var err error
	if decision.Backend == "ocq" {
		response, err = e.sendPromptViaOCQ(ctx, request)
	} else {
		response, err = e.sendPromptViaOpenCode(ctx, request)
	}
	response.CorrelationIDs = mergeStringMaps(response.CorrelationIDs, decision.CorrelationIDs())
	return response, err
}

func (e *EnvOpenCodeExecutor) sendPromptViaOCQ(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error) {
	if e.baseURL == "" {
		return OpenCodeResponse{CorrelationIDs: map[string]string{"ocq_request_id": request.RequestID}}, fmt.Errorf("OCQ_BASE_URL is not configured")
	}
	endpoint, err := url.JoinPath(e.baseURL, "/v1/chat/completions")
	if err != nil {
		return OpenCodeResponse{RequestID: request.RequestID, CorrelationIDs: map[string]string{"ocq_request_id": request.RequestID}}, err
	}
	body := map[string]any{"model": request.Model, "stream": false, "messages": []map[string]string{{"role": "user", "content": request.Prompt}}}
	if request.SessionID != "" {
		body["ocq_session_id"] = request.SessionID
	}
	if request.WorkDir != "" {
		body["directory"] = request.WorkDir
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
	if request.PreAttempt != nil {
		decision := resolveOpenCodeExecutorBackend()
		if callbackErr := request.PreAttempt(decision); callbackErr != nil {
			return OpenCodeResponse{RequestID: request.RequestID, CorrelationIDs: decision.CorrelationIDs()}, callbackErr
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return OpenCodeResponse{RequestID: request.RequestID, CorrelationIDs: map[string]string{"ocq_request_id": request.RequestID}}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return OpenCodeResponse{RequestID: request.RequestID, CorrelationIDs: map[string]string{"ocq_request_id": request.RequestID}}, fmt.Errorf("opencode request failed through ocq: %s", resp.Status)
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

func (e *EnvOpenCodeExecutor) sendPromptViaOpenCode(ctx context.Context, request OpenCodeRequest) (OpenCodeResponse, error) {
	if e.baseURL == "" {
		return OpenCodeResponse{}, fmt.Errorf("OPENCODE_SERVER_URL is not configured")
	}
	directory := firstNonEmpty(request.WorkDir, os.Getenv("OCQ_DEFAULT_DIRECTORY"), "/workspace")
	sessionID := request.SessionID
	if sessionID == "" {
		createCtx, cancel := context.WithTimeout(ctx, openCodeSessionCreateTimeout())
		created, err := e.createOpenCodeSession(createCtx, directory, request.PreAttempt)
		cancel()
		if err != nil {
			return OpenCodeResponse{RequestID: request.RequestID, CorrelationIDs: map[string]string{"opencode_request_id": request.RequestID}}, err
		}
		sessionID = created
		request.PreAttempt = nil
	}
	messageCtx, cancel := context.WithTimeout(ctx, openCodeMessageTimeout())
	content, err := e.postOpenCodeMessage(messageCtx, sessionID, request)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline") {
			e.abortOpenCodeSession(context.Background(), sessionID)
		}
		return OpenCodeResponse{SessionID: sessionID, RequestID: request.RequestID, CorrelationIDs: map[string]string{"opencode_session_id": sessionID, "opencode_request_id": request.RequestID}}, err
	}
	requestID := firstNonEmpty(request.RequestID, "opencode-"+shortRandomID())
	return OpenCodeResponse{Content: content, SessionID: sessionID, RequestID: requestID, CorrelationIDs: map[string]string{"opencode_executor": "switchyard", "opencode_executor_backend": "native", "opencode_session_id": sessionID, "opencode_request_id": requestID}}, nil
}

func (e *EnvOpenCodeExecutor) abortOpenCodeSession(ctx context.Context, sessionID string) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(e.baseURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, openCodeAbortTimeout())
	defer cancel()
	endpoint, err := url.JoinPath(e.baseURL, "/session/", sessionID, "/abort")
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	e.applyOpenCodeAuth(req)
	resp, err := e.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

func (e *EnvOpenCodeExecutor) createOpenCodeSession(ctx context.Context, directory string, preAttempt ...PreAttemptCallback) (string, error) {
	endpoint, err := url.Parse(e.baseURL + "/session")
	if err != nil {
		return "", err
	}
	q := endpoint.Query()
	q.Set("directory", directory)
	endpoint.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	e.applyOpenCodeAuth(req)
	if len(preAttempt) > 0 && preAttempt[0] != nil {
		decision := resolveOpenCodeExecutorBackend()
		if err := preAttempt[0](decision); err != nil {
			return "", err
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := readModelResponseBody(resp.Body)
		return "", fmt.Errorf("opencode session create failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", err
	}
	if decoded.ID == "" {
		return "", fmt.Errorf("opencode session create response missing id")
	}
	return decoded.ID, nil
}

func (e *EnvOpenCodeExecutor) postOpenCodeMessage(ctx context.Context, sessionID string, request OpenCodeRequest) (string, error) {
	endpoint, err := url.JoinPath(e.baseURL, "/session/", sessionID, "/message")
	if err != nil {
		return "", err
	}
	body := map[string]any{"parts": []map[string]string{{"type": "text", "text": request.Prompt}}}
	if request.Model != "" {
		body["model"] = openCodeModelPayload(request.Model)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if request.RequestID != "" {
		req.Header.Set("X-Request-ID", request.RequestID)
	}
	e.applyOpenCodeAuth(req)
	if request.PreAttempt != nil {
		decision := resolveOpenCodeExecutorBackend()
		if err := request.PreAttempt(decision); err != nil {
			return "", err
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := readModelResponseBody(resp.Body)
		return "", fmt.Errorf("opencode message failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	responseBody, err := readModelResponseBody(resp.Body)
	if err != nil {
		return "", err
	}
	return extractOpenCodeMessageContent(responseBody)
}

func (e *EnvOpenCodeExecutor) applyOpenCodeAuth(req *http.Request) {
	if e.username != "" || e.password != "" {
		req.SetBasicAuth(firstNonEmpty(e.username, "switchyard"), e.password)
	}
	if e.gatewayKey != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+e.gatewayKey)
	}
}

func openCodeModelPayload(model string) map[string]string {
	provider := "openai"
	modelID := model
	if strings.Contains(model, "/") {
		parts := strings.SplitN(model, "/", 2)
		provider = parts[0]
		modelID = parts[1]
	}
	return map[string]string{"providerID": provider, "modelID": modelID}
}

func extractOpenCodeMessageContent(body []byte) (string, error) {
	var decoded struct {
		Info struct {
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil {
		var parts []string
		for _, part := range decoded.Parts {
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n"), nil
		}
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return "", err
	}
	if content, ok := generic["content"].(string); ok && strings.TrimSpace(content) != "" {
		return content, nil
	}
	if message, ok := generic["message"].(string); ok && strings.TrimSpace(message) != "" {
		return message, nil
	}
	return strings.TrimSpace(string(body)), nil
}

func shortRandomID() string { return strconv.FormatInt(time.Now().UnixNano(), 36) }
