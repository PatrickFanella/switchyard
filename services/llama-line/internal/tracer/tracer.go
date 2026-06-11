package tracer

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	"git.subcult.tv/PatrickFanella/llama-line/internal/config"
)

// New builds an OTEL tracer for optional Phoenix export.
func New(ctx context.Context, cfg config.PhoenixConfig) (trace.Tracer, func(), error) {
	if !cfg.Enabled {
		return nil, func() {}, nil
	}
	if cfg.OTLPEndpoint == "" {
		return nil, nil, errors.New("phoenix otlp endpoint required")
	}

	exporterOpts := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint),
		otlptracehttp.WithInsecure(),
	}
	if cfg.ProjectName != "" {
		exporterOpts = append(exporterOpts, otlptracehttp.WithHeaders(map[string]string{
			"x-project-name": cfg.ProjectName,
		}))
	}
	exporter, err := otlptracehttp.New(ctx, exporterOpts...)
	if err != nil {
		return nil, nil, err
	}

	attrs := []attribute.KeyValue{semconv.ServiceNameKey.String("llama-line")}
	if cfg.ProjectName != "" {
		attrs = append(attrs, attribute.String("openinference.project.name", cfg.ProjectName))
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(semconv.SchemaURL, attrs...)),
	)

	return provider.Tracer("llama-line"), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	}, nil
}
