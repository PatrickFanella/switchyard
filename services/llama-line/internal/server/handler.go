package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/dedup"
	"git.subcult.tv/PatrickFanella/llama-line/internal/persistence"
	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
	"git.subcult.tv/PatrickFanella/llama-line/internal/queue"
	"git.subcult.tv/PatrickFanella/llama-line/internal/ui"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// extractModel peeks the request body to extract the "model" field without consuming it.
// Returns "" if the field is absent or the body is not JSON.
func extractModel(r *http.Request) (model string, bodyBytes []byte) {
	if r.Body == nil {
		return "", nil
	}
	bodyBytes, err := io.ReadAll(r.Body)
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	if err != nil || len(bodyBytes) == 0 {
		return "", bodyBytes
	}
	var v struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(bodyBytes, &v) == nil {
		return v.Model, bodyBytes
	}
	return "", bodyBytes
}

// isStreamingRequest returns true when the request body contains "stream":true.
func isStreamingRequest(bodyBytes []byte) bool {
	if len(bodyBytes) == 0 {
		return false
	}
	var v struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(bodyBytes, &v); err != nil {
		return false
	}
	return v.Stream != nil && *v.Stream
}

func isOCQSessionRequest(model, upstreamName string) bool {
	model = strings.ToLower(model)
	upstreamName = strings.ToLower(upstreamName)
	return strings.HasPrefix(model, "opencode") || strings.Contains(upstreamName, "ocq")
}

func rewriteOCQRequestPath(r *http.Request) *http.Request {
	if r == nil || r.URL == nil || r.URL.Path != "/api/chat" {
		return r
	}
	clone := r.Clone(r.Context())
	urlClone := *r.URL
	urlClone.Path = "/v1/chat/completions"
	clone.URL = &urlClone
	return clone
}

func injectOCQSession(bodyBytes []byte, sessionID string) []byte {
	if sessionID == "" || len(bodyBytes) == 0 {
		return bodyBytes
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &obj); err != nil {
		return bodyBytes
	}
	sessionBytes, err := json.Marshal(sessionID)
	if err != nil {
		return bodyBytes
	}
	obj["ocq_session_id"] = sessionBytes
	out, err := json.Marshal(obj)
	if err != nil {
		return bodyBytes
	}
	return out
}

