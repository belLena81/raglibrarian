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
	RoleAdmin  Role = "admin"
	RoleReader Role = "reader"
)

// IsValid reports whether r is a recognised role.
func (r Role) IsValid() bool {
	return r == RoleAdmin || r == RoleReader
}

// CanWrite reports whether this role may create or modify resources.
func (r Role) CanWrite() bool { return r == RoleAdmin }

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
