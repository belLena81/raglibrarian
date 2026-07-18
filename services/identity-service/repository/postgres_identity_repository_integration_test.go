//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

func TestPostgresIdentityRepositoryVerificationEnablesSecondRoleOnlyAfterConsumption(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	suffix := uuid.NewString()
	email := fmt.Sprintf("multiple-roles-%s@example.test", suffix)
	fingerprint := integrationFingerprint(suffix)
	verifiedAt := time.Now().UTC().Add(-time.Hour)
	readerID := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO identity.users
        (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at)
        VALUES ($1,'Reader',$2,$3,'reader-hash','reader','active',$4,$4)`, readerID, email, fingerprint, verifiedAt)
	require.NoError(t, err)

	registrationID := uuid.NewString()
	messageID := uuid.NewString()
	tokenHash := integrationFingerprint("token-" + suffix)
	now := time.Now().UTC()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.email_outbox WHERE id=$1`, messageID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.registration_verifications WHERE id=$1`, registrationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.users WHERE email=$1`, email)
	})

	repository := NewPostgresIdentityRepository(pool)
	require.NoError(t, repository.CreateOrIgnore(ctx, port.VerificationRegistration{
		ID: registrationID, TokenHash: tokenHash, Name: "Librarian", Email: email,
		EmailFingerprint: fingerprint, PasswordHash: "librarian-hash", Role: domain.RoleLibrarian,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}, port.SealedEmail{
		ID: messageID, MessageType: "verify_registration", KeyID: "key-v1",
		Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext"), CreatedAt: now,
	}))

	var librarians int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email=$1 AND role='librarian'`, email).Scan(&librarians))
	require.Zero(t, librarians)

	librarianID := uuid.NewString()
	user, err := repository.Consume(ctx, tokenHash, librarianID, now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, librarianID, user.ID())
	require.Equal(t, domain.StatusPending, user.Status())
	require.Equal(t, now.Add(time.Minute), user.VerifiedAt())

	var roles int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email=$1`, email).Scan(&roles))
	require.Equal(t, 2, roles)
}

func TestPostgresIdentityRepositoryRejectedLibrarianReappliesOnlyAfterVerification(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	suffix := uuid.NewString()
	email := fmt.Sprintf("reapplication-%s@example.test", suffix)
	fingerprint := integrationFingerprint(suffix)
	userID := uuid.NewString()
	verifiedAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Microsecond)
	_, err = pool.Exec(ctx, `INSERT INTO identity.users
        (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,reviewed_by,reviewed_at,created_at)
        VALUES ($1,'Rejected',$2,$3,'old-hash','librarian','rejected',$4,$5,$6,$4)`,
		userID, email, fingerprint, verifiedAt, uuid.NewString(), verifiedAt.Add(time.Hour))
	require.NoError(t, err)

	registrationID := uuid.NewString()
	messageID := uuid.NewString()
	tokenHash := integrationFingerprint("token-" + suffix)
	now := time.Now().UTC()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.email_outbox WHERE id=$1`, messageID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.registration_verifications WHERE id=$1`, registrationID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM identity.users WHERE id=$1`, userID)
	})

	repository := NewPostgresIdentityRepository(pool)
	require.NoError(t, repository.CreateOrIgnore(ctx, port.VerificationRegistration{
		ID: registrationID, TokenHash: tokenHash, Name: "Reverified", Email: email,
		EmailFingerprint: fingerprint, PasswordHash: "new-hash", Role: domain.RoleLibrarian,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}, port.SealedEmail{
		ID: messageID, MessageType: "verify_registration", KeyID: "key-v1",
		Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext"), CreatedAt: now,
	}))

	var statusBefore string
	require.NoError(t, pool.QueryRow(ctx, `SELECT status FROM identity.users WHERE id=$1`, userID).Scan(&statusBefore))
	require.Equal(t, string(domain.StatusRejected), statusBefore)

	user, err := repository.Consume(ctx, tokenHash, uuid.NewString(), now.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, userID, user.ID())
	require.Equal(t, "Reverified", user.Name())
	require.Equal(t, verifiedAt, user.VerifiedAt())
	require.Equal(t, domain.StatusPending, user.Status())

	var reviewedBy *string
	var persistedVerifiedAt time.Time
	require.NoError(t, pool.QueryRow(ctx, `SELECT reviewed_by,email_verified_at FROM identity.users WHERE id=$1`, userID).Scan(&reviewedBy, &persistedVerifiedAt))
	require.Nil(t, reviewedBy)
	require.Equal(t, verifiedAt, persistedVerifiedAt.UTC())
}

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

func TestPostgresIdentityRepositoryCleanupPasswordResetChallengesKeepsUsableChallenges(t *testing.T) {
	dsn := integrationDSN(t)
	if dsn == "" {
		t.Skip("IDENTITY_POSTGRES_DSN is required for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	now := time.Now().UTC().Truncate(time.Microsecond)
	fingerprints := [][]byte{
		integrationFingerprint("expired-" + uuid.NewString()),
		integrationFingerprint("consumed-" + uuid.NewString()),
		integrationFingerprint("active-code-" + uuid.NewString()),
		integrationFingerprint("active-grant-" + uuid.NewString()),
	}
	t.Cleanup(func() {
		for _, fingerprint := range fingerprints {
			_, _ = pool.Exec(context.Background(), `DELETE FROM identity.password_reset_challenges WHERE email_fingerprint=$1`, fingerprint)
		}
	})

	insertChallenge := func(fingerprint []byte, expiresAt time.Time, grantExpiresAt *time.Time, consumedAt *time.Time) {
		_, insertErr := pool.Exec(ctx, `
			INSERT INTO identity.password_reset_challenges
				(email_fingerprint, code_hash, expires_at, last_sent_at, grant_hash, grant_expires_at, consumed_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $4)
		`, fingerprint, integrationFingerprint(uuid.NewString()), expiresAt, now, integrationFingerprint(uuid.NewString()), grantExpiresAt, consumedAt)
		require.NoError(t, insertErr)
	}
	expired := now.Add(-time.Minute)
	active := now.Add(time.Minute)
	insertChallenge(fingerprints[0], expired, nil, nil)
	insertChallenge(fingerprints[1], active, &active, &expired)
	insertChallenge(fingerprints[2], active, &expired, nil)
	insertChallenge(fingerprints[3], expired, &active, nil)

	repository := NewPostgresIdentityRepository(pool)
	cleaned, err := repository.CleanupPasswordResetChallenges(ctx, now)
	require.NoError(t, err)
	require.Equal(t, int64(2), cleaned)

	for _, fingerprint := range fingerprints[:2] {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.password_reset_challenges WHERE email_fingerprint=$1`, fingerprint).Scan(&count))
		require.Zero(t, count)
	}
	for _, fingerprint := range fingerprints[2:] {
		var count int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM identity.password_reset_challenges WHERE email_fingerprint=$1`, fingerprint).Scan(&count))
		require.Equal(t, 1, count)
	}
}
