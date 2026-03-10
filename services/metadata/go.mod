// Book/chapter/page CRUD service backed by PostgreSQL.
// Exposes a gRPC server.
// Called by: Query Service (enrichment), Lambdas (status updates), Re-index Scheduler.

module github.com/belLena81/raglibrarian/services/metadata

go 1.26.0

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260309122639-6b9c9a70dd75
	github.com/jackc/pgx/v5 v5.8.0
	github.com/stretchr/testify v1.11.1
)

require (
	aidanwoods.dev/go-paseto v1.6.0 // indirect
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/domain => ../../pkg/domain
	github.com/belLena81/raglibrarian/services/query => ../query
)
