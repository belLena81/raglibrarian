// User-facing REST API + RAG orchestration service.
// Responsibilities: auth middleware, role enforcement, query pipeline,
// LLM synthesis, admin dashboard endpoints.

module github.com/belLena81/raglibrarian/services/query

go 1.26

require (
	aidanwoods.dev/go-paseto                        v1.5.4
	github.com/go-chi/chi/v5                        v5.2.1
	github.com/google/uuid                          v1.6.0
	github.com/jackc/pgx/v5                         v5.7.2
	github.com/stretchr/testify                     v1.10.0
	github.com/belLena81/raglibrarian/pkg/proto      v0.0.0
	github.com/belLena81/raglibrarian/pkg/tokenverifier v0.0.0
	golang.org/x/crypto                             v0.36.0
	google.golang.org/grpc                          v1.70.0
)

replace (
    github.com/belLena81/raglibrarian/pkg/proto     => ../../pkg/proto
    github.com/belLena81/raglibrarian/pkg/events    => ../../pkg/events
    github.com/belLena81/raglibrarian/pkg/telemetry => ../../pkg/telemetry
)