//go:build integration

package repository

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

func TestPostgresRegistrationRepository_RollsBackUserWhenTokenInsertFails(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	existingUserID := uuid.NewString()
	_, err := pool.Exec(ctx,
		`INSERT INTO identity.users (id, email, password_hash, role, created_at) VALUES ($1,$2,$3,$4,$5)`,
		existingUserID, fmt.Sprintf("existing-%s@example.test", existingUserID), "hash", "reader", now,
	)
	require.NoError(t, err)
	collision := sha256.Sum256([]byte("registration-collision-" + existingUserID))
	existingSession := integrationSession(existingUserID, now.Add(time.Hour))
	err = NewPostgresSessionRepository(pool).Create(ctx, existingSession, now, collision[:])
	require.NoError(t, err)

	newEmail := fmt.Sprintf("new-%s@example.test", uuid.NewString())
	user, err := domain.NewUser(newEmail, "hash", domain.RoleReader)
	require.NoError(t, err)
	session := integrationSession(user.ID(), now.Add(time.Hour))
	err = NewPostgresRegistrationRepository(pool).CreateRegistration(ctx, port.Registration{User: user, Session: session, CreatedAt: now, RefreshTokenHash: collision[:]})
	require.Error(t, err)
	require.False(t, errors.Is(err, domain.ErrEmailTaken))

	var userCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE id=$1`, user.ID()).Scan(&userCount)
	require.NoError(t, err)
	require.Zero(t, userCount)
	var sessionCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions WHERE user_id=$1`, user.ID()).Scan(&sessionCount)
	require.NoError(t, err)
	require.Zero(t, sessionCount)
}

func TestPostgresRegistrationRepository_CreatesCompleteRegistration(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user, err := domain.NewUser(fmt.Sprintf("complete-%s@example.test", uuid.NewString()), "hash", domain.RoleReader)
	require.NoError(t, err)
	tokenHash := sha256.Sum256([]byte("complete-registration-" + user.ID()))
	session := integrationSession(user.ID(), now.Add(time.Hour))

	err = NewPostgresRegistrationRepository(pool).CreateRegistration(ctx, port.Registration{User: user, Session: session, CreatedAt: now, RefreshTokenHash: tokenHash[:]})
	require.NoError(t, err)
	require.Equal(t, user.ID(), session.UserID)

	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM identity.refresh_tokens t JOIN identity.sessions s ON s.id=t.session_id JOIN identity.users u ON u.id=s.user_id WHERE u.id=$1 AND t.token_hash=$2`, user.ID(), tokenHash[:]).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestPostgresRegistrationRepository_ConcurrentEmailCreatesOneCompleteAccount(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	email := fmt.Sprintf("concurrent-%s@example.test", uuid.NewString())
	repository := NewPostgresRegistrationRepository(pool)
	errorsByAttempt := make([]error, 2)
	users := make([]domain.User, 2)
	var wait sync.WaitGroup
	for i := range users {
		user, err := domain.NewUser(email, "hash", domain.RoleReader)
		require.NoError(t, err)
		users[i] = user
		tokenHash := sha256.Sum256([]byte(fmt.Sprintf("concurrent-token-%d-%s", i, user.ID())))
		wait.Add(1)
		go func(index int, candidate domain.User, hash []byte) {
			defer wait.Done()
			errorsByAttempt[index] = repository.CreateRegistration(ctx, port.Registration{
				User: candidate, Session: integrationSession(candidate.ID(), now.Add(time.Hour)), CreatedAt: now, RefreshTokenHash: hash,
			})
		}(i, user, tokenHash[:])
	}
	wait.Wait()

	var successCount, conflictCount int
	for _, err := range errorsByAttempt {
		switch {
		case err == nil:
			successCount++
		case errors.Is(err, domain.ErrEmailTaken):
			conflictCount++
		default:
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successCount)
	require.Equal(t, 1, conflictCount)

	var usersCount, sessionsCount, tokensCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email=$1`, email).Scan(&usersCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id WHERE u.email=$1`, email).Scan(&sessionsCount))
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.refresh_tokens t JOIN identity.sessions s ON s.id=t.session_id JOIN identity.users u ON u.id=s.user_id WHERE u.email=$1`, email).Scan(&tokensCount))
	require.Equal(t, 1, usersCount)
	require.Equal(t, 1, sessionsCount)
	require.Equal(t, 1, tokensCount)
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("IDENTITY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}
