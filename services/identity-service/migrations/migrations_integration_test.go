//go:build integration

package migrations_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUserEmailsMigration(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx)) })

	_, err = tx.Exec(ctx, `ALTER TABLE identity.users DROP CONSTRAINT IF EXISTS users_email_canonical_check`)
	require.NoError(t, err)
	userID := uuid.NewString()
	legacyEmail := fmt.Sprintf("  LEGACY-%s@Example.TEST  ", userID)
	_, err = tx.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role) VALUES ($1, $2, 'hash', 'reader')`,
		userID,
		legacyEmail,
	)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, readMigration(t, "003_normalize_user_emails.up.sql"))
	require.NoError(t, err)

	var storedEmail string
	err = tx.QueryRow(ctx, `SELECT email FROM identity.users WHERE id=$1`, userID).Scan(&storedEmail)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("legacy-%s@example.test", userID), storedEmail)

	_, err = tx.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role) VALUES ($1, $2, 'hash', 'reader')`,
		uuid.NewString(),
		"NOT-CANONICAL@example.test",
	)
	require.Error(t, err)
}

func TestNormalizeUserEmailsMigrationRejectsCollisions(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx)) })

	_, err = tx.Exec(ctx, `ALTER TABLE identity.users DROP CONSTRAINT IF EXISTS users_email_canonical_check`)
	require.NoError(t, err)
	suffix := uuid.NewString()
	_, err = tx.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role) VALUES ($1, $2, 'hash', 'reader'), ($3, $4, 'hash', 'reader')`,
		uuid.NewString(), fmt.Sprintf("COLLISION-%s@example.test", suffix),
		uuid.NewString(), fmt.Sprintf(" collision-%s@example.test ", suffix),
	)
	require.NoError(t, err)

	_, err = tx.Exec(ctx, readMigration(t, "003_normalize_user_emails.up.sql"))
	require.ErrorContains(t, err, "identity email normalization has canonical collisions")
}

func TestIdentityRollbackDropsSchemaQualifiedUsersTable(t *testing.T) {
	pool := migrationPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tx.Rollback(ctx)) })

	for _, name := range []string{
		"003_normalize_user_emails.down.sql",
		"002_create_sessions.down.sql",
		"001_create_users.down.sql",
	} {
		_, err = tx.Exec(ctx, readMigration(t, name))
		require.NoError(t, err)
	}

	var usersTable *string
	err = tx.QueryRow(ctx, `SELECT to_regclass('identity.users')::text`).Scan(&usersTable)
	require.NoError(t, err)
	assert.Nil(t, usersTable)
}

func migrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IDENTITY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
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
	contents, err := os.ReadFile(name) // #nosec G304 -- fixed test fixture names only
	require.NoError(t, err)
	return string(contents)
}
