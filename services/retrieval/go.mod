// Vector search service backed by Qdrant.
// Exposes a gRPC server; no HTTP surface.
// Called exclusively by the Query Service on the hot query path.

module github.com/belLena81/raglibrarian/services/retrieval

go 1.26

require (
    // ── gRPC server ──────────────────────────────────────────────────────────
    google.golang.org/grpc                            v1.70.0
    google.golang.org/protobuf                        v1.36.5
    github.com/belLena81/raglibrarian/pkg/proto        v0.0.0

    // ── Vector DB ────────────────────────────────────────────────────────────
    // go-client wraps Qdrant's gRPC API natively — no REST overhead on hot path
    github.com/qdrant/go-client                       v1.13.0

    // ── Config ───────────────────────────────────────────────────────────────
    github.com/spf13/viper                            v1.20.0

    // ── Observability ────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry    v0.0.0
    go.uber.org/zap                                   v1.27.0
    // OTel gRPC interceptor — traces every inbound gRPC call automatically
    go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.59.0

    // ── Assertions & mocking ─────────────────────────────────────────────────
    github.com/stretchr/testify          v1.10.0   // assert + require + suite

    // ── API mocking ──────────────────────────────────────────────────────────
    github.com/jarcoal/httpmock          v1.3.1    // mock Anthropic API calls in unit tests

    // ── Real infra in tests (no mocks for Postgres + Qdrant + RabbitMQ) ──────
    github.com/testcontainers/testcontainers-go          v0.35.0
    github.com/testcontainers/testcontainers-go/modules/postgres   v0.35.0
    github.com/testcontainers/testcontainers-go/modules/rabbitmq   v0.35.0
)

replace (
    github.com/belLena81/raglibrarian/pkg/proto     => ../../pkg/proto
    github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)