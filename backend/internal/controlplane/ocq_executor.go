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

	"git.subcult.tv/PatrickFanella/switchyard/internal/runtimeconfig"
)

type OpenCodeRequest struct {
	Model     string
	Agent     string
	Prompt    string
	SessionID string
	RequestID string
	WorkDir   string
	// FailClosedOnStaleSession prevents reposting after a correlated session is
	// lost or after any message request may have crossed the mutation boundary.
	FailClosedOnStaleSession bool
	PreAttempt               PreAttemptCallback
	// OnCorrelation is called as soon as an upstream session identity exists,
	// before a potentially long-running message request starts.
	OnCorrelation func(map[string]string) error
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
	baseURL       string
	gatewayKey    string
	username      string
	password      string
	client        *http.Client
	forceOpenCode bool
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
	return runtimeconfig.Duration("OPENCODE_EXECUTOR_TIMEOUT")
}

func openCodeSessionCreateTimeout() time.Duration {
	return durationFromEnv("OPENCODE_SESSION_CREATE_TIMEOUT", runtimeconfig.Duration("OPENCODE_SESSION_CREATE_TIMEOUT"))
}

func openCodeMessageTimeout() time.Duration {
	return durationFromEnv("OPENCODE_MESSAGE_TIMEOUT", runtimeconfig.Duration("OPENCODE_MESSAGE_TIMEOUT"))
}

func openCodeSessionStatusPollInterval() time.Duration {
	return durationFromEnv("OPENCODE_SESSION_STATUS_POLL_INTERVAL", 100*time.Millisecond)
}

