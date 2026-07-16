//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPostgresIdentityRepositoryCleanupRejectedErasesIdentity(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	reviewedAt := time.Now().UTC().Add(-time.Hour)
	_, err = pool.Exec(ctx, `
		INSERT INTO identity.users
			(id, display_name, email, email_fingerprint, password_hash, role, status,
			 email_verified_at, reviewed_by, reviewed_at, created_at)
		VALUES ($1, 'Rejected librarian', $2, $3, 'hash', 'librarian', 'rejected',
			$4, $5, $4, $4)
	`, userID, "rejected-"+userID+"@example.test", integrationFingerprint(userID), reviewedAt, uuid.NewString())
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM identity.users WHERE id=$1`, userID) })

	repository := NewPostgresIdentityRepository(pool)
	cleaned, err := repository.CleanupRejected(ctx, reviewedAt.Add(time.Minute))
	require.NoError(t, err)
	require.GreaterOrEqual(t, cleaned, int64(1))

	var identityFields int
	err = pool.QueryRow(ctx, `
		SELECT num_nonnulls(display_name, email, email_fingerprint, password_hash)
		FROM identity.users WHERE id=$1
	`, userID).Scan(&identityFields)
	require.NoError(t, err)
	require.Zero(t, identityFields)
}
