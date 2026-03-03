// Embedder Lambda — triggered by RabbitMQ chunks.ready queue.
// Reads chunk JSON from S3, generates embeddings via Anthropic SDK,
// writes vectors + payload metadata to Qdrant, publishes index.done.

module github.com/belLena81/raglibrarian/lambda/embedder

go 1.26

require (
    // ── Lambda runtime ───────────────────────────────────────────────────────
    github.com/aws/aws-lambda-go              v1.47.0

    // ── AWS SDK v2 (S3 read) ──────────────────────────────────────────────────
    github.com/aws/aws-sdk-go-v2              v1.36.1
    github.com/aws/aws-sdk-go-v2/config       v1.29.6
    github.com/aws/aws-sdk-go-v2/service/s3   v1.74.1

    // ── LLM / Embeddings ──────────────────────────────────────────────────────
    github.com/anthropics/anthropic-sdk-go    v1.13.0

    // ── Token counting (validates chunk size before embedding API call) ────────
    github.com/pkoukk/tiktoken-go             v0.1.7

    // ── Vector DB (write vectors + payload) ────────────────────────────────────
    github.com/qdrant/go-client               v1.13.0

    // ── Messaging ─────────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/events   v0.0.0
    github.com/rabbitmq/amqp091-go            v1.10.0

    // ── Observability ─────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry v0.0.0
    go.uber.org/zap                           v1.27.0
)

replace (
    github.com/belLena81/raglibrarian/pkg/events    => ../../pkg/events
    github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)