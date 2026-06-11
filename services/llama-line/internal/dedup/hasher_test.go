package dedup_test

import (
	"encoding/json"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/dedup"
)

func buildBody(model string, messages any) []byte {
	b, _ := json.Marshal(map[string]any{"model": model, "messages": messages})
	return b
}

func TestHashRequest_Basic(t *testing.T) {
	body := buildBody("llama3", []map[string]string{{"role": "user", "content": "hello"}})
	hash, ok := dedup.HashRequest(body)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(hash) != 64 {
		t.Fatalf("expected 64-char hex hash, got %d chars", len(hash))
	}
}

func TestHashRequest_SameInputSameHash(t *testing.T) {
	body := buildBody("llama3", []map[string]string{{"role": "user", "content": "hello"}})
	h1, _ := dedup.HashRequest(body)
	h2, _ := dedup.HashRequest(body)
	if h1 != h2 {
		t.Fatal("same input should produce same hash")
	}
}

func TestHashRequest_DifferentModelDifferentHash(t *testing.T) {
	msgs := []map[string]string{{"role": "user", "content": "hello"}}
	h1, _ := dedup.HashRequest(buildBody("llama3", msgs))
	h2, _ := dedup.HashRequest(buildBody("mistral", msgs))
	if h1 == h2 {
		t.Fatal("different models should produce different hashes")
	}
}

func TestHashRequest_MissingModel(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	_, ok := dedup.HashRequest(body)
	if ok {
		t.Fatal("expected ok=false for missing model")
	}
}

func TestHashRequest_InvalidJSON(t *testing.T) {
	_, ok := dedup.HashRequest([]byte("not json"))
	if ok {
		t.Fatal("expected ok=false for invalid JSON")
	}
}
