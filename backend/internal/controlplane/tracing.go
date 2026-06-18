package controlplane

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const controlplaneTracerName = "git.subcult.tv/PatrickFanella/switchyard/internal/controlplane"

func startControlPlaneSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer(controlplaneTracerName).Start(ctx, name, trace.WithAttributes(attrs...))
}

func finishControlPlaneSpan(span trace.Span, status string, err error, attrs ...attribute.KeyValue) {
	defer span.End()
	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}
	span.SetAttributes(attribute.String("status", status))
	if err != nil {
		span.SetAttributes(attribute.String("error", err.Error()))
		span.RecordError(err)
		span.SetStatus(codes.Error, status)
		return
	}
	span.SetStatus(codes.Ok, status)
}