func openCodeAbortTimeout() time.Duration {
	return durationFromEnv("OPENCODE_ABORT_TIMEOUT", runtimeconfig.Duration("OPENCODE_ABORT_TIMEOUT"))
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
	if decision.Backend == "ocq" && !e.forceOpenCode {
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
	requestID := firstNonEmpty(request.RequestID, "opencode-"+shortRandomID())
	request.RequestID = requestID
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
	correlation := nativeOpenCodeCorrelation(sessionID, requestID)
	publishCorrelation := func() error {
		if request.OnCorrelation == nil {
			return nil
		}
		return request.OnCorrelation(correlation)
	}
	if err := publishCorrelation(); err != nil {
		e.abortOpenCodeSession(context.Background(), sessionID)
		return OpenCodeResponse{SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, err
	}
	messageCtx, cancel := context.WithTimeout(ctx, openCodeMessageTimeout())
	defer cancel()
	replaced := false
	originalSessionProvided := request.SessionID != ""
	messageAttempted := false
	for {
		priorMessageID, err := e.openCodeLatestMessageID(messageCtx, sessionID)
		if err == nil {
			preAttemptPending := request.PreAttempt != nil
			messageAttempted = true
			_, err = e.postOpenCodeMessage(messageCtx, sessionID, request)
			if preAttemptPending {
				request.PreAttempt = nil
			}
		}
		content := ""
		if err == nil {
			content, err = e.waitForOpenCodeSessionIdle(messageCtx, sessionID, priorMessageID)
		}
		if err == nil {
			return OpenCodeResponse{Content: content, SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, nil
		}
		mayReplaceStale := !request.FailClosedOnStaleSession || (!originalSessionProvided && !messageAttempted)
		if isStaleOpenCodeSession(err) && !replaced && mayReplaceStale {
			createCtx, createCancel := context.WithTimeout(messageCtx, openCodeSessionCreateTimeout())
			preAttemptPending := request.PreAttempt != nil
			replacementID, createErr := e.createOpenCodeSession(createCtx, directory, request.PreAttempt)
			createCancel()
			if preAttemptPending {
				request.PreAttempt = nil
			}
			if createErr != nil {
				return OpenCodeResponse{SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, createErr
			}
			sessionID = replacementID
			correlation = nativeOpenCodeCorrelation(sessionID, requestID)
			replaced = true
			if correlationErr := publishCorrelation(); correlationErr != nil {
				e.abortOpenCodeSession(context.Background(), sessionID)
				return OpenCodeResponse{SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, correlationErr
			}
			continue
		}
		var providerErr openCodeProviderError
		if errors.As(err, &providerErr) {
			correlation["provider_status"] = strconv.Itoa(providerErr.status)
			correlation["execution_retryable"] = strconv.FormatBool(providerErr.retryable)
			if correlationErr := publishCorrelation(); correlationErr != nil {
				return OpenCodeResponse{SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, errors.Join(err, correlationErr)
			}
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || strings.Contains(strings.ToLower(err.Error()), "deadline") {
			e.abortOpenCodeSession(context.Background(), sessionID)
		}
		return OpenCodeResponse{SessionID: sessionID, RequestID: requestID, CorrelationIDs: correlation}, err
	}
}

type staleOpenCodeSessionError struct {
	operation string
	status    string
	body      string
}

func (e *staleOpenCodeSessionError) Error() string {
	return fmt.Sprintf("opencode %s failed: %s %s", e.operation, e.status, e.body)
}

func isStaleOpenCodeSession(err error) bool {
	var staleErr *staleOpenCodeSessionError
	return errors.As(err, &staleErr)
}

func (e *EnvOpenCodeExecutor) waitForOpenCodeSessionIdle(ctx context.Context, sessionID, priorMessageID string) (string, error) {
	ticker := time.NewTicker(openCodeSessionStatusPollInterval())
	defer ticker.Stop()
	for {
		content, idle, err := e.openCodeSessionIdle(ctx, sessionID, priorMessageID)
		if err != nil {
			return "", err
		}
		if idle {
			return content, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *EnvOpenCodeExecutor) openCodeSessionIdle(ctx context.Context, sessionID, priorMessageID string) (string, bool, error) {
	message, err := e.openCodeLatestMessage(ctx, sessionID)
	if err != nil {
		return "", false, err
	}
	if message.ID == priorMessageID || message.Role != "assistant" {
		return "", false, nil
	}
	if message.Error != nil {
		if message.Error.Name == "MessageAbortedError" {
			return "", false, context.Canceled
		}
		status := message.Error.Data.StatusCode
		if status < 100 || status > 599 {
			status = 0
		}
		retryable := modelBrokerRetryableStatus(status)
		if message.Error.Data.IsRetryable != nil {
			retryable = retryable && *message.Error.Data.IsRetryable
		}
		return "", false, openCodeProviderError{status: status, retryable: retryable}
	}
	if message.Finish != "stop" {
		return "", false, nil
	}
	var content strings.Builder
	for _, part := range message.Parts {
		if part.Type == "text" {
			content.WriteString(part.Text)
		}
	}
	return strings.TrimSpace(content.String()), true, nil
}

type openCodeMessageError struct {
	Name string `json:"name"`
	Data struct {
		StatusCode  int   `json:"statusCode"`
		IsRetryable *bool `json:"isRetryable"`
	} `json:"data"`
}

// Provider text, bodies and headers may contain credentials; never retain them.
type openCodeProviderError struct {
	status    int
	retryable bool
}

func (e openCodeProviderError) Retryable() bool { return e.retryable }

func (e openCodeProviderError) Error() string {
	if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
		return fmt.Sprintf("OpenCode provider authentication failed (HTTP %d); refresh the managed provider sign-in before retrying", e.status)
	}
	if e.status != 0 {
		return fmt.Sprintf("OpenCode provider request failed (HTTP %d)", e.status)
	}
	return "OpenCode provider reported a terminal error; inspect the correlated session"
}

type openCodeSessionMessage struct {
	Error *openCodeMessageError

	ID     string
	Role   string
	Finish string
	Parts  []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
}

func (e *EnvOpenCodeExecutor) openCodeLatestMessageID(ctx context.Context, sessionID string) (string, error) {
	message, err := e.openCodeLatestMessage(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return message.ID, nil
}

func (e *EnvOpenCodeExecutor) openCodeLatestMessage(ctx context.Context, sessionID string) (openCodeSessionMessage, error) {
	endpoint, err := url.JoinPath(e.baseURL, "/session/", sessionID, "/message")
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	endpoint += "?limit=1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	e.applyOpenCodeAuth(req)
	resp, err := e.client.Do(req)
	if err != nil {
		return openCodeSessionMessage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := readModelResponseBody(resp.Body)
		if resp.StatusCode == http.StatusNotFound {
			return openCodeSessionMessage{}, &staleOpenCodeSessionError{operation: "session status", status: resp.Status, body: strings.TrimSpace(string(body))}
		}
		return openCodeSessionMessage{}, fmt.Errorf("opencode session status failed: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var payload json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return openCodeSessionMessage{}, err
	}
	if len(payload) == 0 || payload[0] != '[' {
		return openCodeSessionMessage{}, errors.New("opencode session messages response is not an array")
	}
	var messages []struct {
		Info struct {
			ID     string                `json:"id"`
			Role   string                `json:"role"`
			Finish string                `json:"finish"`
			Error  *openCodeMessageError `json:"error"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(payload, &messages); err != nil {
		return openCodeSessionMessage{}, err
	}
	if len(messages) == 0 {
		return openCodeSessionMessage{}, nil
	}
	message := messages[len(messages)-1]
	return openCodeSessionMessage{ID: message.Info.ID, Role: message.Info.Role, Finish: message.Info.Finish, Parts: message.Parts, Error: message.Info.Error}, nil
}

func nativeOpenCodeCorrelation(sessionID, requestID string) map[string]string {
	return map[string]string{
		"opencode_executor":         "switchyard",
		"opencode_executor_backend": "native",
		"opencode_session_id":       sessionID,
		"opencode_request_id":       requestID,
	}
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
	if request.Agent != "" {
		body["agent"] = request.Agent
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
		if resp.StatusCode == http.StatusNotFound {
			return "", &staleOpenCodeSessionError{operation: "message", status: resp.Status, body: strings.TrimSpace(string(body))}
		}
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
