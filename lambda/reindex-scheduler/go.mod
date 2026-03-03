// Re-index Scheduler Lambda — triggered by EventBridge cron.
// Calls Metadata Service via gRPC to find stale/dirty books,
// re-emits pdf.uploaded per book to restart the ingestion pipeline.

module github.com/belLena81/raglibrarian/lambda/reindex-scheduler

go 1.26

require (
    // ── Lambda runtime ───────────────────────────────────────────────────────
    github.com/aws/aws-lambda-go              v1.47.0

    // ── gRPC client → Metadata Service ───────────────────────────────────────
    google.golang.org/grpc                                v1.70.0
    google.golang.org/protobuf                            v1.36.5
    github.com/belLena81/raglibrarian/pkg/proto            v0.0.0

    // ── Messaging ─────────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/events   v0.0.0
    github.com/rabbitmq/amqp091-go            v1.10.0

    // ── Config ───────────────────────────────────────────────────────────────
    github.com/spf13/viper                    v1.20.0

    // ── Observability ─────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry v0.0.0
    go.uber.org/zap                           v1.27.0
)

replace (
    github.com/belLena81/raglibrarian/pkg/proto     => ../../pkg/proto
    github.com/belLena81/raglibrarian/pkg/events    => ../../pkg/events
    github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)