// HandleInference enqueues the request, streams SSE status updates while waiting,
// then pipes the ollama response when dequeued.
//
// Dedup / cache short-circuit paths:
//  1. If the client has caching enabled and the request hash is in the cache → serve from cache.
//  2. If the client has deduplication enabled and an identical request is already in-flight →
//     subscribe to its result channel instead of enqueuing.
//  3. Otherwise proceed with normal queue path; on success, store in cache and publish to
//     dedup subscribers.
func (s *Server) HandleInference(w http.ResponseWriter, r *http.Request) {
	clientName := clientNameFromCtx(r.Context())
	start := time.Now()

	// Extract model and reset body.
	model, bodyBytes := extractModel(r)
	if bodyBytes != nil {
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	upstreamIdx := s.router.Resolve(model)
	entry := &s.upstreamEntries[upstreamIdx]
	sessionful := isOCQSessionRequest(model, entry.cfg.Name)

	// Compute request hash for dedup/cache (only for non-streaming requests).
	streaming := isStreamingRequest(bodyBytes)
	var reqHash string
	var hashOK bool
	if !sessionful && !streaming {
		reqHash, hashOK = dedup.HashRequest(bodyBytes)
	}

	// ── Cache check ──────────────────────────────────────────────────────────
	if !sessionful && hashOK && !streaming {
		cacheTTL := clientCacheTTLFromCtx(r.Context())
		cacheMax := clientCacheMaxFromCtx(r.Context())
		if c := s.clientCache(clientName, cacheTTL, cacheMax); c != nil {
			if cached, ok := c.Get(reqHash); ok {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Llama-Line-Cache", "HIT")
				w.WriteHeader(http.StatusOK)
				w.Write(cached)
				elapsed := time.Since(start)
				s.log.Info("request completed",
					"status", "cache_hit",
					"client", clientName,
					"model", model,
					"path", r.URL.Path,
					"duration_ms", elapsed.Milliseconds(),
				)
				s.writeAuditLog(clientName, "", model, r.URL.Path, bodyBytes, cached, "cache_hit", elapsed, 0, 0, "cache", false)
				return
			}
		}
	}

	// ── Dedup check (subscribe to existing in-flight) ─────────────────────
	if !sessionful && hashOK && clientDeduplicateFromCtx(r.Context()) {
		if waiter, ok := s.deduplicator.Subscribe(reqHash); ok {
			// Another request with the same hash is already in-flight.
			// Wait for its result.
			SetSSEHeaders(w)
			select {
			case <-r.Context().Done():
				return
			case result := <-waiter.ResponseCh:
				elapsed := time.Since(start)
				if result.Err != nil {
					WriteStatusEvent(w, queue.StatusUpdate{Status: "ollama_unavailable", Message: result.Err.Error()})
					s.log.Info("request completed",
						"status", "dedup_error",
						"client", clientName,
						"model", model,
						"duration_ms", elapsed.Milliseconds(),
						"err", result.Err,
					)
					s.writeAuditLog(clientName, newUUID(), model, r.URL.Path, bodyBytes, nil, "dedup_error", elapsed, 0, 0, "", streaming)
					return
				}
				if result.Cancel != nil {
					defer result.Cancel()
				}
				copyOCQResponseHeaders(w, result.Resp)
				var respBodyBytes []byte
				if !streaming && result.Resp != nil && result.Resp.Body != nil {
					var readErr error
					respBodyBytes, readErr = io.ReadAll(result.Resp.Body)
					result.Resp.Body.Close()
					if readErr == nil {
						respBodyBytes = injectOCQSession(respBodyBytes, result.Resp.Header.Get("x-ocq-session"))
						result.Resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))
					} else {
						result.Resp.Body = io.NopCloser(bytes.NewReader(nil))
					}
				}
				defer result.Resp.Body.Close()
				proxy.PipeResponse(w, result.Resp, true)
				s.log.Info("request completed",
					"status", "dedup_hit",
					"client", clientName,
					"model", model,
					"duration_ms", elapsed.Milliseconds(),
				)
				s.writeAuditLog(clientName, newUUID(), model, r.URL.Path, bodyBytes, respBodyBytes, "dedup_hit", elapsed, 0, 0, "", streaming)
				return
			}
		}
	}

	// ── Normal queue path ─────────────────────────────────────────────────
	snap := entry.queue.Snapshot()
	if snap.Depth >= snap.MaxDepth {
		writeError(w, http.StatusServiceUnavailable, "queue full", snap.Depth, snap.MaxDepth)
		s.log.Warn("request rejected",
			"status", "queue_full",
			"client", clientName,
			"model", model,
			"queue_depth", snap.Depth,
			"max_depth", snap.MaxDepth,
		)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	req := &queue.Request{
		ID:         newUUID(),
		ClientName: clientName,
		Priority:   clientPriorityFromCtx(r.Context()),
		EnqueuedAt: time.Now(),
		Ctx:        ctx,
		Cancel:     cancel,
		ResponseCh: make(chan queue.Result, 1),
		StatusCh:   make(chan queue.StatusUpdate, 8),
		HTTPReq:    r,
	}
	if sessionful {
		req.HTTPReq = rewriteOCQRequestPath(r)
	}

	if err := entry.queue.Enqueue(req); err != nil {
		writeError(w, http.StatusServiceUnavailable, "queue full", snap.Depth, snap.MaxDepth)
		s.log.Warn("request rejected",
			"status", "queue_full",
			"client", clientName,
			"id", req.ID,
			"model", model,
		)
		if s.met != nil {
			s.met.RequestsTotal.WithLabelValues(clientName, "queue_full").Inc()
		}
		if s.alerter != nil {
			s.alerter.AlertQueueFull(snap.Depth, snap.MaxDepth, s.cfg.AlertQueueFullThreshold)
		}
		return
	}
	defer entry.queue.Drop(req.ID)
	w.Header().Set("X-Llama-Line-Request-ID", req.ID)

	// Register in deduplicator before we start waiting (so concurrent arrivals
	// can subscribe while this request is in the queue / in-flight).
	if !sessionful && hashOK && clientDeduplicateFromCtx(r.Context()) {
		s.deduplicator.Register(reqHash)
	}

	s.connectedClients.Add(1)
	defer s.connectedClients.Add(-1)
	if s.met != nil {
		s.met.ConnectedClients.Inc()
		defer s.met.ConnectedClients.Dec()
	}

	s.log.Info("request queued",
		"client", clientName,
		"id", req.ID,
		"model", model,
		"path", r.URL.Path,
		"upstream", entry.cfg.Name,
	)

	SetSSEHeaders(w)
	heartbeat := time.NewTicker(time.Duration(s.cfg.HeartbeatIntervalSecs) * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			s.log.Info("client disconnected while queued", "client", clientName, "id", req.ID)
			if !sessionful && hashOK && clientDeduplicateFromCtx(r.Context()) {
				s.deduplicator.Publish(reqHash, queue.Result{Err: r.Context().Err()}, nil)
			}
			return

		case update := <-req.StatusCh:
			update.RequestID = req.ID
			if err := WriteStatusEvent(w, update); err != nil {
				return
			}
			if update.Status == "dropped_by_admin" {
				s.log.Info("request completed",
					"status", "dropped_by_admin",
					"client", clientName,
					"id", req.ID,
					"model", model,
					"duration_ms", time.Since(req.EnqueuedAt).Milliseconds(),
				)
				return
			}

		case <-heartbeat.C:
			pos := entry.queue.Positions()[req.ID]
			elapsed := int(time.Since(req.EnqueuedAt).Seconds())
			WriteStatusEvent(w, queue.StatusUpdate{RequestID: req.ID, Position: pos, WaitSeconds: elapsed, Status: "queued"})

		case result := <-req.ResponseCh:
			elapsed := time.Since(req.EnqueuedAt)
			var upstream, queueWait time.Duration
			if !req.StartedAt.IsZero() {
				upstream = time.Since(req.StartedAt)
				queueWait = req.StartedAt.Sub(req.EnqueuedAt)
			} else {
				queueWait = elapsed
			}
			if result.Err != nil {
				WriteStatusEvent(w, queue.StatusUpdate{RequestID: req.ID, Status: "ollama_unavailable", Message: result.Err.Error()})
				s.log.Info("request completed",
					"status", "error",
					"client", clientName,
					"id", req.ID,
					"model", model,
					"duration_ms", elapsed.Milliseconds(),
					"queue_ms", queueWait.Milliseconds(),
					"upstream_ms", upstream.Milliseconds(),
					"err", result.Err,
				)
				if s.met != nil {
					s.met.RequestsTotal.WithLabelValues(clientName, "error").Inc()
				}
				if !sessionful && hashOK && clientDeduplicateFromCtx(r.Context()) {
					s.deduplicator.Publish(reqHash, result, nil)
				}
				s.recordHistory(req, "error", elapsed, queueWait, upstream, model)
				s.writeAuditLog(clientName, req.ID, model, r.URL.Path, bodyBytes, nil, "error", elapsed, queueWait, upstream, entry.cfg.Name, streaming)
				return
			}

			// Read non-streaming bodies so audit/full-message views work for every
			// inference shape, not just hashable chat requests. Hashable requests also
			// use the captured body for cache and dedup fan-out.
			var respBodyBytes []byte
			if !streaming {
				var readErr error
				respBodyBytes, readErr = io.ReadAll(result.Resp.Body)
				result.Resp.Body.Close()
				if readErr == nil {
					respBodyBytes = injectOCQSession(respBodyBytes, result.Resp.Header.Get("x-ocq-session"))
					// Restore the body for PipeResponse.
					result.Resp.Body = io.NopCloser(bytes.NewReader(respBodyBytes))

					// Store in cache.
					if !sessionful && hashOK {
						cacheTTL := clientCacheTTLFromCtx(r.Context())
						cacheMax := clientCacheMaxFromCtx(r.Context())
						if c := s.clientCache(clientName, cacheTTL, cacheMax); c != nil {
							c.Set(reqHash, respBodyBytes)
						}
					}
				} else {
					result.Resp.Body = io.NopCloser(bytes.NewReader(nil))
				}
			}

			// Fan out to dedup waiters.
			if !sessionful && hashOK && clientDeduplicateFromCtx(r.Context()) {
				s.deduplicator.Publish(reqHash, result, respBodyBytes)
			}

			if result.Cancel != nil {
				defer result.Cancel()
			}
			copyOCQResponseHeaders(w, result.Resp)
			defer result.Resp.Body.Close()
			proxy.PipeResponse(w, result.Resp, true) // SSE headers already committed
			s.log.Info("request completed",
				"status", "success",
				"client", clientName,
				"id", req.ID,
				"model", model,
				"duration_ms", elapsed.Milliseconds(),
				"queue_ms", queueWait.Milliseconds(),
				"upstream_ms", upstream.Milliseconds(),
				"streaming", streaming,
			)
			if s.met != nil {
				s.met.RequestsTotal.WithLabelValues(clientName, "success").Inc()
			}
			s.writeAuditLog(clientName, req.ID, model, r.URL.Path, bodyBytes, respBodyBytes, "success", elapsed, queueWait, upstream, entry.cfg.Name, streaming)
			s.recordHistory(req, "success", elapsed, queueWait, upstream, model)
			return
		}
	}
}

