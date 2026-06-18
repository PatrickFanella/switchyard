package telemetry

import (
	"context"
	"reflect"
	"testing"
)

func TestParseOTLPHeaders(t *testing.T) {
	headers := ParseOTLPHeaders("x-project-name=switchyard, Authorization = Bearer token, invalid, empty=, spaced = hello world")
	want := map[string]string{
		"x-project-name": "switchyard",
		"Authorization":  "Bearer token",
		"empty":          "",
		"spaced":         "hello world",
	}
	if !reflect.DeepEqual(headers, want) {
		t.Fatalf("headers = %#v, want %#v", headers, want)
	}
}

func TestNewTracerProviderNoopWhenEndpointUnset(t *testing.T) {
	provider, shutdown, err := NewTracerProvider(context.Background(), OTLPTraceConfig{ServiceName: "switchyard"})
	if err != nil {
		t.Fatalf("NewTracerProvider() error = %v", err)
	}
	if provider != nil {
		t.Fatalf("provider = %#v, want nil", provider)
	}
	if shutdown == nil {
		t.Fatal("shutdown = nil, want no-op function")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown() error = %v", err)
	}
}

func TestParseOTLPHTTPEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		path     string
		want     otlpHTTPEndpoint
	}{
		{
			name:     "phoenix http endpoint",
			endpoint: "http://10.0.0.200:6006",
			path:     "/v1/traces",
			want:     otlpHTTPEndpoint{Endpoint: "10.0.0.200:6006", Path: "/v1/traces", Insecure: true},
		},
		{
			name:     "host without scheme",
			endpoint: "10.0.0.200:6006",
			path:     "v1/traces",
			want:     otlpHTTPEndpoint{Endpoint: "10.0.0.200:6006", Path: "/v1/traces", Insecure: true},
		},
		{
			name:     "existing base path",
			endpoint: "https://collector.example.com/otel",
			path:     "/v1/traces",
			want:     otlpHTTPEndpoint{Endpoint: "collector.example.com", Path: "/otel/v1/traces", Insecure: false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOTLPHTTPEndpoint(tt.endpoint, tt.path)
			if err != nil {
				t.Fatalf("parseOTLPHTTPEndpoint() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("endpoint = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestOTLPProjectNameIsCaseInsensitive(t *testing.T) {
	got := otlpProjectName(map[string]string{"X-Project-Name": "switchyard"})
	if got != "switchyard" {
		t.Fatalf("project = %q, want switchyard", got)
	}
}
