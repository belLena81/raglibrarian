package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func TestBookLifecycleMigrationGrantsCompleteDeletionLeastPrivilege(t *testing.T) {
	up := readMigration(t, "007_book_lifecycle_remediation.up.sql")
	down := readMigration(t, "007_book_lifecycle_remediation.down.sql")
	policies := []struct {
		grant  string
		revoke string
	}{
		{
			grant:  "GRANT SELECT, INSERT, UPDATE ON retrieval.book_lifecycle TO retrieval_runtime;",
			revoke: "REVOKE SELECT, INSERT, UPDATE ON retrieval.book_lifecycle FROM retrieval_runtime;",
		},
		{
			grant:  "GRANT DELETE ON retrieval.manifest_facts, retrieval.index_jobs, retrieval.outbox TO retrieval_planner;",
			revoke: "REVOKE DELETE ON retrieval.manifest_facts, retrieval.index_jobs, retrieval.outbox FROM retrieval_planner;",
		},
		{
			grant:  "GRANT SELECT (book_id), UPDATE (title,author,publication_year,tags) ON retrieval.metadata_facts TO retrieval_cleanup;",
			revoke: "REVOKE SELECT (book_id), UPDATE (title,author,publication_year,tags) ON retrieval.metadata_facts FROM retrieval_cleanup;",
		},
		{
			grant:  "GRANT SELECT (book_id), DELETE ON retrieval.manifest_facts TO retrieval_cleanup;",
			revoke: "REVOKE SELECT (book_id), DELETE ON retrieval.manifest_facts FROM retrieval_cleanup;",
		},
		{
			grant:  "GRANT DELETE ON retrieval.index_jobs, retrieval.outbox TO retrieval_cleanup;",
			revoke: "REVOKE DELETE ON retrieval.index_jobs, retrieval.outbox FROM retrieval_cleanup;",
		},
		{
			grant:  "GRANT INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox TO retrieval_cleanup;",
			revoke: "REVOKE INSERT (event_id,event_type,aggregate_id,payload,occurred_at,next_attempt_at) ON retrieval.outbox FROM retrieval_cleanup;",
		},
		{
			grant:  "GRANT DELETE ON retrieval.index_jobs, retrieval.outbox TO retrieval_indexer;",
			revoke: "REVOKE DELETE ON retrieval.index_jobs, retrieval.outbox FROM retrieval_indexer;",
		},
	}
	for _, policy := range policies {
		if !strings.Contains(up, policy.grant) {
			t.Errorf("up migration is missing %q", policy.grant)
		}
		if !strings.Contains(down, policy.revoke) {
			t.Errorf("down migration is missing %q", policy.revoke)
		}
	}
	if !strings.Contains(up, "REVOKE DELETE ON retrieval.book_lifecycle FROM retrieval_runtime;") {
		t.Error("up migration leaves default runtime DELETE privilege on book_lifecycle")
	}
	for _, forbidden := range []string{
		"GRANT DELETE ON ALL TABLES IN SCHEMA retrieval TO retrieval_indexer;",
		"GRANT DELETE ON retrieval.book_lifecycle TO retrieval_indexer;",
		"GRANT DELETE ON retrieval.documents TO retrieval_indexer;",
		"GRANT DELETE ON retrieval.evidence TO retrieval_indexer;",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("up migration gives indexer unrelated delete privilege %q", forbidden)
		}
	}
}

func TestBookLifecycleRemediationPreservesAppliedMigrationChecksum(t *testing.T) {
	up := readMigration(t, "006_book_lifecycle.up.sql")
	digest := sha256.Sum256([]byte(up))
	if actual := hex.EncodeToString(digest[:]); actual != "60e9b3d3283a8c6780674541642143f2985faa998bd167331cdcbd5850184922" {
		t.Fatalf("006 up migration checksum = %s; applied migrations must remain immutable", actual)
	}
}

func TestBookLifecycleDownMigrationFailsClosedAndDeduplicates(t *testing.T) {
	up := readMigration(t, "007_book_lifecycle_remediation.up.sql")
	remediationDown := readMigration(t, "007_book_lifecycle_remediation.down.sql")
	down := readMigration(t, "006_book_lifecycle.down.sql")
	if !strings.Contains(up, "DROP CONSTRAINT IF EXISTS index_jobs_book_id_source_sha256_manifest_sha256_profile_di_key") {
		t.Error("remediation migration does not drop PostgreSQL's generated uniqueness constraint")
	}
	if !strings.Contains(up, "DROP CONSTRAINT IF EXISTS book_lifecycle_book_id_fkey") {
		t.Error("remediation migration keeps book_lifecycle dependent on metadata projection")
	}
	if strings.Contains(remediationDown, "DELETE FROM retrieval.book_lifecycle l") {
		t.Error("remediation rollback deletes orphan lifecycle fences instead of failing closed")
	}
	if !strings.Contains(remediationDown, "RAISE EXCEPTION 'cannot roll back retrieval lifecycle remediation while orphan lifecycle fences exist'") ||
		!strings.Contains(remediationDown, "ADD CONSTRAINT book_lifecycle_book_id_fkey") {
		t.Error("remediation rollback does not fail closed before restoring lifecycle metadata FK")
	}
	required := []string{
		"LOCK TABLE retrieval.book_lifecycle,",
		"IN ACCESS EXCLUSIVE MODE",
		"WHERE state IN ('reindexing','deleting')",
		"WHERE finalization_inflight",
		"WHERE state='processing'",
		"PARTITION BY j.book_id,j.source_sha256,j.manifest_sha256,j.profile_digest",
		"ORDER BY coalesce(l.active_job_id=j.id,false) DESC",
		"(j.state='indexed') DESC",
		"j.lifecycle_version DESC",
		"AND o.published_at IS NULL",
		"DELETE FROM retrieval.index_jobs j",
		"ADD CONSTRAINT index_jobs_book_id_source_sha256_manifest_sha256_profile_digest_key",
	}
	for _, fragment := range required {
		if !strings.Contains(down, fragment) {
			t.Errorf("down migration is missing safety policy %q", fragment)
		}
	}
	if strings.Index(down, "LOCK TABLE retrieval.book_lifecycle,") > strings.Index(down, "DO $$") {
		t.Error("down migration checks in-flight work before blocking lifecycle writers")
	}
	if strings.Index(down, "DELETE FROM retrieval.index_jobs j") >
		strings.Index(down, "ADD CONSTRAINT index_jobs_book_id_source_sha256_manifest_sha256_profile_digest_key") {
		t.Error("down migration restores uniqueness before removing duplicate generations")
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile("../../migrations/" + name) // #nosec G304 -- test reads a fixed repository-owned migration name.
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
