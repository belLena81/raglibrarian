// Book/chapter/page CRUD service backed by PostgreSQL.
// Exposes a gRPC server.
// Called by: Query Service (enrichment), Lambdas (status updates), Re-index Scheduler.

module github.com/belLena81/raglibrarian/services/metadata

go 1.26

// ── gRPC server ──────────────────────────────────────────────────────────
require google.golang.org/grpc v1.70.0

require (
	go.opentelemetry.io/otel v1.34.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.34.0 // indirect
	golang.org/x/net v0.34.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250115164207-1a7da9e5054f // indirect
	google.golang.org/protobuf v1.36.5 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
	github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)
