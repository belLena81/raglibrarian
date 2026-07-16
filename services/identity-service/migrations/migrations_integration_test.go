//go:build integration

package migrations_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	initialSchemaUp   = "001_identity_schema.up.sql"
	initialSchemaDown = "001_identity_schema.down.sql"
)

func TestInitialSchemaRebuildsCleanlyAndEnforcesIdentityInvariants(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx)) })
	downSQL := readMigration(t, initialSchemaDown)
	upSQL := readMigration(t, initialSchemaUp)

	_, err = tx.Exec(ctx, downSQL)
	require.NoError(t, err)
	assertIdentityTablesAbsent(t, tx)

	_, err = tx.Exec(ctx, upSQL)
	require.NoError(t, err)
	assertFinalSchemaObjects(t, tx)
	assertFinalSchemaConstraints(t, tx)

	_, err = tx.Exec(ctx, downSQL)
	require.NoError(t, err)
	assertIdentityTablesAbsent(t, tx)

	_, err = tx.Exec(ctx, upSQL)
	require.NoError(t, err)
	assertFinalSchemaObjects(t, tx)

	var users int
	require.NoError(t, tx.QueryRow(ctx, `SELECT count(*) FROM identity.users`).Scan(&users))
	require.Zero(t, users)
}

func assertFinalSchemaObjects(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx := context.Background()

	for _, table := range []string{
		"users",
		"sessions",
		"refresh_tokens",
		"registration_verifications",
		"email_outbox",
	} {
		var qualifiedName *string
		err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, "identity."+table).Scan(&qualifiedName)
		require.NoError(t, err)
		require.NotNil(t, qualifiedName, table)
	}

	for _, index := range []string{
		"users_single_admin_idx",
		"users_email_fingerprint_idx",
		"users_pending_librarians_idx",
		"sessions_active_user_idx",
		"sessions_family_idx",
		"refresh_tokens_active_hash_idx",
		"registration_verifications_pending_email_idx",
		"registration_verifications_expiry_idx",
		"email_outbox_delivery_idx",
	} {
		var qualifiedName *string
		err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, "identity."+index).Scan(&qualifiedName)
		require.NoError(t, err)
		require.NotNil(t, qualifiedName, index)
	}

	var triggerCount int
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger
		WHERE tgrelid = 'identity.users'::regclass
		  AND NOT tgisinternal
		  AND tgname IN ('protect_user_review_fields', 'notify_pending_librarians_changed')
	`).Scan(&triggerCount)
	require.NoError(t, err)
	require.Equal(t, 2, triggerCount)
}

func assertFinalSchemaConstraints(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	adminID := uuid.NewString()
	pendingID := uuid.NewString()

	_, err := tx.Exec(ctx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status, email_verified_at, created_at)
		VALUES ($1, 'Administrator', $2, $3, 'hash', 'admin', 'active', $4, $4)
	`, adminID, "admin-"+adminID+"@example.test", fingerprint(1), now)
	require.NoError(t, err)

	err = rejectedByConstraint(t, tx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status, email_verified_at, created_at)
		VALUES ($1, 'Second administrator', $2, $3, 'hash', 'admin', 'active', $4, $4)
	`, uuid.NewString(), "admin-"+uuid.NewString()+"@example.test", fingerprint(2), now)
	require.Error(t, err, "the schema must allow exactly one administrator")

	err = rejectedByConstraint(t, tx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status, email_verified_at, created_at)
		VALUES ($1, 'Reader', 'NOT-CANONICAL@example.test', $2, 'hash', 'reader', 'active', $3, $3)
	`, uuid.NewString(), fingerprint(3), now)
	require.Error(t, err, "email addresses must be canonical")

	err = rejectedByConstraint(t, tx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status, email_verified_at, created_at)
		VALUES ($1, 'Reader', $2, $3, 'hash', 'reader', 'active', $4, $4)
	`, uuid.NewString(), "reader-"+uuid.NewString()+"@example.test", []byte("short"), now)
	require.Error(t, err, "email fingerprints must be exactly 32 bytes")

	_, err = tx.Exec(ctx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status, email_verified_at, created_at)
		VALUES ($1, 'Pending librarian', $2, $3, 'hash', 'librarian', 'pending', $4, $4)
	`, pendingID, "librarian-"+pendingID+"@example.test", fingerprint(4), now)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, `
		UPDATE identity.users
		SET status = 'active', reviewed_by = $1, reviewed_at = $2
		WHERE id = $3
	`, adminID, now, pendingID)
	require.NoError(t, err)

	err = rejectedByConstraint(t, tx, `
		UPDATE identity.users
		SET reviewed_at = $1
		WHERE id = $2
	`, now.Add(time.Second), pendingID)
	require.ErrorContains(t, err, "identity review fields are immutable")

	err = rejectedByConstraint(t, tx, `
		INSERT INTO identity.registration_verifications
			(id, token_hash, display_name, email, email_fingerprint, password_hash, role,
			 expires_at, last_sent_at, created_at)
		VALUES ($1, $2, 'Invalid role', $3, $4, 'hash', 'admin', $5, $6, $6)
	`, uuid.New(), fingerprint(5), "verification-"+uuid.NewString()+"@example.test", fingerprint(6), now.Add(time.Hour), now)
	require.Error(t, err, "public registration must not create administrators")

	err = rejectedByConstraint(t, tx, `
		INSERT INTO identity.email_outbox
			(id, message_type, key_id, nonce, ciphertext, next_attempt_at, created_at)
		VALUES ($1, 'verify_registration', 'key-1', NULL, NULL, $2, $2)
	`, uuid.New(), now)
	require.Error(t, err, "undelivered outbox messages must retain their encrypted payload")
}

func rejectedByConstraint(t *testing.T, tx pgx.Tx, query string, arguments ...any) error {
	t.Helper()
	ctx := context.Background()
	_, err := tx.Exec(ctx, "SAVEPOINT expected_constraint_failure")
	require.NoError(t, err)

	_, rejectedErr := tx.Exec(ctx, query, arguments...)
	require.Error(t, rejectedErr)

	_, err = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT expected_constraint_failure")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "RELEASE SAVEPOINT expected_constraint_failure")
	require.NoError(t, err)
	return rejectedErr
}

func assertIdentityTablesAbsent(t *testing.T, tx pgx.Tx) {
	t.Helper()
	ctx := context.Background()

	for _, table := range []string{
		"users",
		"sessions",
		"refresh_tokens",
		"registration_verifications",
		"email_outbox",
	} {
		var qualifiedName *string
		err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, "identity."+table).Scan(&qualifiedName)
		require.NoError(t, err)
		require.Nil(t, qualifiedName, table)
	}
}

func fingerprint(seed byte) []byte {
	value := make([]byte, 32)
	for index := range value {
		value[index] = seed
	}
	return value
}

func migrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IDENTITY_MIGRATION_POSTGRES_DSN")
	if path := os.Getenv("IDENTITY_MIGRATION_POSTGRES_DSN_FILE"); path != "" {
		contents, err := os.ReadFile(path) // #nosec G304 -- test-only configured migration-owner secret path.
		require.NoError(t, err)
		dsn = strings.TrimSpace(string(contents))
	}
	if dsn == "" {
		t.Skip("IDENTITY_MIGRATION_POSTGRES_DSN is required for integration tests")
	}
	config, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name) // #nosec G304 -- callers pass fixed migration fixture names only.
	require.NoError(t, err)
	return string(contents)
}
