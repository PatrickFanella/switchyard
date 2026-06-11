package persistence

import (
	"strings"
	"testing"
)

func TestEmbeddingChunksPreferMessagesAndToolErrors(t *testing.T) {
	e := RequestEvent{
		RequestID:        "r1",
		Client:           "subcorp",
		Model:            "qwen3:14b",
		RequestBodyText:  `{"messages":[{"role":"system","content":"be precise"},{"role":"user","content":"browse the web"}]}`,
		ResponseBodyText: `{"message":{"role":"assistant","content":"done"}}`,
		ToolErrors:       []string{"permission denied docker API"},
	}
	chunks := embeddingChunks(e, 4000)
	if len(chunks) < 4 {
		t.Fatalf("chunks = %#v", chunks)
	}
	if chunks[0].Role != "tool_error" || !strings.Contains(chunks[0].Content, "docker API") {
		t.Fatalf("expected first chunk to be tool error, got %#v", chunks[0])
	}
	if chunks[1].Role != "system" || chunks[2].Role != "user" || chunks[3].Role != "assistant" {
		t.Fatalf("expected chat roles, got %#v", chunks)
	}
}

func TestVectorLiteralRejectsEmptyAndFormatsFloats(t *testing.T) {
	got, err := vectorLiteral([]float32{1, -0.25, 0})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[1,-0.25,0]" {
		t.Fatalf("vectorLiteral = %q", got)
	}
	if _, err := vectorLiteral(nil); err == nil {
		t.Fatal("expected empty vector error")
	}
}

func TestEmbeddingDimensionMatchesSchemaContract(t *testing.T) {
	if EmbeddingDimension != 768 {
		t.Fatalf("EmbeddingDimension=%d, schema uses vector(768)", EmbeddingDimension)
	}
}
