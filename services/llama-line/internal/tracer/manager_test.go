package tracer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

func TestManagerRoutesClientProjects(t *testing.T) {
	var mu sync.Mutex
	projects := map[string]int{}
	done := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		project := r.Header.Get("x-project-name")
		mu.Lock()
		projects[project]++
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		done <- struct{}{}
	}))
	defer server.Close()

	mgr, err := NewManager(context.Background(), config.PhoenixConfig{Enabled: true, OTLPEndpoint: server.URL, ProjectName: "llama-line", ProjectMode: "client"})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	_, span := mgr.TracerForClient("subcorp").Start(context.Background(), "test")
	span.End()
	_, span = mgr.TracerForClient("jury-rigged").Start(context.Background(), "test")
	span.End()
	mgr.Shutdown()

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for exports")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if projects["subcorp"] == 0 || projects["jury-rigged"] == 0 {
		t.Fatalf("projects = %#v", projects)
	}
}
