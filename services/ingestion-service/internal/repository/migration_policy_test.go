package repository

import (
	"os"
	"strings"
	"testing"
)

func TestBootstrapSchemaIncludesLifecycleTablesAndGrants(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/001_ingestion_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	required := []string{
		"CREATE TABLE IF NOT EXISTS ingestion.lifecycle_fences",
		"CREATE TABLE IF NOT EXISTS ingestion.deletion_inbox",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA ingestion TO ingestion_runtime;",
		"GRANT SELECT ON ingestion.artifact_sets TO ingestion_cleanup;",
		"GRANT UPDATE ( manifest_reference, manifest_sha256, deletion_cleanup_completed_at ) ON ingestion.artifact_sets TO ingestion_cleanup;",
		"GRANT UPDATE ( manifest_reference, manifest_sha256, manifest_byte_size, updated_at ) ON ingestion.jobs TO ingestion_cleanup;",
		"GRANT SELECT (id) ON ingestion.jobs TO ingestion_cleanup;",
		"GRANT INSERT ( event_id, event_type, aggregate_id, aggregate_sequence, payload, occurred_at, next_attempt_at ) ON ingestion.outbox TO ingestion_cleanup;",
	}
	for _, statement := range required {
		if !strings.Contains(normalized, statement) {
			t.Fatalf("missing least-privilege grant %q", statement)
		}
	}
	for _, broad := range []string{
		"ALTER TABLE",
		"DROP TABLE",
		"DROP INDEX",
	} {
		if strings.Contains(normalized, broad) {
			t.Fatalf("bootstrap schema should stay create-only, found %q", broad)
		}
	}
}

func TestBootstrapSchemaRemainsCreateOnly(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/001_ingestion_schema.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("bootstrap schema should stay create-only, found %q", forbidden)
		}
	}
}

func TestContentSelectionUpgradeMigrationIsAdditiveAndBounded(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/002_content_selection.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS ingestion.content_selection_inbox",
		"octet_length(payload) BETWEEN 1 AND 262144",
		"state IN ('queued','awaiting_selection','processing','retrying','completed','failed')",
		"aggregate_sequence IN (1,2,3)",
		"ingestion.book.content-selection-requested.v1",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("missing content-selection migration clause %q", required)
		}
	}
	for _, forbidden := range []string{"TRUNCATE", "DELETE FROM", "DROP TABLE"} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("upgrade migration mutates existing data with %q", forbidden)
		}
	}
}

func TestPostgresBootstrapAppliesContentSelectionUpgrade(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/bootstrap.sql")
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := string(contents)
	base := strings.Index(bootstrap, `\ir 001_ingestion_schema.up.sql`)
	upgrade := strings.Index(bootstrap, `\ir 002_content_selection.up.sql`)
	if base < 0 || upgrade <= base {
		t.Fatal("content-selection migration is not applied after the ingestion base schema")
	}
}
