package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.subcult.tv/PatrickFanella/llama-line/internal/proxy"
)

func TestPipeResponse_HeadersCopied(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"X-Custom": []string{"value"}},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	w := httptest.NewRecorder()
	if err := proxy.PipeResponse(w, resp, false); err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("X-Custom") != "value" {
		t.Errorf("X-Custom = %q", w.Header().Get("X-Custom"))
	}
}

func TestPipeResponse_BrokerHeaderPresent(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	w := httptest.NewRecorder()
	proxy.PipeResponse(w, resp, false)
	if w.Header().Get("X-Ollama-Broker") != "true" {
		t.Error("X-Ollama-Broker header missing")
	}
}

func TestPipeResponse_StatusCodePreserved(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusCreated,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("")),
	}
	w := httptest.NewRecorder()
	proxy.PipeResponse(w, resp, false)
	if w.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", w.Code)
	}
}

func TestPipeResponse_SSEWrapped(t *testing.T) {
	body := `{"response":"hello"}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := httptest.NewRecorder()
	proxy.PipeResponse(w, resp, true) // headersAlreadySent=true, non-SSE upstream → wrap
	got := w.Body.String()
	want := "data: " + body + "\n\n"
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestPipeResponse_SSEPassThrough(t *testing.T) {
	// When upstream is already SSE, chunks pass through as-is.
	chunks := "data: {\"delta\":\"Hi\"}\n\ndata: [DONE]\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(chunks)),
	}
	w := httptest.NewRecorder()
	proxy.PipeResponse(w, resp, true)
	if w.Body.String() != chunks {
		t.Errorf("body = %q, want %q", w.Body.String(), chunks)
	}
}

func TestPipeResponse_BodyStreamed(t *testing.T) {
	body := `{"response":"streamed"}`
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	w := httptest.NewRecorder()
	proxy.PipeResponse(w, resp, false)
	if w.Body.String() != body {
		t.Errorf("body = %q, want %q", w.Body.String(), body)
	}
}
