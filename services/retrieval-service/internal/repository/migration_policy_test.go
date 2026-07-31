package repository

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

func TestRetrievalSummaryCacheMigrationAddsOnlyCacheTableIndexesAndGrant(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../migrations/003_retrieval_summary_cache.up.sql")), " ")

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS retrieval.summary_assessment_cache",
		"PRIMARY KEY (provider_profile, question_hash, passage_hash)",
		"CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_negative_idx",
		"CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_expiry_idx",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON retrieval.summary_assessment_cache TO retrieval_search;",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("retrieval summary cache migration is missing %q", fragment)
		}
	}

	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX", "TRUNCATE"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("retrieval summary cache migration contains %q", forbidden)
		}
	}
}

func TestRetrievalSummaryCacheHardeningMigrationAddsAccessAndFingerprintMetadata(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../migrations/004_retrieval_summary_cache_hardening.up.sql")), " ")

	for _, fragment := range []string{
		"BEGIN;",
		"ADD COLUMN IF NOT EXISTS topic_hash TEXT",
		"ADD COLUMN IF NOT EXISTS guard_hash TEXT",
		"ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now()",
		"ADD COLUMN IF NOT EXISTS hit_count BIGINT NOT NULL DEFAULT 0",
		"ALTER COLUMN query_embedding DROP NOT NULL",
		"DROP COLUMN IF EXISTS topic_tokens",
		"DROP COLUMN IF EXISTS guard_tokens",
		"DELETE FROM retrieval.summary_assessment_cache WHERE EXISTS",
		"FROM information_schema.columns",
		"column_name IN ('topic_tokens','guard_tokens')",
		"IF NOT EXISTS ( SELECT 1 FROM pg_constraint",
		"CHECK (query_embedding IS NULL OR octet_length(query_embedding) > 0)",
		"CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_negative_v2_idx",
		"CREATE INDEX IF NOT EXISTS retrieval_summary_assessment_cache_eviction_idx",
		"COMMIT;",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("retrieval summary cache hardening migration is missing %q", fragment)
		}
	}
	if strings.Contains(contents, "DELETE FROM retrieval.summary_assessment_cache;") {
		t.Fatal("retrieval summary cache hardening migration unconditionally purges hardened rows")
	}
}

func TestPostgresBootstrapRunsRetrievalLexicalSearchMigration(t *testing.T) {
	contents := strings.Join(strings.Fields(readMigration(t, "../../../../infra/postgres/bootstrap.sql")), " ")

	for _, fragment := range []string{
		"\\connect retrieval",
		"\\ir /schema/retrieval/001_retrieval_schema.up.sql",
		"\\ir /schema/retrieval/002_retrieval_lexical_search.up.sql",
		"\\ir /schema/retrieval/003_retrieval_summary_cache.up.sql",
		"\\ir /schema/retrieval/004_retrieval_summary_cache_hardening.up.sql",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("postgres bootstrap is missing %q", fragment)
		}
	}
}

func TestPostgresBootstrapRevisionTracksHighestRetrievalMigration(t *testing.T) {
	entries, err := os.ReadDir("../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	migrationPattern := regexp.MustCompile(`^([0-9]{3})_.*\.up\.sql$`)
	versions := make([]string, 0, len(entries))
	for _, entry := range entries {
		if match := migrationPattern.FindStringSubmatch(entry.Name()); match != nil {
			versions = append(versions, match[1])
		}
	}
	if len(versions) == 0 {
		t.Fatal("no retrieval migrations found")
	}
	sort.Strings(versions)
	expectedLabel := `io.raglibrarian.bootstrap-revision: "retrieval-` + versions[len(versions)-1] + `"`
	compose := readMigration(t, filepath.Clean("../../../../docker-compose.yml"))
	if !strings.Contains(compose, expectedLabel) {
		t.Fatalf("db-bootstrap label must track highest retrieval migration: missing %q", expectedLabel)
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
