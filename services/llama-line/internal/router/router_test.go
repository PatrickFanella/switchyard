package router

import (
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

func TestResolve_ExactMatch(t *testing.T) {
	r := New([]config.UpstreamConfig{
		{Models: []string{"llama3:8b", "llama3:70b"}},
		{Models: []string{"mistral:7b"}},
		{},
	})
	if got := r.Resolve("llama3:8b"); got != 0 {
		t.Fatalf("want 0 got %d", got)
	}
	if got := r.Resolve("mistral:7b"); got != 1 {
		t.Fatalf("want 1 got %d", got)
	}
	if got := r.Resolve("unknown"); got != 2 {
		t.Fatalf("want 2 (fallback) got %d", got)
	}
}

func TestResolve_Glob(t *testing.T) {
	r := New([]config.UpstreamConfig{
		{Models: []string{"llama3*"}},
		{},
	})
	if got := r.Resolve("llama3:8b"); got != 0 {
		t.Fatalf("want 0 got %d", got)
	}
	if got := r.Resolve("llama3:70b"); got != 0 {
		t.Fatalf("want 0 got %d", got)
	}
	if got := r.Resolve("gemma3"); got != 1 {
		t.Fatalf("want 1 got %d", got)
	}
}

func TestResolve_ProviderPrefixedOpenCodeModels(t *testing.T) {
	r := New([]config.UpstreamConfig{
		{Name: "ollama"},
		{Name: "ocq", Models: []string{"openai/gpt-*", "github-copilot/*"}},
	})
	if got := r.Resolve("openai/gpt-5.4-mini"); got != 1 {
		t.Fatalf("want 1 got %d", got)
	}
	if got := r.Resolve("github-copilot/gpt-4.1"); got != 1 {
		t.Fatalf("want 1 got %d", got)
	}
	if got := r.Resolve("opencode-gpt"); got != 0 {
		t.Fatalf("want 0 (fallback) got %d", got)
	}
}

func TestResolve_CaseInsensitive(t *testing.T) {
	r := New([]config.UpstreamConfig{
		{Models: []string{"Llama3*"}},
		{},
	})
	if got := r.Resolve("llama3:8b"); got != 0 {
		t.Fatalf("want 0 got %d", got)
	}
}

func TestResolve_NoFallback_UsesIndex0(t *testing.T) {
	r := New([]config.UpstreamConfig{
		{Models: []string{"llama3*"}},
		{Models: []string{"mistral*"}},
	})
	if got := r.Resolve("unknown"); got != 0 {
		t.Fatalf("want 0 (fallback=0) got %d", got)
	}
}
