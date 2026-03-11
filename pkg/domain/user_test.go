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
	assert.True(t, domain.RoleLibrarian.IsValid())
	assert.False(t, domain.Role("superuser").IsValid())
	assert.False(t, domain.Role("").IsValid())
	// Case-sensitivity: role values are lowercase constants; mixed-case must be
	// rejected to avoid ambiguous string comparisons in middleware and the DB.
	assert.False(t, domain.Role("Admin").IsValid())
	assert.False(t, domain.Role("LIBRARIAN").IsValid())
	assert.False(t, domain.Role("Reader").IsValid())
}

func TestRole_CanWrite(t *testing.T) {
	assert.True(t, domain.RoleAdmin.CanWrite())
	assert.True(t, domain.RoleLibrarian.CanWrite())
	assert.False(t, domain.RoleReader.CanWrite())
}

// TestRole_Rank verifies the strict ordering: reader < librarian < admin.
// This property is the foundation of RequireMinRole enforcement.
func TestRole_Rank(t *testing.T) {
	// Reader is the lowest rank.
	assert.Less(t, domain.RoleReader.Rank(), domain.RoleLibrarian.Rank())
	// Librarian sits between reader and admin.
	assert.Less(t, domain.RoleLibrarian.Rank(), domain.RoleAdmin.Rank())
	// Admin is strictly above librarian — not equal.
	assert.NotEqual(t, domain.RoleAdmin.Rank(), domain.RoleLibrarian.Rank())
}

func TestRole_Rank_UnknownRole_ReturnsNegative(t *testing.T) {
	// An unrecognised role must never accidentally pass a privilege check.
	// Returning a negative rank guarantees it is lower than every valid role.
	assert.Less(t, domain.Role("").Rank(), 0)
	assert.Less(t, domain.Role("superuser").Rank(), 0)
}

func TestRole_AtLeast(t *testing.T) {
	tests := []struct {
		name     string
		role     domain.Role
		min      domain.Role
		expected bool
	}{
		// Admin satisfies every minimum.
		{"admin satisfies admin", domain.RoleAdmin, domain.RoleAdmin, true},
		{"admin satisfies librarian", domain.RoleAdmin, domain.RoleLibrarian, true},
		{"admin satisfies reader", domain.RoleAdmin, domain.RoleReader, true},
		// Librarian satisfies librarian and reader, but not admin.
		{"librarian satisfies librarian", domain.RoleLibrarian, domain.RoleLibrarian, true},
		{"librarian satisfies reader", domain.RoleLibrarian, domain.RoleReader, true},
		{"librarian does not satisfy admin", domain.RoleLibrarian, domain.RoleAdmin, false},
		// Reader only satisfies reader.
		{"reader satisfies reader", domain.RoleReader, domain.RoleReader, true},
		{"reader does not satisfy librarian", domain.RoleReader, domain.RoleLibrarian, false},
		{"reader does not satisfy admin", domain.RoleReader, domain.RoleAdmin, false},
		// Unknown role never satisfies anything.
		{"unknown does not satisfy reader", domain.Role("god"), domain.RoleReader, false},
		{"unknown does not satisfy admin", domain.Role("god"), domain.RoleAdmin, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.role.AtLeast(tt.min))
		})
	}
}

func TestRole_String(t *testing.T) {
	assert.Equal(t, "admin", string(domain.RoleAdmin))
	assert.Equal(t, "librarian", string(domain.RoleLibrarian))
	assert.Equal(t, "reader", string(domain.RoleReader))
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

func TestNewUser_LibrarianRole(t *testing.T) {
	u, err := domain.NewUser("librarian@example.com", "hashed-secret", domain.RoleLibrarian)

	require.NoError(t, err)
	assert.Equal(t, domain.RoleLibrarian, u.Role())
	assert.True(t, u.Role().CanWrite(),
		"librarian must be able to write — otherwise book management endpoints are blocked")
	assert.True(t, u.Role().IsValid())
	assert.NotEmpty(t, u.ID())
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
	tests := []struct {
		name string
		role domain.Role
	}{
		{"arbitrary string", domain.Role("god")},
		// Mixed-case variants must all be rejected — role comparison is exact.
		{"uppercase ADMIN", domain.Role("ADMIN")},
		{"title-case Admin", domain.Role("Admin")},
		{"uppercase LIBRARIAN", domain.Role("LIBRARIAN")},
		{"title-case Librarian", domain.Role("Librarian")},
		{"uppercase READER", domain.Role("READER")},
		// Empty role is not allowed; callers must explicitly choose a role.
		{"empty string", domain.Role("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := domain.NewUser("a@example.com", "hash", tt.role)
			assert.ErrorIs(t, err, domain.ErrInvalidRole)
		})
	}
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
