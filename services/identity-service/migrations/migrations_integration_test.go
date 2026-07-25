//go:build integration

package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestIdentitySchemaIsCreateOnly(t *testing.T) {
	contents := readMigration(t, "001_identity_schema.up.sql")

	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS identity.password_reset_challenges",
		"CONSTRAINT users_email_role_unique UNIQUE (email, role)",
		"CONSTRAINT email_outbox_payload_check CHECK",
		"CREATE OR REPLACE FUNCTION identity.protect_user_review_fields()",
		"identity rejected librarian reapplication is invalid",
		"message_type IN ('verify_registration', 'password_reset_code')",
	} {
		if !strings.Contains(contents, fragment) {
			t.Fatalf("identity schema is missing %q", fragment)
		}
	}

	for _, forbidden := range []string{"ALTER TABLE", "DROP TABLE", "DROP INDEX"} {
		if strings.Contains(contents, forbidden) {
			t.Fatalf("identity schema still contains %q", forbidden)
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
