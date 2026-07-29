package repository

import (
	"os"
	"strings"
	"testing"
)

func TestRetrievalSchemaIsCreateOnly(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../migrations/001_retrieval_schema.up.sql")), " ")

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS retrieval.book_lifecycle",
		"CREATE INDEX IF NOT EXISTS retrieval_book_lifecycle_cleanup_idx",
		"CREATE INDEX IF NOT EXISTS retrieval_index_jobs_vector_cleanup_idx",
		"GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_runtime;",
		"GRANT SELECT, UPDATE ON retrieval.index_batches, retrieval.outbox TO retrieval_cleanup;",
		"GRANT INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox TO retrieval_cleanup;",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("retrieval schema is missing %q", fragment)
		}
	}

	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("retrieval schema still contains %q", forbidden)
		}
	}
}

func TestRetrievalLexicalSearchMigrationAddsOnlyIndexAndGrant(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../migrations/002_retrieval_lexical_search.up.sql")), " ")

	for _, fragment := range []string{
		"CREATE INDEX IF NOT EXISTS retrieval_evidence_lexical_search_idx",
		"USING GIN ( to_tsvector('simple', title || ' ' || author || ' ' || chapter || ' ' || section || ' ' || passage) )",
		"GRANT SELECT ON retrieval.evidence TO retrieval_search;",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("retrieval lexical search migration is missing %q", fragment)
		}
	}

	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX", "DELETE FROM", "TRUNCATE"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("retrieval lexical search migration contains %q", forbidden)
		}
	}
}

func TestPostgresBootstrapRunsRetrievalLexicalSearchMigration(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../../../infra/postgres/bootstrap.sql")), " ")

	for _, fragment := range []string{
		"\\connect retrieval",
		"\\ir /schema/retrieval/001_retrieval_schema.up.sql",
		"\\ir /schema/retrieval/002_retrieval_lexical_search.up.sql",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("postgres bootstrap is missing %q", fragment)
		}
	}
}

func readMigration(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path) // #nosec G304 -- fixed repository-owned migration fixture.
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
