package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type llamaLineCompatChatRequest struct {
	Model    string         `json:"model"`
	Messages []ModelMessage `json:"messages"`
	Prompt   string         `json:"prompt"`
	Stream   bool           `json:"stream"`
}

func (a *API) llamaLineCompatStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status := a.modelBroker.Status(ctx)
	queued, inflight := a.compatBroker.snapshot()
	inFlightIDs := a.compatBroker.inFlightIDs()
	if len(inFlightIDs) > 0 {
		status.InFlightRequestIDs = inFlightIDs
		status.ActiveRequest = true
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":            status.Status,
		"backend":           "switchyard-native",
		"queue_depth":       len(queued),
		"max_depth":         status.MaxDepth,
		"active_request":    len(inflight) > 0 || status.ActiveRequest,
		"connected_clients": status.ConnectedClients,
		"in_flight_ids":     status.InFlightRequestIDs,
		"queue":             queued,
		"inflight":          inflight,
		"upstreams":         status.Upstreams,
		"checked_at":        status.CheckedAt,
		"error":             status.Error,
	})
}

func (a *API) llamaLineCompatChatCompletions(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(input.Messages) == 0 && strings.TrimSpace(input.Prompt) != "" {
		input.Messages = []ModelMessage{{Role: "user", Content: input.Prompt}}
	}
	if input.Stream {
		a.llamaLineCompatStreamChatCompletions(w, r, input)
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input.Model, input.Messages)
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
	setCompatRequestHeaders(w.Header(), requestID, map[string]string{})
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-OCQ-Status", "running")
	w.Header().Set("X-OCQ-Streaming-Mode", "buffered")
	w.WriteHeader(http.StatusOK)
	writeCompatSSE(w, "status", map[string]any{"status": "queued", "request_id": requestID, "backend": "switchyard-native"})
	writeCompatSSE(w, "status", map[string]any{"status": "running", "request_id": requestID, "backend": "switchyard-native"})
	flushCompatSSE(w)

	broker := NewEnvModelBroker()
	response, err := broker.Generate(ctx, ModelRequest{Model: model, Messages: input.Messages, RequestID: requestID})
	if err != nil {
		finish(ModelBrokerRequestFailed, `{}`, err.Error(), map[string]string{"llama_line_request_id": requestID})
		writeCompatSSE(w, "error", map[string]any{"status": "failed", "request_id": requestID, "error": err.Error(), "backend": "switchyard-native"})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	response.CorrelationIDs["llama_line_request_id"] = requestID
	setCompatRequestHeaders(w.Header(), requestID, response.CorrelationIDs)
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": response.CorrelationIDs})
	finish(ModelBrokerRequestCompleted, string(responseBody), "", response.CorrelationIDs)
	writeCompatSSE(w, "status", map[string]any{"status": "completed", "request_id": requestID, "backend": "switchyard-native"})
	payload, _ := json.Marshal(openAIChatCompletionBody(model, response))
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
}

func (a *API) llamaLineCompatCompletions(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input.Model, []ModelMessage{{Role: "user", Content: input.Prompt}})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": "cmpl-" + shortRandomID(), "object": "text_completion", "created": time.Now().Unix(), "model": input.Model, "choices": []map[string]any{{"index": 0, "text": response.Content, "finish_reason": "stop"}}})
}

func (a *API) llamaLineCompatChat(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input.Model, input.Messages)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": input.Model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "message": map[string]string{"role": "assistant", "content": response.Content}, "done": true})
}

func (a *API) llamaLineCompatGenerate(w http.ResponseWriter, r *http.Request) {
	var input llamaLineCompatChatRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	response, ok := a.llamaLineCompatGenerateModel(w, r, input.Model, []ModelMessage{{Role: "user", Content: input.Prompt}})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": input.Model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "response": response.Content, "done": true})
}

