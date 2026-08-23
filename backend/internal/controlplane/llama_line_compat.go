package controlplane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

type llamaLineCompatChatRequest struct {
	Model      string         `json:"model"`
	Messages   []ModelMessage `json:"messages"`
	Prompt     string         `json:"prompt"`
	Stream     bool           `json:"stream"`
	Tools      []any          `json:"tools"`
	ToolChoice any            `json:"tool_choice"`
	Options    map[string]any `json:"options"`
}

func (a *API) llamaLineCompatStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status := a.modelBroker.Status(ctx)
	queued, inflight := a.compatBroker.snapshot(r.Context())
	inFlightIDs := a.compatBroker.inFlightIDs(r.Context())
	if len(inFlightIDs) > 0 {
		status.InFlightRequestIDs = inFlightIDs
		status.ActiveRequest = true
	}
	body := map[string]any{
		"status":                status.Status,
		"backend":               "switchyard-native",
		"effective_selector":    status.EffectiveSelector,
		"compatibility_warning": status.CompatibilityWarning,
		"queue_depth":           len(queued),
		"max_depth":             status.MaxDepth,
		"active_request":        len(inflight) > 0 || status.ActiveRequest,
		"connected_clients":     status.ConnectedClients,
		"in_flight_ids":         status.InFlightRequestIDs,
		"queue":                 queued,
		"inflight":              inflight,
		"upstreams":             status.Upstreams,
		"checked_at":            status.CheckedAt,
		"error":                 status.Error,
	}
	if status.ConfiguredSelector != "" {
		body["configured_selector"] = status.ConfiguredSelector
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *API) llamaLineCompatChatCompletions(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if len(input.Messages) == 0 && strings.TrimSpace(input.Prompt) != "" {
		input.Messages = []ModelMessage{{Role: "user", Content: input.Prompt}}
	}
	if compatAsyncRequested(r) && !input.Stream {
		a.llamaLineCompatAcceptAsync(w, r, input.Model, input.Messages)
		return
	}
	if input.Stream {
		a.llamaLineCompatStreamChatCompletions(w, r, input)
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input)
	if !ok {
		return
	}
	body := openAIChatCompletionBody(input.Model, response)
	writeJSON(w, http.StatusOK, body)
}

