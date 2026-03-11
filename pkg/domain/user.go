package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Role represents a user's permission level.
type Role string

// Valid role values.
const (
	RoleReader    Role = "reader"
	RoleLibrarian Role = "librarian"
	RoleAdmin     Role = "admin"
)

// roleRanks maps every valid role to its privilege tier.
// Reader < Librarian < Admin. Unknown roles get rank -1.
var roleRanks = map[Role]int{
	RoleReader:    0,
	RoleLibrarian: 1,
	RoleAdmin:     2,
}

// IsValid reports whether r is a recognised role.
func (r Role) IsValid() bool {
	_, ok := roleRanks[r]
	return ok
}

// Rank returns the privilege tier of r.
// Returns -1 for any unrecognised role so that unknown roles always fail
// privilege checks — they can never accidentally satisfy a minimum.
func (r Role) Rank() int {
	if rank, ok := roleRanks[r]; ok {
		return rank
	}
	return -1
}

// AtLeast reports whether r has at least the same privilege level as min.
// Used by RequireMinRole middleware; keeps the decision logic in the domain
// rather than spreading it across HTTP handlers.
func (r Role) AtLeast(min Role) bool {
	return r.Rank() >= min.Rank() && r.Rank() >= 0
}

// CanWrite reports whether this role may create or modify book resources.
// Both Librarian and Admin have write access; Reader is read-only.
func (r Role) CanWrite() bool {
	return r == RoleAdmin || r == RoleLibrarian
}

// User is the aggregate root for authentication and authorisation.
// Stores a bcrypt password hash; the plaintext is never retained.
type User struct {
	id           string
	email        string
	passwordHash string
	role         Role
	createdAt    time.Time
}

// NewUser constructs a User with a pre-hashed password.
func NewUser(email, passwordHash string, role Role) (User, error) {
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, ErrEmptyPasswordHash
	}
	if !role.IsValid() {
		return User{}, ErrInvalidRole
	}

	return User{
		id:           uuid.NewString(),
		email:        email,
		passwordHash: passwordHash,
		role:         role,
		createdAt:    time.Now().UTC(),
	}, nil
}

// NewUserFromDB reconstructs a User from persisted data, skipping validation.
// Only repository implementations should call this.
func NewUserFromDB(id, email, passwordHash string, role Role, createdAt time.Time) User {
	return User{
		id:           id,
		email:        email,
		passwordHash: passwordHash,
		role:         role,
		createdAt:    createdAt,
	}
}

// ID returns the user's unique identifier.
func (u User) ID() string { return u.id }

// Email returns the user's email address.
func (u User) Email() string { return u.email }

// PasswordHash returns the bcrypt hash of the user's password.
func (u User) PasswordHash() string { return u.passwordHash }

// Role returns the user's permission level.
func (u User) Role() Role { return u.role }

// CreatedAt returns when the user account was created.
func (u User) CreatedAt() time.Time { return u.createdAt }