// HandlePassThrough forwards non-inference requests to ollama immediately.
// It uses the primary upstream forwarder (index 0) since pass-through requests
// (e.g. /api/tags, /api/show) are not model-specific.
func (s *Server) HandlePassThrough(w http.ResponseWriter, r *http.Request) {
	clientName := clientNameFromCtx(r.Context())
	start := time.Now()
	req := &queue.Request{
		ID:         newUUID(),
		ClientName: clientName,
		Ctx:        r.Context(),
		HTTPReq:    r,
		StatusCh:   make(chan queue.StatusUpdate, 1),
	}
	result := s.forwarder.Forward(r.Context(), req)
	elapsed := time.Since(start)
	if result.Err != nil {
		writeError(w, http.StatusBadGateway, result.Err.Error(), 0, 0)
		s.log.Info("passthrough completed",
			"status", "error",
			"client", clientName,
			"path", r.URL.Path,
			"duration_ms", elapsed.Milliseconds(),
			"err", result.Err,
		)
		return
	}
	defer result.Resp.Body.Close()
	proxy.PipeResponse(w, result.Resp, false) // no prior headers
	s.log.Info("passthrough completed",
		"status", "success",
		"client", clientName,
		"path", r.URL.Path,
		"duration_ms", elapsed.Milliseconds(),
	)
}

func copyOCQResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	if resp == nil {
		return
	}
	for _, header := range []string{"x-ocq-session", "x-ocq-request-id", "x-ocq-status", "x-ocq-streaming-mode"} {
		if v := resp.Header.Get(header); v != "" {
			w.Header().Set(header, v)
		}
	}
}

// writeAuditLog writes a single audit log entry. It is a no-op when auditLog is nil
// or when respBody is nil (streaming responses are not captured).
//
// total is wall-clock from request arrival; queueWait is enqueue→dispatch;
// upstream is the time spent forwarding to ollama. queueWait and upstream may
// be zero for short-circuit paths (cache_hit/dedup_hit).
func (s *Server) writeAuditLog(client, id, model, path string, reqBody, respBody []byte, status string, total, queueWait, upstream time.Duration, upstreamName string, streaming bool) {
	// Do not audit streaming responses — the body is piped directly and not buffered.
	skipAuditLog := respBody == nil && status == "success"
	attrs := s.auditAttrs(client, id, model, path, reqBody, respBody, status, total, queueWait, upstream)
	if s.tracerResolver != nil && s.tracerResolver.TracerForClient(client) != nil {
		s.emitPhoenixSpans(client, id, model, path, status, total, queueWait, upstream, upstreamName, reqBody, respBody, attrs)
	}
	s.persistRequestEvent(client, id, model, path, reqBody, respBody, status, total, queueWait, upstream, upstreamName, streaming, attrs)
	if s.auditLog == nil || skipAuditLog {
		return
	}
	s.auditLog.Info("request", attrs...)
}

func (s *Server) emitPhoenixSpans(client, id, model, path, status string, total, queueWait, upstream time.Duration, upstreamName string, reqBody, respBody []byte, attrs []any) {
	tr := s.tracerResolver.TracerForClient(client)
	if tr == nil {
		return
	}
	ctx, span := tr.Start(context.Background(), "llama-line.request",
		trace.WithAttributes(
			attribute.String("openinference.span.kind", "LLM"),
			attribute.String("llm.model_name", model),
			attribute.String("llama_line.client", client),
			attribute.String("llama_line.path", path),
			attribute.String("llama_line.status", status),
			attribute.Int64("llm.duration_ms", total.Milliseconds()),
			attribute.Int64("llm.token_count.prompt", int64(auditInt(attrs, "prompt_tokens"))),
			attribute.Int64("llm.token_count.completion", int64(auditInt(attrs, "completion_tokens"))),
			attribute.Int64("llm.token_count.total", int64(auditInt(attrs, "total_tokens"))),
		),
	)
	addMessageAttributes(span, reqBody, respBody)

	if queueWait > 0 {
		_, child := tr.Start(ctx, "llm.queue_wait",
			trace.WithAttributes(attribute.String("openinference.span.kind", "LLM"), attribute.Int64("llm.queue_wait_ms", queueWait.Milliseconds())))
		child.End()
	}
	if upstream > 0 {
		_, child := tr.Start(ctx, "llm.upstream_call",
			trace.WithAttributes(
				attribute.String("openinference.span.kind", "LLM"),
				attribute.String("llama_line.upstream", upstreamName),
				attribute.Int64("llm.upstream_ms", upstream.Milliseconds()),
			))
		child.End()
	}

	toolNames, toolErrors := auditStringSlices(attrs)
	for i, toolName := range toolNames {
		_, toolSpan := tr.Start(ctx, "tool.call",
			trace.WithAttributes(
				attribute.String("openinference.span.kind", "TOOL"),
				attribute.String("tool.name", toolName),
			),
		)
		if i < len(toolErrors) && toolErrors[i] != "" {
			toolSpan.SetAttributes(attribute.String("tool.error", toolErrors[i]))
		}
		toolSpan.End()
	}
	span.End()
}

