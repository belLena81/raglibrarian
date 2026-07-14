//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPostgresSessionRepository_Rotate(t *testing.T) {
	dsn := os.Getenv("IDENTITY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
		userID,
		fmt.Sprintf("session-%s@example.test", userID),
		"integration-test-hash",
		"reader",
		time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	current := sha256.Sum256([]byte("current-refresh-token-" + userID))
	successor := sha256.Sum256([]byte("successor-refresh-token-" + userID))
	created, err := repository.Create(ctx, userID, time.Now().UTC().Add(time.Hour), current[:])
	require.NoError(t, err)

	rotated, err := repository.Rotate(ctx, current[:], successor[:], time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, created.ID, rotated.ID)
	require.Equal(t, userID, rotated.UserID)
}

func TestPostgresSessionRepository_CleanupExpiredCascadesRefreshTokens(t *testing.T) {
	dsn := os.Getenv("IDENTITY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role, created_at) VALUES ($1, $2, $3, $4, $5)`,
		userID, fmt.Sprintf("cleanup-%s@example.test", userID), "integration-test-hash", "reader", time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	token := sha256.Sum256([]byte("expired-refresh-token-" + userID))
	session, err := repository.Create(ctx, userID, time.Now().UTC().Add(-time.Minute), token[:])
	require.NoError(t, err)
	deleted, err := repository.CleanupExpired(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))

	var tokenCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.refresh_tokens WHERE session_id=$1`, session.ID).Scan(&tokenCount)
	require.NoError(t, err)
	require.Zero(t, tokenCount)
}
