package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// ── Role value object ─────────────────────────────────────────────────────────

func TestRole_IsValid(t *testing.T) {
	assert.True(t, domain.RoleAdmin.IsValid())
	assert.True(t, domain.RoleReader.IsValid())
	assert.False(t, domain.Role("superuser").IsValid())
	assert.False(t, domain.Role("").IsValid())
}

func TestRole_CanWrite(t *testing.T) {
	assert.True(t, domain.RoleAdmin.CanWrite())
	assert.False(t, domain.RoleReader.CanWrite())
}

// ── NewUser ───────────────────────────────────────────────────────────────────

func TestNewUser_Valid(t *testing.T) {
	u, err := domain.NewUser("alice@example.com", "hashed-secret", domain.RoleAdmin)

	require.NoError(t, err)
	assert.NotEmpty(t, u.ID())
	assert.Equal(t, "alice@example.com", u.Email())
	assert.Equal(t, "hashed-secret", u.PasswordHash())
	assert.Equal(t, domain.RoleAdmin, u.Role())
	assert.WithinDuration(t, time.Now().UTC(), u.CreatedAt(), time.Second)
}

func TestNewUser_UniqueIDs(t *testing.T) {
	a, err := domain.NewUser("a@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)

	b, err := domain.NewUser("b@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)

	assert.NotEqual(t, a.ID(), b.ID())
}

func TestNewUser_InvalidEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr error
	}{
		{"empty", "", domain.ErrEmptyEmail},
		{"whitespace only", "   ", domain.ErrEmptyEmail},
		{"no at sign", "notanemail", domain.ErrInvalidEmail},
		{"leading at", "@example.com", domain.ErrInvalidEmail},
		{"trailing at", "alice@", domain.ErrInvalidEmail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewUser(tt.email, "hash", domain.RoleReader)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestNewUser_EmptyPasswordHash(t *testing.T) {
	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewUser("a@example.com", tt.hash, domain.RoleReader)
			assert.ErrorIs(t, err, domain.ErrEmptyPasswordHash)
		})
	}
}

func TestNewUser_InvalidRole(t *testing.T) {
	_, err := domain.NewUser("a@example.com", "hash", domain.Role("god"))
	assert.ErrorIs(t, err, domain.ErrInvalidRole)
}

func TestNewUserFromDB_ReconstructsWithoutValidation(t *testing.T) {
	ts := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	u := domain.NewUserFromDB("id-1", "a@b.com", "h", domain.RoleAdmin, ts)

	assert.Equal(t, "id-1", u.ID())
	assert.Equal(t, "a@b.com", u.Email())
	assert.Equal(t, "h", u.PasswordHash())
	assert.Equal(t, domain.RoleAdmin, u.Role())
	assert.Equal(t, ts, u.CreatedAt())
}