func addMessageAttributes(span trace.Span, reqBody, respBody []byte) {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(reqBody, &req) == nil {
		for i, msg := range req.Messages {
			if msg.Role == "" && msg.Content == "" {
				continue
			}
			span.SetAttributes(
				attribute.String("llm.input_messages."+strconv.Itoa(i)+".message.role", msg.Role),
				attribute.String("llm.input_messages."+strconv.Itoa(i)+".message.content", msg.Content),
			)
		}
	}
	var resp struct {
		Message *struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Response string `json:"response"`
	}
	if json.Unmarshal(respBody, &resp) == nil {
		if resp.Message != nil {
			span.SetAttributes(
				attribute.String("llm.output_messages.0.message.role", resp.Message.Role),
				attribute.String("llm.output_messages.0.message.content", resp.Message.Content),
			)
		} else if resp.Response != "" {
			span.SetAttributes(
				attribute.String("llm.output_messages.0.message.role", "assistant"),
				attribute.String("llm.output_messages.0.message.content", resp.Response),
			)
		}
	}
}

func (s *Server) auditAttrs(client, id, model, path string, reqBody, respBody []byte, status string, total, queueWait, upstream time.Duration) []any {
	attrs := []any{
		"client", client,
		"id", id,
		"model", model,
		"path", path,
		"status", status,
		"duration_ms", total.Milliseconds(),
		"queue_ms", queueWait.Milliseconds(),
		"upstream_ms", upstream.Milliseconds(),
		"request_body_bytes", len(reqBody),
		"response_body_bytes", len(respBody),
	}
	if summary := summarizeAuditRequest(reqBody); summary != nil {
		attrs = append(attrs, summary...)
	}
	promptTokens, completionTokens, totalTokens := ollamaTokenCounts(respBody)
	attrs = append(attrs,
		"prompt_tokens", promptTokens,
		"completion_tokens", completionTokens,
		"total_tokens", totalTokens,
	)
	if len(respBody) > 0 {
		attrs = append(attrs, "response_json_valid", json.Valid(respBody))
	}
	if s.cfg.AuditLogIncludeBody {
		attrs = append(attrs,
			"request_body", string(reqBody),
			"response_body", string(respBody),
		)
	}
	return attrs
}

func ollamaTokenCounts(respBody []byte) (promptTokens, completionTokens, totalTokens int) {
	var resp struct {
		PromptEvalCount  *int `json:"prompt_eval_count"`
		EvalCount        *int `json:"eval_count"`
		TotalTokens      *int `json:"total_tokens"`
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	}
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return 0, 0, 0
	}
	if resp.PromptEvalCount != nil {
		promptTokens = *resp.PromptEvalCount
	}
	if resp.EvalCount != nil {
		completionTokens = *resp.EvalCount
	}
	if resp.PromptTokens != nil {
		promptTokens = *resp.PromptTokens
	}
	if resp.CompletionTokens != nil {
		completionTokens = *resp.CompletionTokens
	}
	if resp.TotalTokens != nil {
		totalTokens = *resp.TotalTokens
	} else {
		totalTokens = promptTokens + completionTokens
	}
	return promptTokens, completionTokens, totalTokens
}

