package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// Role is a value object representing a user's permission level.
// It is immutable after construction and validated at the boundary —
// the rest of the system never passes raw strings for roles.
type Role string

// Role constants define all valid permission levels in the system.
const (
	RoleAdmin  Role = "admin"
	RoleReader Role = "reader"
)

// IsValid reports whether r is a recognised role.
func (r Role) IsValid() bool {
	return r == RoleAdmin || r == RoleReader
}

// CanWrite reports whether this role may create or modify resources.
// Centralising the policy here means handler/usecase code never contains
// role-string comparisons.
func (r Role) CanWrite() bool { return r == RoleAdmin }

// User is the aggregate root for authentication and authorisation.
// PasswordHash is stored; the plaintext password never lives on this struct.
// All fields are private — use NewUser or NewUserFromDB to construct.
type User struct {
	id           string
	email        string
	passwordHash string
	role         Role
	createdAt    time.Time
}

// NewUser constructs a User with a pre-hashed password.
// The caller is responsible for hashing — this keeps the domain free of
// the bcrypt dependency.
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

// NewUserFromDB reconstructs a User from persisted data without re-validation.
// Only infrastructure (repository implementations) should call this.
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
