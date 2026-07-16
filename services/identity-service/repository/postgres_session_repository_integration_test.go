//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

func TestPostgresSessionRepository_Rotate(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at) VALUES ($1,$2,$2,$3,$4,$5,'active',$6,$6)`,
		userID,
		fmt.Sprintf("session-%s@example.test", userID),
		integrationFingerprint(userID),
		"integration-test-hash",
		"reader",
		time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	current := sha256.Sum256([]byte("current-refresh-token-" + userID))
	successor := sha256.Sum256([]byte("successor-refresh-token-" + userID))
	now := time.Now().UTC()
	created := integrationSession(userID, now.Add(time.Hour))
	require.NoError(t, repository.Create(ctx, created, now, current[:]))

	var principal port.RefreshPrincipal
	err = repository.Rotate(ctx, current[:], successor[:], now, func(value port.RefreshPrincipal) error {
		principal = value
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, created.ID, principal.Session.ID)
	require.Equal(t, userID, principal.UserID)
	require.Contains(t, principal.Email, userID)
	require.Equal(t, "reader", string(principal.Role))
}

func TestPostgresSessionRepository_ReusedTokenRevokesFamily(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at) VALUES ($1,$2,$2,$3,$4,$5,'active',$6,$6)`,
		userID, fmt.Sprintf("reuse-%s@example.test", userID), integrationFingerprint(userID), "hash", "reader", time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	current := sha256.Sum256([]byte("reuse-current-" + userID))
	successor := sha256.Sum256([]byte("reuse-successor-" + userID))
	third := sha256.Sum256([]byte("reuse-third-" + userID))
	now := time.Now().UTC()
	session := integrationSession(userID, now.Add(time.Hour))
	require.NoError(t, repository.Create(ctx, session, now, current[:]))
	require.NoError(t, repository.Rotate(ctx, current[:], successor[:], now, func(port.RefreshPrincipal) error { return nil }))

	err = repository.Rotate(ctx, current[:], third[:], now.Add(time.Second), func(port.RefreshPrincipal) error { return nil })
	require.ErrorIs(t, err, port.ErrRefreshTokenReused)
	err = repository.Rotate(ctx, successor[:], third[:], now.Add(2*time.Second), func(port.RefreshPrincipal) error { return nil })
	require.ErrorIs(t, err, port.ErrRefreshTokenInvalid)

	var revoked bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM identity.sessions WHERE id=$1`, session.ID).Scan(&revoked))
	require.True(t, revoked)
}

func TestPostgresSessionRepository_ConcurrentRefreshAllowsOneThenRevokesFamily(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at) VALUES ($1,$2,$2,$3,$4,$5,'active',$6,$6)`,
		userID, fmt.Sprintf("concurrent-refresh-%s@example.test", userID), integrationFingerprint(userID), "hash", "reader", time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	current := sha256.Sum256([]byte("concurrent-current-" + userID))
	successors := [2][32]byte{
		sha256.Sum256([]byte("concurrent-successor-a-" + userID)),
		sha256.Sum256([]byte("concurrent-successor-b-" + userID)),
	}
	now := time.Now().UTC()
	session := integrationSession(userID, now.Add(time.Hour))
	require.NoError(t, repository.Create(ctx, session, now, current[:]))

	results := make([]error, 2)
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index] = repository.Rotate(ctx, current[:], successors[index][:], now, func(port.RefreshPrincipal) error { return nil })
		}(i)
	}
	wait.Wait()

	var successCount, reuseCount int
	for _, result := range results {
		switch {
		case result == nil:
			successCount++
		case errors.Is(result, port.ErrRefreshTokenReused):
			reuseCount++
		default:
			require.NoError(t, result)
		}
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, 1, reuseCount)

	var revoked bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM identity.sessions WHERE id=$1`, session.ID).Scan(&revoked))
	require.True(t, revoked)
}

func TestPostgresSessionRepository_PrepareFailureRollsBackRotation(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at) VALUES ($1,$2,$2,$3,$4,$5,'active',$6,$6)`,
		userID, fmt.Sprintf("rollback-%s@example.test", userID), integrationFingerprint(userID), "integration-test-hash", "reader", time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	current := sha256.Sum256([]byte("rollback-current-" + userID))
	successor := sha256.Sum256([]byte("rollback-successor-" + userID))
	now := time.Now().UTC()
	session := integrationSession(userID, now.Add(time.Hour))
	require.NoError(t, repository.Create(ctx, session, now, current[:]))
	prepareErr := fmt.Errorf("prepare failed")
	err = repository.Rotate(ctx, current[:], successor[:], now, func(port.RefreshPrincipal) error { return prepareErr })
	require.ErrorIs(t, err, prepareErr)

	var consumed bool
	err = pool.QueryRow(ctx, `SELECT consumed_at IS NOT NULL FROM identity.refresh_tokens WHERE session_id=$1 AND token_hash=$2`, session.ID, current[:]).Scan(&consumed)
	require.NoError(t, err)
	require.False(t, consumed)
	var successorCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.refresh_tokens WHERE token_hash=$1`, successor[:]).Scan(&successorCount)
	require.NoError(t, err)
	require.Zero(t, successorCount)

	err = repository.Rotate(ctx, current[:], successor[:], now, func(port.RefreshPrincipal) error { return nil })
	require.NoError(t, err)
}

func TestPostgresSessionRepository_CleanupExpiredCascadesRefreshTokens(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	userID := uuid.NewString()
	_, err = pool.Exec(ctx,
		`INSERT INTO identity.users (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at) VALUES ($1,$2,$2,$3,$4,$5,'active',$6,$6)`,
		userID, fmt.Sprintf("cleanup-%s@example.test", userID), integrationFingerprint(userID), "integration-test-hash", "reader", time.Now().UTC(),
	)
	require.NoError(t, err)

	repository := NewPostgresSessionRepository(pool)
	token := sha256.Sum256([]byte("expired-refresh-token-" + userID))
	now := time.Now().UTC()
	session := integrationSession(userID, now.Add(-time.Minute))
	require.NoError(t, repository.Create(ctx, session, now.Add(-time.Hour), token[:]))
	deleted, err := repository.CleanupExpired(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(1))

	var tokenCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.refresh_tokens WHERE session_id=$1`, session.ID).Scan(&tokenCount)
	require.NoError(t, err)
	require.Zero(t, tokenCount)
}

func integrationSession(userID string, expiresAt time.Time) port.Session {
	return port.Session{ID: uuid.NewString(), UserID: userID, FamilyID: uuid.NewString(), ExpiresAt: expiresAt.UTC()}
}

func integrationFingerprint(userID string) []byte {
	fingerprint := sha256.Sum256([]byte(userID))
	return fingerprint[:]
}
