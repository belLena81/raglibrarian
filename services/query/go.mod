// User-facing REST API + RAG orchestration service.
// Responsibilities: auth middleware, role enforcement, query pipeline,
// LLM synthesis, admin dashboard endpoints.

module github.com/belLena81/raglibrarian/services/query

go 1.26

require (
    // ── HTTP layer ──────────────────────────────────────────────────────────
    github.com/go-chi/chi/v5                  v5.2.1    // idiomatic stdlib-compatible router
    github.com/go-chi/httplog/v2              v2.1.0    // structured request logging (chi-native)
    github.com/go-playground/validator/v10    v10.24.0  // struct tag input validation

    // ── Auth ────────────────────────────────────────────────────────────────
    github.com/o1ecc8b/paseto                 v2.0.0+incompatible // PASETO v2 local tokens
    golang.org/x/crypto                       v0.32.0             // bcrypt password hashing

    // ── gRPC clients (calls Retrieval + Metadata services) ──────────────────
    google.golang.org/grpc                                v1.70.0
    google.golang.org/protobuf                            v1.36.5
    github.com/belLena81/raglibrarian/pkg/proto            v0.0.0

    // ── LLM (in-process synthesis) ───────────────────────────────────────────
    github.com/anthropics/anthropic-sdk-go    v1.13.0

    // ── Database (user store, query log, admin stats) ────────────────────────
    github.com/jackc/pgx/v5                   v5.7.2
    github.com/jmoiron/sqlx                   v1.4.0    // thin ergonomic SQL layer over pgx stdlib adapter
    github.com/golang-migrate/migrate/v4      v4.18.1   // schema migrations run on service startup

    // ── Config ───────────────────────────────────────────────────────────────
    github.com/spf13/viper                    v1.20.0

    // ── Observability ────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry        v0.0.0
    go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.59.0
    go.uber.org/zap                           v1.27.0

    // ── API docs ─────────────────────────────────────────────────────────────
    github.com/swaggo/swag                    v1.16.4
    github.com/swaggo/http-swagger/v2         v2.0.2

    // ── Health checks ─────────────────────────────────────────────────────────
    github.com/alexliesenfeld/health          v0.8.0

    // ── Shared packages ───────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/events v0.0.0

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
    github.com/belLena81/raglibrarian/pkg/events    => ../../pkg/events
    github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)