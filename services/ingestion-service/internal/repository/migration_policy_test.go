package repository

import (
	"os"
	"strings"
	"testing"
)

func TestLifecycleMigrationGrantsOnlyDeletionCompletionColumns(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/002_ingestion_lifecycle.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	required := []string{
		"GRANT SELECT ( event_id, ack_event_id, ack_event_type, ack_payload, ack_occurred_at, completed_at ) ON ingestion.deletion_inbox TO ingestion_cleanup;",
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
		"GRANT SELECT ON ingestion.deletion_inbox",
		"GRANT UPDATE ON ingestion.artifact_sets",
		"GRANT UPDATE ON ingestion.jobs",
		"GRANT INSERT ON ingestion.outbox",
	} {
		if strings.Contains(normalized, broad) {
			t.Fatalf("broad cleanup-role grant found: %q", broad)
		}
	}
}

func TestLifecycleDownMigrationRemovesAcknowledgementsBeforeRestoringOutboxConstraint(t *testing.T) {
	contents, err := os.ReadFile("../../migrations/002_ingestion_lifecycle.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(contents)), " ")
	deleteAcknowledgement := "DELETE FROM ingestion.outbox WHERE event_type = 'ingestion.book.artifacts-deleted.v1';"
	restoreConstraint := "ALTER TABLE ingestion.outbox ADD CONSTRAINT outbox_event_type_check CHECK"
	deleteIndex := strings.Index(normalized, deleteAcknowledgement)
	restoreIndex := strings.Index(normalized, restoreConstraint)
	if deleteIndex < 0 {
		t.Fatalf("missing lifecycle acknowledgement cleanup statement %q", deleteAcknowledgement)
	}
	if restoreIndex < 0 {
		t.Fatalf("missing outbox constraint restore statement %q", restoreConstraint)
	}
	if deleteIndex > restoreIndex {
		t.Fatal("lifecycle acknowledgements must be removed before restoring the pre-lifecycle outbox constraint")
	}
}
