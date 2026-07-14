// Book/chapter/page CRUD service backed by PostgreSQL.
// Exposes a gRPC server.
// Called by: Query Service (enrichment), Lambdas (status updates), Re-index Scheduler.

module github.com/belLena81/raglibrarian/services/identity-service

go 1.26.5

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260310060144-d8455ee5d7b2
	github.com/belLena81/raglibrarian/pkg/logger v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/proto v0.0.0-00010101000000-000000000000
	github.com/google/uuid v1.6.0
	github.com/jackc/pgx/v5 v5.9.2
	github.com/stretchr/testify v1.11.1
	go.uber.org/zap v1.27.1
	google.golang.org/grpc v1.79.3
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20251202230838-ff82c1b0f217 // indirect
	google.golang.org/protobuf v1.36.10 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/config => ../../pkg/config
	github.com/belLena81/raglibrarian/pkg/domain => ../../pkg/domain
	github.com/belLena81/raglibrarian/pkg/logger => ../../pkg/logger
	github.com/belLena81/raglibrarian/pkg/proto => ../../pkg/proto
)
