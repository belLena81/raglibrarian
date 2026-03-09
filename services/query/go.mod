// User-facing REST API + RAG orchestration service.
// Responsibilities: auth middleware, role enforcement, query pipeline,
// LLM synthesis, admin dashboard endpoints.

module github.com/belLena81/raglibrarian/services/query

go 1.26

require (
    // ── HTTP layer ──────────────────────────────────────────────────────────
    github.com/go-chi/chi/v5                  v5.2.1    // idiomatic stdlib-compatible router
 )