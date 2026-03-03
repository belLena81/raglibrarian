// Book/chapter/page CRUD service backed by PostgreSQL.
// Exposes a gRPC server.
// Called by: Query Service (enrichment), Lambdas (status updates), Re-index Scheduler.

module github.com/belLena81/raglibrarian/services/metadata

go 1.26

require (
    // ── gRPC server ──────────────────────────────────────────────────────────
    google.golang.org/grpc                            v1.70.0
    google.golang.org/protobuf                        v1.36.5
    github.com/belLena81/raglibrarian/pkg/proto        v0.0.0

    // ── Database ─────────────────────────────────────────────────────────────
    github.com/jackc/pgx/v5                           v5.7.2
    github.com/jmoiron/sqlx                           v1.4.0
    github.com/golang-migrate/migrate/v4              v4.18.1

    // ── Job queue ─────────────────────────────────────────────────────────────
    // River: Postgres-backed durable job queue; used for async index status updates
    // that must be retried on failure without losing the event.
    github.com/riverqueue/river                       v0.14.2
    github.com/riverqueue/river/riverdriver/riverpgxv5 v0.14.2

    // ── Config ───────────────────────────────────────────────────────────────
    github.com/spf13/viper                            v1.20.0

    // ── Observability ────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry    v0.0.0
    go.uber.org/zap                                   v1.27.0
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