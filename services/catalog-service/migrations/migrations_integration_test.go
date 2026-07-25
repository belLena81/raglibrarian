//go:build integration

package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestCatalogMigrationsRebuildCleanly(t *testing.T) {
	contents := readMigration(t, "001_catalog_schema.up.sql")

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS catalog.lifecycle_commands",
		"CREATE TABLE IF NOT EXISTS catalog.lifecycle_inbox",
		"CREATE TABLE IF NOT EXISTS catalog.processing_inbox",
		"books_tombstone_shape_check",
		"outbox_aggregate_sequence_idx",
		"catalog.book.uploaded.v1",
		"catalog.book.processing-status-changed.v1",
		"catalog.book.reindex-requested.v1",
		"catalog.book.deletion-requested.v1",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("catalog schema is missing %q", fragment)
		}
	}

	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("catalog schema still contains %q", forbidden)
		}
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name) // #nosec G304 -- fixed repository-owned migration fixture.
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