func (s *Server) persistRequestEvent(client, id, model, path string, reqBody, respBody []byte, status string, total, queueWait, upstream time.Duration, upstreamName string, streaming bool, attrs []any) {
	if s.store == nil {
		return
	}
	requestID := id
	if requestID == "" {
		requestID = newUUID()
	}
	toolNames, toolErrors := auditStringSlices(attrs)
	responseValid := (*bool)(nil)
	if len(respBody) > 0 {
		v := json.Valid(respBody)
		responseValid = &v
	}
	evt := persistence.RequestEvent{
		RequestID:           requestID,
		Time:                time.Now(),
		Client:              client,
		Model:               model,
		Path:                path,
		Upstream:            upstreamName,
		Status:              status,
		Streaming:           streaming,
		DurationMS:          int(total.Milliseconds()),
		QueueMS:             int(queueWait.Milliseconds()),
		UpstreamMS:          int(upstream.Milliseconds()),
		RequestBodyBytes:    len(reqBody),
		ResponseBodyBytes:   len(respBody),
		ResponseJSONValid:   responseValid,
		PromptTokens:        auditInt(attrs, "prompt_tokens"),
		CompletionTokens:    auditInt(attrs, "completion_tokens"),
		RequestMessageCount: auditInt(attrs, "request_message_count"),
		ToolNames:           toolNames,
		ToolErrorCount:      len(toolErrors),
		ToolErrors:          toolErrors,
		RawAudit:            auditRawJSON(attrs),
	}
	if s.cfg.Database.PersistRequestBodies && s.cfg.Database.BodyReadEnabled {
		evt.RequestBodyText = string(reqBody)
		evt.ResponseBodyText = string(respBody)
		if json.Valid(reqBody) {
			evt.RequestBody = append([]byte(nil), reqBody...)
		}
		if json.Valid(respBody) {
			evt.ResponseBody = append([]byte(nil), respBody...)
		}
	}
	s.store.EnqueueRequestEvent(evt)
}

func auditStringSlices(attrs []any) (toolNames, toolErrors []string) {
	for i := 0; i+1 < len(attrs); i += 2 {
		key, _ := attrs[i].(string)
		switch key {
		case "request_tool_names":
			toolNames = stringSliceAttr(attrs[i+1])
		case "request_tool_errors":
			toolErrors = stringSliceAttr(attrs[i+1])
		}
	}
	return toolNames, toolErrors
}

func auditInt(attrs []any, want string) int {
	for i := 0; i+1 < len(attrs); i += 2 {
		key, _ := attrs[i].(string)
		if key != want {
			continue
		}
		switch v := attrs[i+1].(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}

func auditRawJSON(attrs []any) json.RawMessage {
	m := map[string]any{"msg": "request"}
	for i := 0; i+1 < len(attrs); i += 2 {
		key, _ := attrs[i].(string)
		if key == "" {
			continue
		}
		m[key] = attrs[i+1]
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func stringSliceAttr(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func summarizeAuditRequest(body []byte) []any {
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
			Name string `json:"name"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}

	toolNames := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		name := tool.Function.Name
		if name == "" {
			name = tool.Name
		}
		if name != "" {
			toolNames = append(toolNames, name)
		}
	}

	toolErrors := make([]string, 0)
	for _, msg := range req.Messages {
		if msg.Role != "tool" || msg.Content == "" {
			continue
		}
		var payload struct {
			Error  string `json:"error"`
			Stderr string `json:"stderr"`
		}
		if err := json.Unmarshal([]byte(msg.Content), &payload); err != nil {
			if strings.HasPrefix(msg.Content, "ERROR:") {
				toolErrors = append(toolErrors, msg.Content)
			}
			continue
		}
		if payload.Error != "" {
			toolErrors = append(toolErrors, payload.Error)
			continue
		}
		if payload.Stderr != "" {
			toolErrors = append(toolErrors, payload.Stderr)
		}
	}

	attrs := []any{"request_message_count", len(req.Messages)}
	if len(toolNames) > 0 {
		attrs = append(attrs, "request_tool_names", toolNames)
	}
	if len(toolErrors) > 0 {
		attrs = append(attrs,
			"request_tool_error_count", len(toolErrors),
			"request_tool_errors", toolErrors,
		)
	}
	return attrs
}

// recordHistory appends an entry to the UI history ring buffer.
func (s *Server) recordHistory(req *queue.Request, status string, total, queueWait, upstream time.Duration, model string) {
	if s.history == nil {
		return
	}
	s.history.Add(ui.HistoryEntry{
		ID:          req.ID,
		Client:      req.ClientName,
		Model:       model,
		Status:      status,
		DurationMs:  total.Milliseconds(),
		QueueMs:     queueWait.Milliseconds(),
		UpstreamMs:  upstream.Milliseconds(),
		CompletedAt: time.Now(),
	})
}
