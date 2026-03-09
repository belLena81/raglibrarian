// User-facing REST API + RAG orchestration service.
// Responsibilities: auth middleware, role enforcement, query pipeline,
// LLM synthesis, admin dashboard endpoints.

module github.com/belLena81/raglibrarian/services/query

go 1.26

// ── HTTP layer ──────────────────────────────────────────────────────────
require (
	github.com/go-chi/chi/v5 v5.2.5 // idiomatic stdlib-compatible router
	github.com/stretchr/testify v1.9.0
	go.uber.org/zap v1.27.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
