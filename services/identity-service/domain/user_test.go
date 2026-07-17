package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

func TestNewVerifiedUserBuildsActiveReader(t *testing.T) {
	now := time.Now().UTC()
	user, err := domain.NewVerifiedUser(
		"reader-1", "Reader", "reader@example.com", make([]byte, 32), "hash",
		domain.RoleReader, domain.StatusActive, now, now,
	)
	require.NoError(t, err)
	assert.Equal(t, "reader-1", user.ID())
	assert.Equal(t, domain.RoleReader, user.Role())
}

func TestNewUnverifiedUserDoesNotAssertEmailOwnership(t *testing.T) {
	now := time.Now().UTC()
	user, err := domain.NewUnverifiedUser(
		"reader-1", "Reader", "reader@example.com", make([]byte, 32), "hash",
		domain.RoleReader, domain.StatusActive, now,
	)
	require.NoError(t, err)
	assert.True(t, user.VerifiedAt().IsZero())
	assert.True(t, user.CanAuthenticate())
}

func TestPendingLibrarianHasOneFinalDecision(t *testing.T) {
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	user, err := domain.NewVerifiedUser(
		"librarian-1", "Librarian", "librarian@example.test", make([]byte, 32), "hash",
		domain.RoleLibrarian, domain.StatusPending, now, now,
	)
	require.NoError(t, err)
	require.NoError(t, user.Approve("admin-1", now.Add(time.Minute)))
	assert.Equal(t, domain.StatusActive, user.Status())
	assert.Equal(t, "admin-1", user.ReviewedBy())
	assert.ErrorIs(t, user.Reject("admin-2", now.Add(2*time.Minute)), domain.ErrInvalidTransition)
}

func TestReaderAndAdminCannotBePending(t *testing.T) {
	now := time.Now().UTC()
	for _, role := range []domain.Role{domain.RoleReader, domain.RoleAdmin} {
		_, err := domain.NewVerifiedUser("user-1", "User", "user@example.test", nil, "hash", role, domain.StatusPending, now, now)
		assert.ErrorIs(t, err, domain.ErrInvalidStatus)
	}
}

func TestNewVerifiedUserRejectsInvalidState(t *testing.T) {
	now := time.Now().UTC()
	_, err := domain.NewVerifiedUser("reader-1", "Reader", "invalid", nil, "hash", domain.RoleReader, domain.StatusActive, now, now)
	assert.ErrorIs(t, err, domain.ErrInvalidEmail)
	_, err = domain.NewVerifiedUser("reader-1", "Reader", "a@example.com", nil, "", domain.RoleReader, domain.StatusActive, now, now)
	assert.ErrorIs(t, err, domain.ErrEmptyPasswordHash)
	_, err = domain.NewVerifiedUser("reader-1", "Reader", "a@example.com", nil, "hash", domain.Role("owner"), domain.StatusActive, now, now)
	assert.ErrorIs(t, err, domain.ErrInvalidRole)
}
