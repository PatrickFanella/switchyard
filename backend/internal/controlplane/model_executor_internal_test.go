package controlplane

import (
	"strings"
	"testing"
	"time"
)

func TestModelBrokerTimeoutUsesEnv(t *testing.T) {
	t.Setenv("MODEL_BROKER_TIMEOUT", "2m")
	if got := modelBrokerTimeout(); got != 2*time.Minute {
		t.Fatalf("modelBrokerTimeout() = %s", got)
	}
}

func TestDecodeModelBrokerChatResponseAcceptsPlainJSON(t *testing.T) {
	decoded, err := decodeModelBrokerChatResponse([]byte(`{"choices":[{"message":{"content":"APPROVED: ok"}}]}`))
	if err != nil {
		t.Fatalf("decodeModelBrokerChatResponse() error = %v", err)
	}
	if got := decoded.Choices[0].Message.Content; got != "APPROVED: ok" {
		t.Fatalf("content = %q", got)
	}
}

func TestDecodeModelBrokerChatResponseAcceptsSingleDataEvent(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"message\":{\"content\":\"APPROVED: ok\"}}]}\n\n")
	decoded, err := decodeModelBrokerChatResponse(body)
	if err != nil {
		t.Fatalf("decodeModelBrokerChatResponse() error = %v", err)
	}
	if got := decoded.Choices[0].Message.Content; got != "APPROVED: ok" {
		t.Fatalf("content = %q", got)
	}
}

func TestDecodeModelBrokerChatResponseIgnoresQueueStatusEvents(t *testing.T) {
	body := []byte("data: {\"request_id\":\"req\",\"status\":\"queued\"}\n\ndata: {\"choices\":[{\"message\":{\"content\":\"CHANGES_REQUESTED: fix reconnect\"}}]}\n\n")
	decoded, err := decodeModelBrokerChatResponse(body)
	if err != nil {
		t.Fatalf("decodeModelBrokerChatResponse() error = %v", err)
	}
	if got := decoded.Choices[0].Message.Content; got != "CHANGES_REQUESTED: fix reconnect" {
		t.Fatalf("content = %q", got)
	}
}

func TestDecodeModelBrokerChatResponseRejectsStatusOnlyStream(t *testing.T) {
	body := []byte("data: {\"request_id\":\"req\",\"status\":\"queued\"}\n\n")
	_, err := decodeModelBrokerChatResponse(body)
	if err == nil {
		t.Fatal("expected status-only stream to fail")
	}
}

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

func TestModelAdapterPromptAndResponsePreserveToolContract(t *testing.T) {
	request := ModelRequest{
		Messages:   []ModelMessage{{Role: "user", Content: "write the proof"}},
		Tools:      []any{map[string]any{"type": "function", "function": map[string]any{"name": "write", "parameters": map[string]any{"type": "object"}}}},
		ToolChoice: "auto",
	}
	prompt, err := modelAdapterPrompt(request)
	if err != nil || !strings.Contains(prompt, `"name":"write"`) || !strings.Contains(prompt, `"tool_choice":"auto"`) {
		t.Fatalf("modelAdapterPrompt() = %q, %v", prompt, err)
	}
	content, calls, err := decodeModelAdapterResponse("```json\n{\"content\":\"\",\"tool_calls\":[{\"id\":\"call_write\",\"name\":\"write\",\"arguments\":{\"path\":\"PROOF.md\",\"content\":\"proof\"}}]}\n```")
	if err != nil || content != "" || len(calls) != 1 {
		t.Fatalf("decodeModelAdapterResponse() content=%q calls=%v err=%v", content, calls, err)
	}
	call := calls[0].(map[string]any)
	function := call["function"].(map[string]any)
	if function["name"] != "write" || !strings.Contains(function["arguments"].(string), `"path":"PROOF.md"`) {
		t.Fatalf("tool call = %#v", call)
	}
}

func TestDecodeModelAdapterResponseFailsClosed(t *testing.T) {
	for _, raw := range []string{"not json", `{}`, `{"content":"","tool_calls":[{"name":"write"}]}`} {
		if _, _, err := decodeModelAdapterResponse(raw); err == nil {
			t.Fatalf("decodeModelAdapterResponse(%q) succeeded", raw)
		}
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

func TestOpenAIStreamPayloadRejectsToolCallsWithoutContent(t *testing.T) {
	event := classifyOpenAIStreamPayload(`{"choices":[{"delta":{"tool_calls":[{"id":"call_1"}]}}]}`)
	if event.Err == nil || !strings.Contains(event.Err.Error(), "use non-streaming tool path") {
		t.Fatalf("event.Err = %v", event.Err)
	}
}

func TestOpenAIStreamPayloadRejectsTopLevelError(t *testing.T) {
	event := classifyOpenAIStreamPayload(`{"error":{"message":"bad request"}}`)
	if event.Err == nil || !strings.Contains(event.Err.Error(), "bad request") {
		t.Fatalf("event.Err = %v", event.Err)
	}
}
