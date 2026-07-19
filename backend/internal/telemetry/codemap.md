# backend/internal/telemetry/

## Responsibility
Builds the optional OpenTelemetry OTLP/HTTP trace provider used by Switchyard.

## Design
- Typed `OTLPTraceConfig` is derived from standard OTEL environment variables.
- Endpoint/path parsing supports secure and explicitly insecure HTTP collectors.
- No endpoint means a no-op shutdown function, keeping telemetry optional.

## Flow
1. Parse endpoint, headers, service name, and trace path.
2. Create an OTLP exporter and batched tracer provider.
3. Register it globally in `cmd/app` and flush it during shutdown.

## Integration
- Consumed by `backend/cmd/app/main.go`.
- Exports to the collector configured by `OTEL_EXPORTER_OTLP_ENDPOINT`.
