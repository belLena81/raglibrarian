package user_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
)

func validUser(t *testing.T) *user.User {
	t.Helper()
	u, err := user.NewUser("Ada Lovelace", "ada@example.com", "$2a$12$hash", user.RoleReader)
	require.NoError(t, err)
	return u
}

// ── NewUser ───────────────────────────────────────────────────────────────────

func TestNewUser_Valid(t *testing.T) {
	u, err := user.NewUser("Ada Lovelace", "ada@example.com", "$2a$12$hash", user.RoleReader)
	require.NoError(t, err)
	assert.NotEmpty(t, u.ID())
	assert.Equal(t, "Ada Lovelace", u.Name())
	assert.Equal(t, "ada@example.com", u.Email())
	assert.Equal(t, user.RoleReader, u.Role())
	assert.False(t, u.CreatedAt().IsZero())
}

func TestNewUser_TrimsAndLowercases(t *testing.T) {
	u, err := user.NewUser("  Ada  ", "  ADA@EXAMPLE.COM  ", "$2a$12$hash", user.RoleReader)
	require.NoError(t, err)
	assert.Equal(t, "Ada", u.Name())
	assert.Equal(t, "ada@example.com", u.Email())
}

func TestNewUser_EmptyName(t *testing.T) {
	_, err := user.NewUser("", "ada@example.com", "$2a$12$hash", user.RoleReader)
	assert.ErrorIs(t, err, user.ErrEmptyName)
}

func TestNewUser_NameTooLong(t *testing.T) {
	_, err := user.NewUser(strings.Repeat("a", 101), "ada@example.com", "$2a$12$hash", user.RoleReader)
	assert.ErrorIs(t, err, user.ErrNameTooLong)
}

func TestNewUser_InvalidEmail(t *testing.T) {
	for _, e := range []string{"notanemail", "missing@", "@nodomain.com"} {
		_, err := user.NewUser("Ada", e, "$2a$12$hash", user.RoleReader)
		assert.ErrorIs(t, err, user.ErrInvalidEmail, "email: %s", e)
	}
}

func TestNewUser_EmptyPasswordHash(t *testing.T) {
	_, err := user.NewUser("Ada", "ada@example.com", "", user.RoleReader)
	assert.ErrorIs(t, err, user.ErrEmptyPassword)
}

func TestNewUser_InvalidRole(t *testing.T) {
	_, err := user.NewUser("Ada", "ada@example.com", "$2a$12$hash", user.Role("superuser"))
	assert.ErrorIs(t, err, user.ErrInvalidRole)
}

// ── Setters ───────────────────────────────────────────────────────────────────

func TestSetName_UpdatesTimestamp(t *testing.T) {
	u := validUser(t)
	before := u.UpdatedAt()
	time.Sleep(time.Millisecond)
	require.NoError(t, u.SetName("Grace Hopper"))
	assert.Equal(t, "Grace Hopper", u.Name())
	assert.True(t, u.UpdatedAt().After(before))
}

func TestSetName_EmptyRejected(t *testing.T) {
	u := validUser(t)
	assert.ErrorIs(t, u.SetName(""), user.ErrEmptyName)
	assert.Equal(t, "Ada Lovelace", u.Name()) // unchanged
}

func TestSetRole_AllValid(t *testing.T) {
	for _, r := range []user.Role{user.RoleReader, user.RoleLibrarian, user.RoleAdmin} {
		u := validUser(t)
		assert.NoError(t, u.SetRole(r))
		assert.Equal(t, r, u.Role())
	}
}

func TestSetRole_InvalidRejected(t *testing.T) {
	u := validUser(t)
	assert.ErrorIs(t, u.SetRole("god"), user.ErrInvalidRole)
	assert.Equal(t, user.RoleReader, u.Role()) // unchanged
}

// ── RefreshToken ──────────────────────────────────────────────────────────────

func TestRefreshToken_NewValid(t *testing.T) {
	rt, err := user.NewRefreshToken(uuid.New(), "sha256hash", 7*24*time.Hour)
	require.NoError(t, err)
	assert.True(t, rt.IsValid())
	assert.False(t, rt.IsExpired())
	assert.False(t, rt.IsRevoked())
}

func TestRefreshToken_Revoke(t *testing.T) {
	rt, err := user.NewRefreshToken(uuid.New(), "sha256hash", 7*24*time.Hour)
	require.NoError(t, err)
	rt.Revoke()
	assert.False(t, rt.IsValid())
	assert.True(t, rt.IsRevoked())
	assert.NotNil(t, rt.RevokedAt())
}

func TestRefreshToken_Expired(t *testing.T) {
	rt, err := user.NewRefreshToken(uuid.New(), "sha256hash", -time.Second)
	require.NoError(t, err)
	assert.False(t, rt.IsValid())
	assert.True(t, rt.IsExpired())
}

func TestRefreshToken_EmptyHashRejected(t *testing.T) {
	_, err := user.NewRefreshToken(uuid.New(), "", 7*24*time.Hour)
	assert.Error(t, err)
}

// ── Role.IsValid ──────────────────────────────────────────────────────────────

func TestRole_IsValid(t *testing.T) {
	assert.True(t, user.RoleReader.IsValid())
	assert.True(t, user.RoleLibrarian.IsValid())
	assert.True(t, user.RoleAdmin.IsValid())
	assert.False(t, user.Role("").IsValid())
	assert.False(t, user.Role("hacker").IsValid())
}
