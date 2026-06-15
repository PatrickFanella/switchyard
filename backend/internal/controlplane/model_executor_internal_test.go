package controlplane

import "testing"
import "time"

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
