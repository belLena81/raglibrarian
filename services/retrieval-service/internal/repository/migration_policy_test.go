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

func readMigration(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path) // #nosec G304 -- fixed repository-owned migration fixture.
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
