package telemetry

import (
	"context"
	"net/url"
	"os"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

type OTLPTraceConfig struct {
	ServiceName string
	Endpoint    string
	TracesPath  string
	Headers     map[string]string
}

type otlpHTTPEndpoint struct {
	Endpoint string
	Path     string
	Insecure bool
}

func OTLPTraceConfigFromEnv() OTLPTraceConfig {
	return OTLPTraceConfig{
		ServiceName: firstNonEmpty(strings.TrimSpace(os.Getenv("OTEL_SERVICE_NAME")), "switchyard"),
		Endpoint:    strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		TracesPath:  firstNonEmpty(strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PATH")), "/v1/traces"),
		Headers:     ParseOTLPHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")),
	}
}

func ParseOTLPHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	headers := make(map[string]string)
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers[key] = strings.TrimSpace(value)
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

func NewTracerProviderFromEnv(ctx context.Context) (*trace.TracerProvider, func(context.Context) error, error) {
	return NewTracerProvider(ctx, OTLPTraceConfigFromEnv())
}

func NewTracerProvider(ctx context.Context, cfg OTLPTraceConfig) (*trace.TracerProvider, func(context.Context) error, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, func(context.Context) error { return nil }, nil
	}
	endpoint, err := parseOTLPHTTPEndpoint(cfg.Endpoint, cfg.TracesPath)
	if err != nil {
		return nil, nil, err
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(endpoint.Endpoint),
		otlptracehttp.WithURLPath(endpoint.Path),
		otlptracehttp.WithHeaders(cfg.Headers),
	}
	if endpoint.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx,
		options...,
	)
	if err != nil {
		return nil, nil, err
	}
	attrs := []attribute.KeyValue{
		semconv.ServiceNameKey.String(firstNonEmpty(strings.TrimSpace(cfg.ServiceName), "switchyard")),
	}
	if projectName := otlpProjectName(cfg.Headers); strings.TrimSpace(projectName) != "" {
		attrs = append(attrs, attribute.String("openinference.project.name", strings.TrimSpace(projectName)))
	}
	provider := trace.NewTracerProvider(
		trace.WithBatcher(exporter),
		trace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, attrs...)),
	)
	return provider, provider.Shutdown, nil
}

func parseOTLPHTTPEndpoint(endpoint, tracesPath string) (otlpHTTPEndpoint, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return otlpHTTPEndpoint{}, err
	}
	path := strings.TrimSpace(tracesPath)
	if path == "" {
		path = "/v1/traces"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = path
	} else {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	}
	return otlpHTTPEndpoint{Endpoint: parsed.Host, Path: parsed.Path, Insecure: parsed.Scheme == "http" || parsed.Scheme == ""}, nil
}

func otlpProjectName(headers map[string]string) string {
	for key, value := range headers {
		if strings.EqualFold(key, "x-project-name") {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
