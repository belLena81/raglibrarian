// User-facing REST API + RAG orchestration service.
// Responsibilities: auth middleware, role enforcement, query pipeline,
// LLM synthesis, admin dashboard endpoints.

module github.com/belLena81/raglibrarian/services/query

go 1.26.0

// ── HTTP layer ──────────────────────────────────────────────────────────
require (
	github.com/go-chi/chi/v5 v5.2.5 // idiomatic stdlib-compatible router
	github.com/stretchr/testify v1.11.1
	go.uber.org/zap v1.27.1
)

require (
	github.com/belLena81/raglibrarian/pkg/auth v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/config v0.0.0-00010101000000-000000000000
	github.com/belLena81/raglibrarian/pkg/domain v0.0.0-20260309122639-6b9c9a70dd75
	github.com/belLena81/raglibrarian/pkg/logger v0.0.0-20260309122639-6b9c9a70dd75
	github.com/belLena81/raglibrarian/services/metadata v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.8.0
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
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/belLena81/raglibrarian/pkg/auth => ../../pkg/auth
	github.com/belLena81/raglibrarian/pkg/config => ../../pkg/config
	github.com/belLena81/raglibrarian/pkg/domain => ../../pkg/domain
	github.com/belLena81/raglibrarian/services/metadata => ../metadata
)