func (a *API) llamaLineCompatStreamChatCompletions(w http.ResponseWriter, r *http.Request, input llamaLineCompatChatRequest) {
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = firstNonEmpty(r.URL.Query().Get("model"), "gpt-5.4")
	}
	requestBody, _ := json.Marshal(map[string]any{"model": model, "messages": input.Messages, "path": r.URL.Path, "stream": true})
	requestID, ctx, finish := a.compatBroker.begin(r, model, string(requestBody))
	ctx, span := startControlPlaneSpan(ctx, "http.model_compat.stream",
		attribute.String("path", r.URL.Path),
		attribute.String("model", model),
		attribute.String("request_id", requestID),
	)
	var spanErr error
	defer func() {
		status := "completed"
		if spanErr != nil {
			status = "failed"
		}
		finishControlPlaneSpan(span, status, spanErr,
			attribute.String("path", r.URL.Path),
			attribute.String("model", model),
			attribute.String("request_id", requestID),
		)
	}()
	setCompatRequestHeaders(w.Header(), requestID, map[string]string{})
	broker := NewEnvModelBroker()
	setDeprecatedBackendHeaders(w.Header(), resolveModelExecutionRoute(model, strings.TrimSpace(broker.ollamaBaseURL) != ""))
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-OCQ-Status", "running")
	w.Header().Set("X-OCQ-Streaming-Mode", broker.StreamingMode(model))
	w.WriteHeader(http.StatusOK)
	writeCompatSSE(w, "status", map[string]any{"status": "queued", "request_id": requestID, "backend": "switchyard-native"})
	writeCompatSSE(w, "status", map[string]any{"status": "running", "request_id": requestID, "backend": "switchyard-native"})
	flushCompatSSE(w)

	var streamed strings.Builder
	var correlation map[string]string
	response, err := broker.Stream(ctx, ModelRequest{Model: model, Messages: input.Messages, Tools: input.Tools, ToolChoice: input.ToolChoice, Options: input.Options, RequestID: requestID, PreAttempt: func(decision BackendDecision) error {
		_, mergeErr := a.store.WithContext(ctx).MergeModelBrokerRequestCorrelationIDs(requestID, decision.CorrelationIDs())
		return mergeErr
	}}, func(event ModelStreamEvent) error {
		if event.CorrelationIDs != nil {
			correlation = event.CorrelationIDs
		}
		if event.Content != "" {
			streamed.WriteString(event.Content)
			payload, _ := json.Marshal(openAIChatCompletionChunkBody(model, requestID, event.Content, false))
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flushCompatSSE(w)
		}
		return nil
	})
	if err != nil {
		spanErr = err
		finish(ModelBrokerRequestFailed, `{}`, err.Error(), mergeStringMaps(response.CorrelationIDs, map[string]string{"llama_line_request_id": requestID}))
		writeCompatSSE(w, "error", map[string]any{"status": "failed", "request_id": requestID, "error": err.Error(), "backend": "switchyard-native"})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = correlation
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	if response.Content == "" {
		response.Content = streamed.String()
	}
	response.CorrelationIDs["llama_line_request_id"] = requestID
	setCompatRequestHeaders(w.Header(), requestID, response.CorrelationIDs)
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": response.CorrelationIDs})
	finish(ModelBrokerRequestCompleted, string(responseBody), "", response.CorrelationIDs)
	writeCompatSSE(w, "status", map[string]any{"status": "completed", "request_id": requestID, "backend": "switchyard-native"})
	finishPayload, _ := json.Marshal(openAIChatCompletionChunkBody(model, requestID, "", true))
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finishPayload)
}

func (a *API) llamaLineCompatCompletions(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if compatAsyncRequested(r) {
		a.llamaLineCompatAcceptAsync(w, r, input.Model, []ModelMessage{{Role: "user", Content: input.Prompt}})
		return
	}
	input.Messages = []ModelMessage{{Role: "user", Content: input.Prompt}}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": "cmpl-" + shortRandomID(), "object": "text_completion", "created": time.Now().Unix(), "model": input.Model, "choices": []map[string]any{{"index": 0, "text": response.Content, "finish_reason": "stop"}}})
}

