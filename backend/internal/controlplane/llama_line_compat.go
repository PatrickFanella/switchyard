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
	response, ok := a.llamaLineCompatGenerateModel(w, r, input.Model, input.Messages)
	if !ok {
		return
	}
	body := map[string]any{
		"id":      firstNonEmpty(response.CorrelationIDs["opencode_request_id"], response.CorrelationIDs["llama_line_request_id"], "chatcmpl-"+shortRandomID()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   input.Model,
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": response.Content}, "finish_reason": "stop"}},
		"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	}
	if input.Stream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		payload, _ := json.Marshal(body)
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
		return
	}
	writeJSON(w, http.StatusOK, body)
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
	requestID, ctx, finish := a.compatBroker.begin(r, model)
	w.Header().Set("X-Llama-Line-Request-ID", requestID)
	broker := NewEnvModelBroker()
	response, err := broker.Generate(ctx, ModelRequest{Model: model, Messages: messages})
	if err != nil {
		finish("failed")
		writeError(w, http.StatusBadGateway, err.Error())
		return ModelResponse{}, false
	}
	finish("completed")
	if response.CorrelationIDs == nil {
		response.CorrelationIDs = map[string]string{}
	}
	response.CorrelationIDs["llama_line_request_id"] = requestID
	return response, true
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

func (a *API) llamaLineCompatMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("# HELP switchyard_llama_line_compat_requests Native llama-line compatibility surface.\n# TYPE switchyard_llama_line_compat_requests counter\nswitchyard_llama_line_compat_requests 1\n"))
}
