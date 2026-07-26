# llama-line Empty Response Fixes Implementation Plan

> **Historical cross-repository plan:** The standalone llama-line repository referenced below is no longer a Switchyard product boundary. Switchyard compatibility behavior is documented in `docs/reference/client-compatibility.md`; this checklist is not current status.

> **For agentic workers:** Execute this plan task-by-task. Recommended path:
> dispatch a fresh subagent per task, review each result with `review-quality`,
> then continue. For complex multi-agent splits, use
> `parallel-feature-development`, `team-composition-patterns`, and
> `team-communication-protocols`. Steps use checkbox (`- [ ]`) syntax for
> tracking.

**Goal:** Make local Ollama/llama-line rounds preserve usable content/tool-call payloads or fail loudly instead of producing successful-looking empty no-tool responses.

**Architecture:** Fix both sides of the compatibility boundary. Switchyard must preserve llama-line/OpenAI `tool_calls` and reject empty no-tool completions. llama-line must classify upstream HTTP errors and empty bodies as terminal SSE errors, then persist useful terminal-condition metadata.

**Tech Stack:** Go, `net/http`, SSE, Ollama `/api/chat`, OpenAI `/v1/chat/completions`, Switchyard `backend/internal/controlplane`, llama-line `internal/server` and `internal/proxy`, Go tests.

---

## Scope and repositories

- Switchyard repo: `/home/onnwee/Projects/tools/switchyard`
- llama-line repo: `/home/onnwee/Projects/tools/llama-line`

Implement Switchyard first, then llama-line. Keep commits separate by repository.

---

## File Structure

### Switchyard

- Modify: `backend/internal/controlplane/model_executor.go`
  - Extend `modelBrokerChatResponse` to decode `choices[].message.tool_calls`, `choices[].finish_reason`, and error payloads.
  - Return `ModelResponse.ToolCalls` from `generateViaLlamaLine()`.
  - Reject empty content with no tool calls.
  - Make `streamViaLlamaLine()` detect terminal status/error payloads and empty content streams.
- Modify: `backend/internal/controlplane/model_executor_internal_test.go`
  - Parser tests for tool-call-only OpenAI responses, broker error payloads, empty no-tool completions, and streamed terminal errors.
- Modify: `backend/internal/controlplane/model_executor_test.go`
  - EnvModelBroker tests for llama-line tool-call preservation and empty-completion rejection.
- Modify: `docs/reference/client-compatibility.md`
  - Document tool-call preservation and empty-completion rejection.

### llama-line

- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/forwarder.go`
  - Return an error for upstream HTTP status >= 400, including status and a bounded body snippet.
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/pipe.go`
  - Return `ErrEmptyUpstreamBody` when SSE-wrapping a non-streaming upstream response with no non-empty lines.
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/*_test.go`
  - Tests for HTTP error classification and empty-body detection.
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/server/handler.go`
  - Convert `PipeResponse` errors into terminal SSE status events and non-success audit/history statuses.
  - Add audit fields for terminal condition and response shape.
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/server/handler_test.go`
  - Tests for empty body, upstream 500/502, and tool-call-only response metadata.
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/metrics/metrics.go`
  - Add terminal-condition counter if the metrics registry pattern supports it cleanly.
- Modify: `/home/onnwee/Projects/tools/llama-line/README.md` and `/home/onnwee/Projects/tools/llama-line/skill/SKILL.md`
  - Document the terminal response contract.

---

## Task 1: Switchyard parser preserves OpenAI tool calls

**Files:**
- Modify: `backend/internal/controlplane/model_executor.go:633-675`
- Modify: `backend/internal/controlplane/model_executor_internal_test.go`

- [ ] **Step 1: Write failing parser tests**

Add these tests to `backend/internal/controlplane/model_executor_internal_test.go` and convert imports to include `strings`, `testing`, and `time`:

```go
func TestDecodeModelBrokerChatResponsePreservesToolCalls(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_fetch","arguments":"{\"url\":\"https://example.com\"}"}}]},"finish_reason":"tool_calls"}]}`)
	decoded, err := decodeModelBrokerChatResponse(body)
	if err != nil {
		t.Fatalf("decodeModelBrokerChatResponse() error = %v", err)
	}
	if got := len(decoded.Choices[0].Message.ToolCalls); got != 1 {
		t.Fatalf("tool_calls len = %d, want 1", got)
	}
	if got := decoded.Choices[0].FinishReason; got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", got)
	}
}

func TestDecodeModelBrokerChatResponseRejectsOpenAIErrorPayload(t *testing.T) {
	body := []byte("data: {\"error\":{\"message\":\"upstream exploded\",\"type\":\"server_error\"}}\n\n")
	_, err := decodeModelBrokerChatResponse(body)
	if err == nil || !strings.Contains(err.Error(), "upstream exploded") {
		t.Fatalf("error = %v, want upstream exploded", err)
	}
}

func TestDecodeModelBrokerChatResponseMarksEmptyCompletion(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"message\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\n")
	decoded, err := decodeModelBrokerChatResponse(body)
	if err != nil {
		t.Fatalf("decodeModelBrokerChatResponse() error = %v", err)
	}
	if !decoded.EmptyNoToolCompletion() {
		t.Fatal("EmptyNoToolCompletion() = false, want true")
	}
}
```

- [ ] **Step 2: Run failing tests**

Run from `/home/onnwee/Projects/tools/switchyard`:

```bash
go test ./backend/internal/controlplane -run 'TestDecodeModelBrokerChatResponse' -count=1
```

Expected: compile/test failure because tool-call fields and `EmptyNoToolCompletion` do not exist.

- [ ] **Step 3: Extend response types and parser**

Replace `modelBrokerChatResponse` with:

```go
type modelBrokerChatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []any  `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (r modelBrokerChatResponse) EmptyNoToolCompletion() bool {
	if len(r.Choices) == 0 {
		return false
	}
	choice := r.Choices[0]
	return strings.TrimSpace(choice.Message.Content) == "" && len(choice.Message.ToolCalls) == 0
}
```

After `json.Unmarshal` in `decodeModelBrokerChatResponse`, add:

```go
if decoded.Error != nil {
	message := strings.TrimSpace(decoded.Error.Message)
	if message == "" {
		message = "model broker returned error payload"
	}
	return modelBrokerChatResponse{}, errors.New(message)
}
```

- [ ] **Step 4: Run parser tests**

```bash
go test ./backend/internal/controlplane -run 'TestDecodeModelBrokerChatResponse' -count=1
```

Expected: PASS.

---

## Task 2: Switchyard llama-line Generate returns tool calls and rejects empty completions

**Files:**
- Modify: `backend/internal/controlplane/model_executor.go:321-373`
- Modify: `backend/internal/controlplane/model_executor_test.go`

- [ ] **Step 1: Write failing broker tests**

Add `strings` to imports and add:

```go
func TestEnvModelBrokerLlamaLinePreservesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Llama-Line-Request-ID", "llama-tool-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"web_fetch","arguments":"{\"url\":\"https://example.com\"}"}}]},"finish_reason":"tool_calls"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", server.URL)

	response, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "qwen3:14b", Messages: []controlplane.ModelMessage{{Role: "user", Content: "fetch"}}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "" || len(response.ToolCalls) != 1 || response.CorrelationIDs["llama_line_request_id"] != "llama-tool-1" {
		t.Fatalf("response = %+v", response)
	}
}

func TestEnvModelBrokerLlamaLineRejectsEmptyCompletion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	t.Setenv("MODEL_BROKER_BACKEND", "llama-line")
	t.Setenv("LLAMA_LINE_BASE_URL", server.URL)

	_, err := controlplane.NewEnvModelBroker().Generate(t.Context(), controlplane.ModelRequest{Model: "qwen3:14b", Messages: []controlplane.ModelMessage{{Role: "user", Content: "use a tool"}}})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Generate() error = %v, want empty completion error", err)
	}
}
```

- [ ] **Step 2: Run failing tests**

```bash
go test ./backend/internal/controlplane -run 'TestEnvModelBrokerLlamaLine' -count=1
```

Expected: tool calls are dropped and empty completion is accepted.

- [ ] **Step 3: Return tool calls and reject empty completions**

In `generateViaLlamaLine()`, replace the final return with:

```go
choice := decoded.Choices[0]
if decoded.EmptyNoToolCompletion() {
	return ModelResponse{}, fmt.Errorf("model response was empty and contained no tool calls")
}
return ModelResponse{Content: choice.Message.Content, ToolCalls: choice.Message.ToolCalls, CorrelationIDs: correlation}, nil
```

- [ ] **Step 4: Run broker tests**

```bash
go test ./backend/internal/controlplane -run 'TestEnvModelBrokerLlamaLine|TestEnvModelExecutorUsesLlamaLine' -count=1
```

Expected: PASS.

---

## Task 3: Switchyard streaming llama-line path detects terminal errors and empty streams

**Files:**
- Modify: `backend/internal/controlplane/model_executor.go:490-580`
- Modify: `backend/internal/controlplane/model_executor_internal_test.go`

- [ ] **Step 1: Add stream classifier tests**

```go
func TestOpenAIStreamPayloadClassifiesTerminalError(t *testing.T) {
	event := classifyOpenAIStreamPayload(`{"status":"ollama_unavailable","message":"connection refused","request_id":"req"}`)
	if event.Err == nil || !strings.Contains(event.Err.Error(), "connection refused") {
		t.Fatalf("event.Err = %v, want connection refused", event.Err)
	}
}

func TestOpenAIStreamPayloadContentStillWorks(t *testing.T) {
	event := classifyOpenAIStreamPayload(`{"choices":[{"delta":{"content":"hello"}}]}`)
	if event.Content != "hello" || event.Err != nil {
		t.Fatalf("event = %+v", event)
	}
}
```

- [ ] **Step 2: Implement classifier**

Replace `openAIStreamPayloadContent` internals with a classifier that returns content or error:

```go
type openAIStreamPayloadEvent struct {
	Content string
	Err     error
}

func classifyOpenAIStreamPayload(payload string) openAIStreamPayloadEvent {
	var status struct {
		Status  string `json:"status"`
		Message string `json:"message"`
		Error   any    `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &status); err == nil {
		if status.Status == "ollama_unavailable" || status.Status == "dropped_by_admin" || status.Status == "failed" {
			message := firstNonEmpty(strings.TrimSpace(status.Message), stringFromAny(status.Error), "model broker stream ended with status "+status.Status)
			return openAIStreamPayloadEvent{Err: errors.New(message)}
		}
	}
	var decoded struct {
		Choices []struct {
			Delta   struct{ Content string `json:"content"` } `json:"delta"`
			Message struct{ Content string `json:"content"` } `json:"message"`
			Text    string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil || len(decoded.Choices) == 0 {
		return openAIStreamPayloadEvent{}
	}
	return openAIStreamPayloadEvent{Content: firstNonEmpty(decoded.Choices[0].Delta.Content, decoded.Choices[0].Message.Content, decoded.Choices[0].Text)}
}
```

- [ ] **Step 3: Use classifier in `streamViaLlamaLine()`**

Replace content extraction with:

```go
event := classifyOpenAIStreamPayload(payload)
if event.Err != nil {
	return ModelResponse{}, event.Err
}
chunk := event.Content
if chunk == "" {
	continue
}
```

Before emitting done, add:

```go
if strings.TrimSpace(builder.String()) == "" {
	return ModelResponse{}, errors.New("model broker stream ended without content")
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./backend/internal/controlplane -run 'TestOpenAIStreamPayload|TestDecodeModelBrokerChatResponse' -count=1
```

Expected: PASS.

---

## Task 4: llama-line proxy classifies upstream HTTP errors

**Files:**
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/forwarder.go`
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/forwarder_test.go`

- [ ] **Step 1: Add failing test**

Add a test that makes fake Ollama return HTTP 502 with body `{"error":"model load failed"}` and asserts `Forward()` returns `Err != nil`, contains `502` and `model load failed`, and returns no `Resp`.

- [ ] **Step 2: Implement `UpstreamHTTPError`**

In `forwarder.go`, add:

```go
type UpstreamHTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e UpstreamHTTPError) Error() string {
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Sprintf("upstream ollama returned %s", e.Status)
	}
	return fmt.Sprintf("upstream ollama returned %s: %s", e.Status, strings.TrimSpace(e.Body))
}
```

After `f.client.Do(req)` succeeds, before returning the response:

```go
if resp.StatusCode >= 400 {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	return queue.Result{Err: UpstreamHTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: string(body)}}
}
```

- [ ] **Step 3: Run proxy tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./internal/proxy -count=1
```

Expected: PASS.

---

## Task 5: llama-line detects empty upstream bodies during SSE wrapping

**Files:**
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/pipe.go`
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/proxy/pipe_test.go`

- [ ] **Step 1: Add failing empty-body test**

Add a test to `pipe_test.go` where `PipeResponse(w, resp, true)` receives HTTP 200 JSON content type with an empty body and returns an error containing `empty upstream response body`.

- [ ] **Step 2: Implement sentinel error**

In `pipe.go`, import `errors` and add:

```go
var ErrEmptyUpstreamBody = errors.New("empty upstream response body")
```

In the SSE-wrapping scanner branch, track whether any non-empty line was emitted. After scanning, return `ErrEmptyUpstreamBody` if nothing was emitted.

- [ ] **Step 3: Run proxy tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./internal/proxy -count=1
```

Expected: PASS.

---

## Task 6: llama-line handler persists terminal condition and response shape metadata

**Files:**
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/server/handler.go`
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/server/handler_test.go`

- [ ] **Step 1: Add failing server tests**

Add tests for:

1. Empty upstream body is audited with `status: "empty_body"` and `terminal_condition: "empty_body"`.
2. Tool-call-only Ollama response is audited with `response_has_message_content: false`, `response_tool_call_count: 1`, and `response_done_reason: "stop"`.
3. Upstream HTTP 502 is audited as non-success and emits terminal `ollama_unavailable` SSE.

- [ ] **Step 2: Convert `PipeResponse` errors into terminal SSE status**

In `handler.go`, replace the direct `proxy.PipeResponse(w, result.Resp, true)` success path with:

```go
pipeErr := proxy.PipeResponse(w, result.Resp, true)
if pipeErr != nil {
	status := "pipe_error"
	if errors.Is(pipeErr, proxy.ErrEmptyUpstreamBody) {
		status = "empty_body"
	}
	_ = WriteStatusEvent(w, queue.StatusUpdate{RequestID: req.ID, Status: "ollama_unavailable", Message: pipeErr.Error()})
	s.writeAuditLog(clientName, req.ID, model, r.URL.Path, bodyBytes, respBodyBytes, status, elapsed, queueWait, upstream, entry.cfg.Name, streaming)
	s.recordHistory(req, status, elapsed, queueWait, upstream, model)
	return
}
```

Add logging and metrics in the same style as existing error/success branches.

- [ ] **Step 3: Add response-shape audit helper**

In `auditAttrs`, append `summarizeAuditResponse(respBody)...`. Implement helper to return:

- `terminal_condition`: `normal_response`, `empty_body`, or `error_payload`
- `response_has_message_content`: bool
- `response_tool_call_count`: int
- `response_done_reason`: Ollama `done_reason` or OpenAI `finish_reason`

Use JSON decoding for both Ollama shape (`message.content`, `message.tool_calls`, `done_reason`) and OpenAI shape (`choices[0].message.content`, `choices[0].message.tool_calls`, `choices[0].finish_reason`).

- [ ] **Step 4: Run server tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./internal/server -count=1
```

Expected: PASS.

---

## Task 7: llama-line metrics expose terminal conditions

**Files:**
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/metrics/metrics.go`
- Modify: `/home/onnwee/Projects/tools/llama-line/internal/server/handler.go`

- [ ] **Step 1: Add counter**

Add a Prometheus counter vector:

```go
TerminalConditions *prometheus.CounterVec
```

Register:

```go
prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "llama_line_terminal_condition_total",
	Help: "Terminal conditions for llama-line requests by client, model, and condition.",
}, []string{"client", "model", "condition"})
```

- [ ] **Step 2: Increment in handler terminal paths**

Increment with labels `clientName`, `model`, and status/condition for: `success`, `error`, `empty_body`, `pipe_error`, `dropped_by_admin`, `cache_hit`, `dedup_hit`, and `dedup_error`.

- [ ] **Step 3: Run tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./internal/metrics ./internal/server -count=1
```

Expected: PASS.

---

## Task 8: Documentation updates

**Files:**
- Modify: `docs/reference/client-compatibility.md`
- Modify: `/home/onnwee/Projects/tools/llama-line/README.md`
- Modify: `/home/onnwee/Projects/tools/llama-line/skill/SKILL.md`

- [ ] **Step 1: Update Switchyard docs**

Add to `docs/reference/client-compatibility.md`:

```markdown
Switchyard rejects llama-line/OpenAI-compatible chat completions that contain no assistant content and no `message.tool_calls`. Tool-call-only responses are valid and are preserved as `ModelResponse.ToolCalls` internally and as OpenAI-compatible `message.tool_calls` externally.
```

- [ ] **Step 2: Update llama-line docs**

Add to llama-line README and skill docs:

```markdown
For non-streaming upstream responses, llama-line emits exactly one final SSE data event containing the full upstream JSON body. If upstream Ollama returns HTTP >= 400, an empty body, a read failure, or a broker timeout, llama-line emits a terminal error status instead of logging the request as success. Empty upstream bodies are terminal errors; a stream with only queued/status events and no final model JSON is not a successful completion.
```

- [ ] **Step 3: Run doc sanity checks**

```bash
cd /home/onnwee/Projects/tools/switchyard && grep -R "no assistant content" docs/reference/client-compatibility.md
cd /home/onnwee/Projects/tools/llama-line && grep -R "Empty upstream bodies" README.md skill/SKILL.md
```

Expected: both commands print the new documentation lines.

---

## Task 9: End-to-end verification

- [ ] **Step 1: Run Switchyard focused tests**

```bash
cd /home/onnwee/Projects/tools/switchyard && go test ./backend/internal/controlplane -count=1
```

Expected: PASS.

- [ ] **Step 2: Run Switchyard full verification**

```bash
cd /home/onnwee/Projects/tools/switchyard && make verify
```

Expected: PASS.

- [ ] **Step 3: Run llama-line focused tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./internal/proxy ./internal/server ./internal/metrics -count=1
```

Expected: PASS.

- [ ] **Step 4: Run llama-line full tests**

```bash
cd /home/onnwee/Projects/tools/llama-line && go test ./... -count=1
```

Expected: PASS.

- [ ] **Step 5: Manual smoke cases**

Use fake Ollama responses for three cases:

1. Tool-call-only HTTP 200 JSON response. Expected: llama-line emits one final SSE data event; Switchyard returns `ToolCalls` length 1.
2. Empty HTTP 200 body. Expected: llama-line emits terminal `ollama_unavailable`; audit/history status is `empty_body`; Switchyard receives an error.
3. HTTP 502 JSON error. Expected: llama-line emits terminal error; audit/history status is non-success; Switchyard receives a retryable model broker error.

---

## Risk notes

- Once SSE headers are committed, llama-line cannot change HTTP status. Post-header failures must be terminal SSE events.
- Empty content is valid when `tool_calls` is present. Do not reject tool-call-only responses.
- Do not log full request/response bodies by default. Keep body snippets bounded and rely on existing body-inclusion config for full payloads.
- Avoid DB migrations unless UI/search needs first-class columns. `RawAudit` is enough for initial diagnosis.
- Cache hits currently return JSON, not SSE. If strict “always SSE” is required for cache hits, handle as a separate follow-up.

---

## Self-review

**Spec coverage:** Empty responses are covered by Tasks 1, 2, 5, 6, and 9. HTTP 502/upstream availability is covered by Tasks 4, 6, 7, and 9. SSE framing is covered by Tasks 5, 6, 8, and 9. Tool-call compatibility is covered by Tasks 1, 2, 3, 6, and 9. Queue/in-flight state already exists; this plan adds terminal metrics rather than changing queue scheduling because the evidence points first to response classification and tool-call dropping.

**Placeholder scan:** No task uses TBD/TODO/fill-later language. Each implementation task has exact files, commands, expected outcomes, and either code or concrete behavior.

**Type consistency:** New Switchyard names are `ToolCalls`, `FinishReason`, `EmptyNoToolCompletion`, and `classifyOpenAIStreamPayload`. New llama-line names are `UpstreamHTTPError`, `ErrEmptyUpstreamBody`, `terminal_condition`, `response_has_message_content`, `response_tool_call_count`, and `response_done_reason`.
