// Shared OpenTelemetry setup, Zap logger factory, and Prometheus registry helpers.
// Every service and Lambda imports this for a consistent observability baseline.

module github.com/belLena81/raglibrarian/pkg/telemetry

go 1.26

require (
    go.uber.org/zap     v1.27.0

    // ── OpenTelemetry core ───────────────────────────────────────────────────
    go.opentelemetry.io/otel                v1.34.0
    go.opentelemetry.io/otel/trace          v1.34.0
    go.opentelemetry.io/otel/metric         v1.34.0
    go.opentelemetry.io/otel/sdk            v1.34.0
    go.opentelemetry.io/otel/sdk/metric     v1.34.0

    // ── OTel exporters ───────────────────────────────────────────────────────
    go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc   v1.34.0
    go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.34.0
    go.opentelemetry.io/otel/exporters/prometheus                      v0.56.0

    // ── OTel HTTP instrumentation (chi middleware) ────────────────────────────
    go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp      v0.59.0

    // ── Prometheus client ────────────────────────────────────────────────────
    github.com/prometheus/client_golang     v1.21.1

    // ── gRPC for OTel exporter transport ─────────────────────────────────────
    google.golang.org/grpc                  v1.70.0
)