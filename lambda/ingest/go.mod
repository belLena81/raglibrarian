// PDF Ingest Lambda — triggered by RabbitMQ pdf.uploaded queue.
// Downloads PDF from S3, extracts text with pdfcpu, splits into chunks,
// saves chunks JSON to S3, publishes chunks.ready event.

module github.com/belLena81/raglibrarian/lambda/ingest

go 1.26

require (
    // ── Lambda runtime ───────────────────────────────────────────────────────
    github.com/aws/aws-lambda-go              v1.47.0

    // ── AWS SDK v2 (S3 read/write) ────────────────────────────────────────────
    github.com/aws/aws-sdk-go-v2              v1.36.1
    github.com/aws/aws-sdk-go-v2/config       v1.29.6
    github.com/aws/aws-sdk-go-v2/service/s3   v1.74.1

    // ── PDF processing ────────────────────────────────────────────────────────
    github.com/pdfcpu/pdfcpu                  v0.9.1    // primary: text extraction, TOC, page metadata

    // ── Chunking ──────────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/chunker  v0.0.0

    // ── Messaging ─────────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/events   v0.0.0
    github.com/rabbitmq/amqp091-go            v1.10.0

    // ── Observability ─────────────────────────────────────────────────────────
    github.com/belLena81/raglibrarian/pkg/telemetry v0.0.0
    go.uber.org/zap                           v1.27.0
)

replace (
    github.com/belLena81/raglibrarian/pkg/chunker   => ../pkg/chunker
    github.com/belLena81/raglibrarian/pkg/events    => ../pkg/events
    github.com/belLena81/raglibrarian/pkg/telemetry => ../pkg/telemetry
)