func (a *API) llamaLineCompatGenerateModel(w http.ResponseWriter, r *http.Request, model string, messages []ModelMessage) (ModelResponse, bool) {
	if strings.TrimSpace(model) == "" {
		model = firstNonEmpty(r.URL.Query().Get("model"), "gpt-5.4")
	}
	requestBody, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "path": r.URL.Path})
	requestID, ctx, finish := a.compatBroker.begin(r, model, string(requestBody))
	setCompatRequestHeaders(w.Header(), requestID, map[string]string{})
	broker := NewEnvModelBroker()
	response, err := broker.Generate(ctx, ModelRequest{Model: model, Messages: messages, RequestID: requestID})
	if err != nil {
		finish(ModelBrokerRequestFailed, `{}`, err.Error(), map[string]string{"llama_line_request_id": requestID})
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
	responseBody, _ := json.Marshal(map[string]any{"content": response.Content, "correlation_ids": response.CorrelationIDs})
	finish(ModelBrokerRequestCompleted, string(responseBody), "", response.CorrelationIDs)
	return response, true
}

func openAIChatCompletionBody(model string, response ModelResponse) map[string]any {
	return map[string]any{
		"id":      firstNonEmpty(response.CorrelationIDs["opencode_request_id"], response.CorrelationIDs["ocq_request_id"], response.CorrelationIDs["llama_line_request_id"], "chatcmpl-"+shortRandomID()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": response.Content}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
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
		writeError(w, http.StatusBadRequest, "invalid JSON body")
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
	queued, inflight := a.compatBroker.snapshot()
	if r.Method == http.MethodDelete {
		cancelled := a.compatBroker.cancelAll("canceled")
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "cancelled": cancelled, "backend": "switchyard-native"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": queued, "queue_depth": len(queued), "inflight_depth": len(inflight), "max_depth": a.compatBroker.maxDepth, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatQueueItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.compatBroker.cancel(id) {
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "canceled", "id": id, "backend": "switchyard-native"})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "id": id, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatInflight(w http.ResponseWriter, r *http.Request) {
	_, inflight := a.compatBroker.snapshot()
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
		if a.compatBroker.cancel(id) {
			writeJSON(w, http.StatusAccepted, map[string]any{"status": "canceled", "id": id, "cancelled": 1, "backend": "switchyard-native"})
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"status": "not_found", "id": id, "cancelled": 0, "backend": "switchyard-native"})
		return
	}
	cancelled := a.compatBroker.cancelAll("canceled")
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "cancelled": cancelled, "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatHistory(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	requests, err := a.store.ListModelBrokerRequests(ListModelBrokerRequestsFilter{Limit: limit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests, "history": requests, "count": len(requests), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatHistoryItem(w http.ResponseWriter, r *http.Request) {
	req, err := a.store.ModelBrokerRequest(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "request not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
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
		logs = append(logs, map[string]any{"id": req.ID, "request_id": req.ID, "level": compatLogLevel(req.Status), "message": message, "model": req.Model, "path": req.Path, "created_at": req.CreatedAt, "updated_at": req.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "count": len(logs), "backend": "switchyard-native"})
}

func (a *API) llamaLineCompatConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"backend":                    "switchyard-native",
		"llama_line_port":            firstNonEmpty(os.Getenv("LLAMA_LINE_PORT"), "11434"),
		"ollama_base_url_configured": strings.TrimSpace(os.Getenv("OLLAMA_BASE_URL")) != "",
		"opencode_server_url":        firstNonEmpty(os.Getenv("OPENCODE_SERVER_URL"), "http://opencode:4096"),
		"model_broker_timeout":       modelBrokerTimeout().String(),
		"ollama_model_prefixes":      strings.Split(firstNonEmpty(os.Getenv("MODEL_BROKER_OLLAMA_MODELS"), "llama,qwen,mistral,codellama,nomic-embed,bge,deepseek,gemma,phi"), ","),
		"max_depth":                  a.compatBroker.maxDepth,
		"durable_history":            true,
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
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# HELP switchyard_llama_line_compat_requests Native llama-line compatibility surface.\n# TYPE switchyard_llama_line_compat_requests counter\nswitchyard_llama_line_compat_requests 1\n"))
}