func (a *API) llamaLineCompatChat(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if compatAsyncRequested(r) {
		a.llamaLineCompatAcceptAsync(w, r, input.Model, input.Messages)
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input)
	if !ok {
		return
	}
	message := map[string]any{"role": "assistant", "content": response.Content}
	if len(response.ToolCalls) > 0 {
		message["tool_calls"] = response.ToolCalls
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": input.Model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "message": message, "done": true})
}

func (a *API) llamaLineCompatGenerate(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if compatAsyncRequested(r) {
		a.llamaLineCompatAcceptAsync(w, r, input.Model, []ModelMessage{{Role: "user", Content: input.Prompt}})
		return
	}
	input.Messages = []ModelMessage{{Role: "user", Content: input.Prompt}}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": input.Model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "response": response.Content, "done": true})
}

func compatAsyncRequested(r *http.Request) bool {
	prefer := strings.ToLower(r.Header.Get("Prefer"))
	return strings.Contains(prefer, "respond-async") || strings.EqualFold(r.URL.Query().Get("async"), "true") || strings.EqualFold(r.URL.Query().Get("mode"), "async")
}

func (a *API) llamaLineCompatAcceptAsync(w http.ResponseWriter, r *http.Request, model string, messages []ModelMessage) {
	if strings.TrimSpace(model) == "" {
		model = firstNonEmpty(r.URL.Query().Get("model"), "gpt-5.4")
	}
	if err := validateModelRequestSize(model, messages); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	requestBody, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "path": r.URL.Path, "async": true})
	req, started, err := a.compatBroker.submitAsync(r, model, messages, string(requestBody))
	if err != nil {
		if errors.Is(err, ErrQueueFull) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, err.Error())
			return
		}
		writeControlError(w, err)
		return
	}
	setCompatRequestHeaders(w.Header(), req.ID, req.CorrelationIDs)
	w.Header().Set("Location", "/api/requests/"+req.ID+"/result")
	status := http.StatusAccepted
	if isTerminalModelBrokerRequest(req.Status) {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"id": req.ID, "request_id": req.ID, "status": req.Status, "accepted": true, "started": started, "result_url": "/api/requests/" + req.ID + "/result", "history_url": "/api/history/" + req.ID, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatGenerateModel(w http.ResponseWriter, r *http.Request, input llamaLineCompatChatRequest) (ModelResponse, bool) {
	model := input.Model
	if strings.TrimSpace(model) == "" {
		model = firstNonEmpty(r.URL.Query().Get("model"), "gpt-5.4")
	}
	if err := validateModelRequestSize(model, input.Messages); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		return ModelResponse{}, false
	}
	requestBody, _ := json.Marshal(map[string]any{"model": model, "messages": input.Messages, "tools": input.Tools, "path": r.URL.Path})
	requestID, ctx, finish := a.compatBroker.begin(r, model, string(requestBody))
	ctx, span := startControlPlaneSpan(ctx, "http.model_compat.generate",
		attribute.String("path", r.URL.Path),
		attribute.String("model", model),
		attribute.String("request_id", requestID),
	)
	var spanErr error
	defer func() {
		status := "completed"
		if spanErr != nil {
			status = "failed"
		}
		finishControlPlaneSpan(span, status, spanErr,
			attribute.String("path", r.URL.Path),
			attribute.String("model", model),
			attribute.String("request_id", requestID),
		)
	}()
	setCompatRequestHeaders(w.Header(), requestID, map[string]string{})
	broker := NewEnvModelBroker()
	setDeprecatedBackendHeaders(w.Header(), resolveModelExecutionRoute(model, strings.TrimSpace(broker.ollamaBaseURL) != ""))
	response, err := broker.Generate(ctx, ModelRequest{Model: model, Messages: input.Messages, Tools: input.Tools, ToolChoice: input.ToolChoice, Options: input.Options, RequestID: requestID, PreAttempt: func(decision BackendDecision) error {
		_, mergeErr := a.store.WithContext(ctx).MergeModelBrokerRequestCorrelationIDs(requestID, decision.CorrelationIDs())
		return mergeErr
	}})
	if err != nil {
		spanErr = err
		finish(ModelBrokerRequestFailed, `{}`, err.Error(), mergeStringMaps(response.CorrelationIDs, map[string]string{"llama_line_request_id": requestID}))
		w.Header().Set("X-OCQ-Status", "failed")
		writeError(w, http.StatusBadGateway, err.Error())
		return ModelResponse{}, false
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	response.CorrelationIDs["llama_line_request_id"] = requestID
	setCompatRequestHeaders(w.Header(), requestID, response.CorrelationIDs)
	w.Header().Set("X-OCQ-Status", "completed")
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "tool_calls": response.ToolCalls, "correlation_ids": response.CorrelationIDs})
	finish(ModelBrokerRequestCompleted, string(responseBody), "", response.CorrelationIDs)
	return response, true
}

func validateModelRequestSize(model string, messages []ModelMessage) error {
	maxPrompt := 64 << 10
	if parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SWITCHYARD_MAX_PROMPT_BYTES"))); err == nil && parsed >= 1024 && parsed <= 4<<20 {
		maxPrompt = parsed
	}
	if len(model) > 256 {
		return fmt.Errorf("model identifier exceeds 256 bytes")
	}
	total := 0
	for _, message := range messages {
		total += len(message.Role) + len(message.Content)
		if total > maxPrompt {
			return fmt.Errorf("prompt exceeds configured %d-byte limit", maxPrompt)
		}
	}
	return nil
}

