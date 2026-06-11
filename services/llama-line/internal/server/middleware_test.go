package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
	"git.subcult.tv/PatrickFanella/llama-line/internal/server"
)

func TestAuthMiddleware_MissingHeader(t *testing.T) {
	keys := []config.APIKey{{Name: "app", Key: "secret"}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := server.AuthMiddlewareFromKeys(keys, next)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthMiddleware_InvalidKey(t *testing.T) {
	keys := []config.APIKey{{Name: "app", Key: "secret"}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := server.AuthMiddlewareFromKeys(keys, next)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer wrongkey")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAuthMiddleware_ValidKey_AttachesClientName(t *testing.T) {
	keys := []config.APIKey{{Name: "my-app", Key: "secret"}}
	var gotName string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName, _ = r.Context().Value(server.ClientNameKey).(string)
		w.WriteHeader(http.StatusOK)
	})
	h := server.AuthMiddlewareFromKeys(keys, next)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotName != "my-app" {
		t.Errorf("ClientName = %q, want my-app", gotName)
	}
}

func TestAuthMiddleware_BrokerHeaderOn401(t *testing.T) {
	keys := []config.APIKey{}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	h := server.AuthMiddlewareFromKeys(keys, next)

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Header().Get("X-Ollama-Broker") != "true" {
		t.Error("X-Ollama-Broker header missing on 401")
	}
}

// Export ClientNameKey for test access — it's already exported in middleware.go
var _ context.Context // ensure context import used
