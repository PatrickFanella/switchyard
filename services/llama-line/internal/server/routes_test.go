package server_test

import (
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

func TestIsInferenceEndpoint_True(t *testing.T) {
	endpoints := []string{
		"/api/generate",
		"/api/chat",
		"/api/embed",
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/embeddings",
	}
	for _, ep := range endpoints {
		if !server.IsInferenceEndpoint(ep) {
			t.Errorf("IsInferenceEndpoint(%q) = false, want true", ep)
		}
	}
}

func TestIsInferenceEndpoint_False(t *testing.T) {
	nonEndpoints := []string{
		"/api/tags",
		"/api/pull",
		"/broker/status",
		"/api/version",
		"/api/ps",
		"/api/delete",
	}
	for _, ep := range nonEndpoints {
		if server.IsInferenceEndpoint(ep) {
			t.Errorf("IsInferenceEndpoint(%q) = true, want false", ep)
		}
	}
}