func openAIChatCompletionBody(model string, response ModelResponse) map[string]any {
	return map[string]any{
		"id":      firstNonEmpty(response.CorrelationIDs["opencode_request_id"], response.CorrelationIDs["ocq_request_id"], response.CorrelationIDs["llama_line_request_id"], "chatcmpl-"+shortRandomID()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": openAIChatMessage(response), "finish_reason": openAIFinishReason(response)}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
}

func openAIChatMessage(response ModelResponse) map[string]any {
	message := map[string]any{"role": "assistant", "content": response.Content}
	if len(response.ToolCalls) > 0 {
		message["tool_calls"] = openAIToolCalls(response.ToolCalls)
		if strings.TrimSpace(response.Content) == "" {
			message["content"] = ""
		}
	}
	return message
}

func openAIFinishReason(response ModelResponse) string {
	if len(response.ToolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func openAIToolCalls(calls []any) []map[string]any {
	out := make([]map[string]any, 0, len(calls))
	for index, raw := range calls {
		call, _ := raw.(map[string]any)
		function, _ := call["function"].(map[string]any)
		name := firstNonEmpty(stringFromAny(function["name"]), stringFromAny(call["name"]))
		arguments := function["arguments"]
		if arguments == nil {
			arguments = call["arguments"]
		}
		if arguments == nil {
			arguments = call["parameters"]
		}
		argumentString := stringFromAny(arguments)
		if argumentString == "" && arguments != nil {
			encoded, _ := json.Marshal(arguments)
			argumentString = string(encoded)
		}
		id := firstNonEmpty(stringFromAny(call["id"]), fmt.Sprintf("call_%d", index))
		out = append(out, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": argumentString}})
	}
	return out
}

func stringFromAny(value any) string {
	if str, ok := value.(string); ok {
		return str
	}
	return ""
}

func openAIChatCompletionChunkBody(model string, requestID string, content string, done bool) map[string]any {
	choice := map[string]any{"index": 0, "delta": map[string]string{"content": content}, "finish_reason": nil}
	if done {
		choice["delta"] = map[string]string{}
		choice["finish_reason"] = "stop"
	}
	return map[string]any{"id": firstNonEmpty(requestID, "chatcmpl-"+shortRandomID()), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": []map[string]any{choice}}
}

func setCompatRequestHeaders(header http.Header, requestID string, correlation map[string]string) {
	requestID = firstNonEmpty(requestID, correlation["llama_line_request_id"], correlation["opencode_request_id"], correlation["ocq_request_id"])
	if requestID != "" {
		header.Set("X-Request-ID", requestID)
		header.Set("X-Llama-Line-Request-ID", requestID)
	}
	ocqRequestID := firstNonEmpty(correlation["ocq_request_id"], correlation["opencode_request_id"], requestID)
	if ocqRequestID != "" {
		header.Set("X-OCQ-Request-ID", ocqRequestID)
	}
	if sessionID := firstNonEmpty(correlation["ocq_session_id"], correlation["opencode_session_id"]); sessionID != "" {
		header.Set("X-OCQ-Session", sessionID)
	}
}

func setDeprecatedBackendHeaders(header http.Header, decision BackendDecision) {
	if decision.DeprecatedBackend == "" {
		return
	}
	header.Set("X-Switchyard-Backend", decision.DeprecatedBackend)
	header.Set("X-Switchyard-Compatibility-Warning", decision.CompatibilityWarning)
}

func writeCompatSSE(w http.ResponseWriter, event string, payload map[string]any) {
	encoded, _ := json.Marshal(payload)
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", encoded)
}

func flushCompatSSE(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (a *API) llamaLineCompatEmbeddings(w http.ResponseWriter, r *http.Request) {
	ollamaBaseURL := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	if strings.TrimSpace(ollamaBaseURL) == "" {
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": map[string]string{"message": "OLLAMA_BASE_URL is not configured; Switchyard cannot serve embeddings"}})
		return
	}
	var input struct {
		Model  string `json:"model"`
		Input  any    `json:"input"`
		Prompt any    `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	if input.Input == nil {
		input.Input = input.Prompt
	}
	ollamaBody, err := json.Marshal(map[string]any{"model": ollamaModelName(input.Model), "input": input.Input})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	response, ok := proxyOllamaJSON(w, r, ollamaBaseURL, "/api/embed", ollamaBody)
	if !ok {
		return
	}
	if r.URL.Path == "/v1/embeddings" {
		writeJSON(w, http.StatusOK, openAIEmbeddingsResponse(ollamaModelName(input.Model), response))
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) llamaLineCompatTags(w http.ResponseWriter, r *http.Request) {
	ollamaBaseURL := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	if strings.TrimSpace(ollamaBaseURL) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"models": []any{}})
		return
	}
	response, ok := proxyOllamaJSON(w, r, ollamaBaseURL, "/api/tags", nil)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) llamaLineCompatModels(w http.ResponseWriter, r *http.Request) {
	ollamaBaseURL := strings.TrimRight(os.Getenv("OLLAMA_BASE_URL"), "/")
	if strings.TrimSpace(ollamaBaseURL) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": []any{}})
		return
	}
	response, ok := proxyOllamaJSON(w, r, ollamaBaseURL, "/api/tags", nil)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, openAIModelsResponse(response))
}

func proxyOllamaJSON(w http.ResponseWriter, r *http.Request, baseURL string, path string, body []byte) (map[string]any, bool) {
	endpoint, err := url.JoinPath(baseURL, path)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, false
	}
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method = http.MethodPost
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, endpoint, reader)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, false
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		writeError(w, http.StatusBadGateway, "ollama request failed: "+resp.Status)
		return nil, false
	}
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return nil, false
	}
	return decoded, true
}

func openAIEmbeddingsResponse(model string, ollama map[string]any) map[string]any {
	items := []map[string]any{}
	if embeddings, ok := ollama["embeddings"].([]any); ok {
		for index, embedding := range embeddings {
			items = append(items, map[string]any{"object": "embedding", "embedding": embedding, "index": index})
		}
	} else if embedding, ok := ollama["embedding"]; ok {
		items = append(items, map[string]any{"object": "embedding", "embedding": embedding, "index": 0})
	}
	return map[string]any{"object": "list", "data": items, "model": model, "usage": map[string]int{"prompt_tokens": 0, "total_tokens": 0}}
}

func openAIModelsResponse(ollama map[string]any) map[string]any {
	items := []map[string]any{}
	if models, ok := ollama["models"].([]any); ok {
		for _, model := range models {
			if entry, ok := model.(map[string]any); ok {
				name, _ := entry["name"].(string)
				if name == "" {
					name, _ = entry["model"].(string)
				}
				if name != "" {
					items = append(items, map[string]any{"id": name, "object": "model", "owned_by": "ollama"})
				}
			}
		}
	}
	return map[string]any{"object": "list", "data": items}
}

func (a *API) llamaLineCompatQueue(w http.ResponseWriter, r *http.Request) {
	queued, inflight := a.compatBroker.snapshot(r.Context())
	if r.Method == http.MethodDelete {
		cancelled := a.compatBroker.cancelAll(r.Context(), "canceled")
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "cancelled": cancelled, "backend": "switchyard-native"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": queued, "queue_depth": len(queued), "inflight_depth": len(inflight), "max_depth": a.compatBroker.maxDepth, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatQueueItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.compatBroker.cancel(r.Context(), id) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "canceled", "id": id, "backend": "switchyard-native"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "id": id, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatInflight(w http.ResponseWriter, r *http.Request) {
	_, inflight := a.compatBroker.snapshot(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"inflight": inflight, "active_request": len(inflight) > 0, "inflight_depth": len(inflight), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatInflightCancel(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID        string `json:"id"`
		RequestID string `json:"request_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	id := firstNonEmpty(input.ID, input.RequestID, r.URL.Query().Get("id"), r.URL.Query().Get("request_id"))
	if id != "" {
		if a.compatBroker.cancel(r.Context(), id) {
			writeJSON(w, http.StatusAccepted, map[string]any{"status": "canceled", "id": id, "cancelled": 1, "backend": "switchyard-native"})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "id": id, "cancelled": 0, "backend": "switchyard-native"})
		return
	}
	cancelled := a.compatBroker.cancelAll(r.Context(), "canceled")
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "cancelled": cancelled, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	filter := ListModelBrokerRequestsFilter{Limit: limit + 1}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		created, id, decodeErr := decodeRunCursor(raw)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		filter.BeforeCreated = &created
		filter.BeforeID = id
	}
	requests, err := a.store.ListModelBrokerRequestsContext(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	nextCursor := ""
	if len(requests) > limit {
		requests = requests[:limit]
		last := requests[len(requests)-1]
		nextCursor = encodeRunCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests, "history": requests, "count": len(requests), "next_cursor": nextCursor, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatHistoryItem(w http.ResponseWriter, r *http.Request) {
	req, err := a.store.ModelBrokerRequestContext(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (a *API) llamaLineCompatRequestResult(w http.ResponseWriter, r *http.Request) {
	req, err := a.store.ModelBrokerRequestContext(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	switch req.Status {
	case ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested:
		writeJSON(w, http.StatusAccepted, map[string]any{"id": req.ID, "request_id": req.ID, "status": req.Status, "result_available": false, "history_url": "/api/history/" + req.ID, "backend": "switchyard-native"})
	case ModelBrokerRequestCanceled:
		writeJSON(w, http.StatusGone, map[string]any{"id": req.ID, "request_id": req.ID, "status": req.Status, "result_available": false, "error": req.Error, "backend": "switchyard-native"})
	case ModelBrokerRequestFailed:
		writeJSON(w, http.StatusBadGateway, map[string]any{"id": req.ID, "request_id": req.ID, "status": req.Status, "result_available": true, "error": req.Error, "backend": "switchyard-native"})
	default:
		var response map[string]any
		if strings.TrimSpace(req.ResponseBody) != "" {
			_ = json.Unmarshal([]byte(req.ResponseBody), &response)
		}
		if response == nil {
			response = map[string]any{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "request_id": req.ID, "status": req.Status, "result_available": true, "response": response, "backend": "switchyard-native"})
	}
}

func (a *API) llamaLineCompatSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	query := firstNonEmpty(r.URL.Query().Get("q"), r.URL.Query().Get("query"))
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Query: query, Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "results": requests, "count": len(requests), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	logs := make([]map[string]any, 0, len(requests))
	for _, req := range requests {
		message := req.Status
		if req.Error != "" {
			message += ": " + req.Error
		}
		logs = append(logs, map[string]any{"id": req.ID, "request_id": req.ID, "level": compatLogLevel(req.Status), "message": message, "model": req.Model, "path": req.Path, "client": req.Client, "priority": req.ClientPriority, "created_at": req.CreatedAt, "updated_at": req.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "count": len(logs), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	events := make([]map[string]any, 0, len(requests))
	for _, req := range requests {
		events = append(events, map[string]any{
			"id":              req.ID,
			"request_id":      req.ID,
			"action":          "model_broker." + req.Status,
			"status":          req.Status,
			"model":           req.Model,
			"path":            req.Path,
			"client":          req.Client,
			"priority":        req.ClientPriority,
			"correlation_ids": req.CorrelationIDs,
			"created_at":      req.CreatedAt,
			"updated_at":      req.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": events, "events": events, "count": len(events), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatStatsSummary(w http.ResponseWriter, r *http.Request) {
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: 1000})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byStatus := map[string]int{}
	byModel := map[string]int{}
	for _, req := range requests {
		byStatus[req.Status]++
		if strings.TrimSpace(req.Model) != "" {
			byModel[req.Model]++
		}
	}
	queued, inflight := a.compatBroker.snapshot(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"requests_total": len(requests), "by_status": byStatus, "by_model": byModel, "queue_depth": len(queued), "inflight_depth": len(inflight), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var input CreateModelBrokerFeedbackInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeJSONDecodeError(w, err)
			return
		}
		feedback, err := a.store.AddModelBrokerFeedback(input)
		if err != nil {
			writeControlError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "created", "feedback": feedback, "backend": "switchyard-native"})
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	feedback, err := a.store.ListModelBrokerFeedback(limit)
	if err != nil {
		writeControlError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": feedback, "count": len(feedback), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>Switchyard Model Broker</title></head><body><h1>Switchyard native model broker</h1><p>This compatibility page replaces the legacy llama-line UI.</p><ul><li><a href="/broker/status">Broker status</a></li><li><a href="/api/history">History</a></li><li><a href="/api/logs">Logs</a></li><li><a href="/api/config">Config</a></li></ul></body></html>`))
}

func (a *API) llamaLineCompatConfig(w http.ResponseWriter, r *http.Request) {
	broker := resolveModelBrokerBackend("", strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) != "")
	executor := resolveOpenCodeExecutorBackend()
	configuredBrokerSelector := strings.TrimSpace(os.Getenv("MODEL_BROKER_BACKEND"))
	configuredExecutorSelector := strings.TrimSpace(os.Getenv("OPENCODE_EXECUTOR_BACKEND"))
	brokerSelector := effectiveModelBrokerSelector(broker.ConfiguredSelector)
	executorSelector := effectiveOpenCodeExecutorSelector(executor.ConfiguredSelector)
	writeJSON(w, http.StatusOK, map[string]any{
		"backend":                               "switchyard-native",
		"llama_line_port":                       firstNonEmpty(os.Getenv("LLAMA_LINE_PORT"), "11434"),
		"ollama_base_url_configured":            strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) != "",
		"opencode_server_url":                   firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"),
		"model_broker_timeout":                  modelBrokerTimeout().String(),
		"ollama_model_prefixes":                 strings.Split(firstNonEmpty(os.Getenv("MODEL_BROKER_OLLAMA_MODELS"), "llama,qwen,mistral,codellama,nomic-embed,bge,deepseek,gemma,phi,hermes"), ","),
		"max_depth":                             a.compatBroker.maxDepth,
		"durable_history":                       true,
		"model_broker_selector":                 brokerSelector,
		"opencode_executor_selector":            executorSelector,
		"model_broker_configured_selector":      configuredBrokerSelector,
		"opencode_executor_configured_selector": configuredExecutorSelector,
		"compatibility_warning":                 firstNonEmpty(broker.CompatibilityWarning, executor.CompatibilityWarning),
	})
}

func compatLogLevel(status string) string {
	switch status {
	case ModelBrokerRequestFailed:
		return "error"
	case ModelBrokerRequestCanceled, ModelBrokerRequestCancelRequested:
		return "warn"
	default:
		return "info"
	}
}

func (a *API) llamaLineCompatMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	metrics, err := a.store.RuntimeMetrics()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "model broker metrics unavailable")
		return
	}
	queued, inflight := a.compatBroker.snapshot(r.Context())
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintln(w, "# HELP switchyard_model_broker_records Durable model broker requests currently retained by status.")
	_, _ = fmt.Fprintln(w, "# TYPE switchyard_model_broker_records gauge")
	for _, status := range []string{ModelBrokerRequestQueued, ModelBrokerRequestRunning, ModelBrokerRequestCancelRequested, ModelBrokerRequestCanceled, ModelBrokerRequestCompleted, ModelBrokerRequestFailed} {
		_, _ = fmt.Fprintf(w, "switchyard_model_broker_records{status=%q} %d\n", status, metrics.ModelRequestStatusCounts[status])
	}
	_, _ = fmt.Fprintln(w, "# TYPE switchyard_workflow_runs gauge")
	for _, status := range []RunStatus{RunQueued, RunRunning, RunSucceeded, RunFailed, RunCanceled, RunPaused, RunTimedOut} {
		_, _ = fmt.Fprintf(w, "switchyard_workflow_runs{status=%q} %d\n", status, metrics.RunStatusCounts[string(status)])
	}
	_, _ = fmt.Fprintln(w, "# TYPE switchyard_model_broker_latency_milliseconds summary")
	_, _ = fmt.Fprintf(w, "switchyard_model_broker_latency_milliseconds_count %d\n", metrics.ModelLatencyCount)
	_, _ = fmt.Fprintf(w, "switchyard_model_broker_latency_milliseconds_sum %.3f\n", metrics.ModelLatencyMilliseconds)
	_, _ = fmt.Fprintln(w, "# TYPE switchyard_model_broker_queue_depth gauge")
	_, _ = fmt.Fprintf(w, "switchyard_model_broker_queue_depth %d\n", len(queued))
	_, _ = fmt.Fprintln(w, "# TYPE switchyard_model_broker_inflight gauge")
	_, _ = fmt.Fprintf(w, "switchyard_model_broker_inflight %d\n", len(inflight))
	if a.workerPool != nil {
		status := a.workerPool.Status()
		_, _ = fmt.Fprintln(w, "# TYPE switchyard_worker_active gauge")
		_, _ = fmt.Fprintf(w, "switchyard_worker_active %d\n", status.Active)
		_, _ = fmt.Fprintln(w, "# TYPE switchyard_worker_completed_runs_total counter")
		_, _ = fmt.Fprintf(w, "switchyard_worker_completed_runs_total %d\n", status.CompletedRuns)
		_, _ = fmt.Fprintln(w, "# TYPE switchyard_worker_failed_runs_total counter")
		_, _ = fmt.Fprintf(w, "switchyard_worker_failed_runs_total %d\n", status.FailedRuns)
		_, _ = fmt.Fprintln(w, "# TYPE switchyard_worker_lease_renewals_total counter")
		_, _ = fmt.Fprintf(w, "switchyard_worker_lease_renewals_total %d\n", status.LeaseRenewals)
	}
	if timestamp, ok := backupSuccessTimestamp(os.Getenv("SWITCHYARD_BACKUP_METRICS_FILE")); ok {
		_, _ = fmt.Fprintln(w, "# HELP switchyard_postgres_backup_last_success_timestamp_seconds Unix timestamp of the newest successful PostgreSQL backup.")
		_, _ = fmt.Fprintln(w, "# TYPE switchyard_postgres_backup_last_success_timestamp_seconds gauge")
		_, _ = fmt.Fprintf(w, "switchyard_postgres_backup_last_success_timestamp_seconds %.0f\n", timestamp)
	}
	if restoreMetrics, ok := restoreDrillMetrics("/var/lib/switchyard-metrics/restore-drill.prom"); ok {
		for _, name := range restoreDrillMetricNames {
			if value, exists := restoreMetrics[name]; exists {
				_, _ = fmt.Fprintf(w, "%s %.0f\n", name, value)
			}
		}
	}
}

var restoreDrillMetricNames = []string{
	"switchyard_restore_drill_last_attempt_timestamp_seconds",
	"switchyard_restore_drill_last_result",
	"switchyard_restore_drill_last_failure_timestamp_seconds",
	"switchyard_restore_drill_last_success_timestamp_seconds",
	"switchyard_restore_drill_next_deadline_timestamp_seconds",
}

func restoreDrillMetrics(path string) (map[string]float64, bool) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, false
	}
	allowed := make(map[string]struct{}, len(restoreDrillMetricNames))
	for _, name := range restoreDrillMetricNames {
		allowed[name] = struct{}{}
	}
	values := make(map[string]float64)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, false
		}
		if _, exists := allowed[fields[0]]; !exists {
			return nil, false
		}
		if _, duplicate := values[fields[0]]; duplicate {
			return nil, false
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || fields[0] == "switchyard_restore_drill_last_result" && value != 0 && value != 1 {
			return nil, false
		}
		values[fields[0]] = value
	}
	return values, scanner.Err() == nil && len(values) > 0
}

func backupSuccessTimestamp(path string) (float64, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, false
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "switchyard_postgres_backup_last_success_timestamp_seconds" {
			continue
		}
		timestamp, err := strconv.ParseFloat(fields[1], 64)
		if err == nil && timestamp > 0 {
			return timestamp, true
		}
		return 0, false
	}
	return 0, false
}
