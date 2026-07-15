// Package domain contains Identity-owned business rules and aggregates.
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Identity domain errors are stable inputs to repository and transport adapters.
var (
	ErrEmptyEmail         = errors.New("user: email must not be empty")
	ErrInvalidEmail       = errors.New("user: email format is invalid")
	ErrEmptyPasswordHash  = errors.New("user: password hash must not be empty")
	ErrInvalidRole        = errors.New("user: role must be admin or reader")
	ErrEmailTaken         = errors.New("user: email is already registered")
	ErrUserNotFound       = errors.New("user: not found")
	ErrInvalidPassword    = errors.New("user: password is invalid")
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
)

// Role is an Identity-owned authorization role.
type Role string

// Supported Identity roles.
const (
	RoleAdmin  Role = "admin"
	RoleReader Role = "reader"
)

// IsValid reports whether the role belongs to the supported role vocabulary.
func (r Role) IsValid() bool { return r == RoleAdmin || r == RoleReader }

// CanWrite reports whether the role may mutate protected resources.
func (r Role) CanWrite() bool { return r == RoleAdmin }

// User is Identity's aggregate root for credentials and roles.
type User struct {
	id           string
	email        string
	passwordHash string
	role         Role
	createdAt    time.Time
}

// NewUser creates a validated Identity user.
func NewUser(email, passwordHash string, role Role) (User, error) {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return User{}, ErrEmptyEmail
	}
	at := strings.LastIndex(trimmed, "@")
	if at < 1 || at == len(trimmed)-1 {
		return User{}, ErrInvalidEmail
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, ErrEmptyPasswordHash
	}
	if !role.IsValid() {
		return User{}, ErrInvalidRole
	}
	return User{id: uuid.NewString(), email: trimmed, passwordHash: passwordHash, role: role, createdAt: time.Now().UTC()}, nil
}

// NewUserFromDB reconstructs repository-owned persisted state.
func NewUserFromDB(id, email, passwordHash string, role Role, createdAt time.Time) User {
	return User{id: id, email: email, passwordHash: passwordHash, role: role, createdAt: createdAt}
}

// ID returns the stable user identifier.
func (u User) ID() string { return u.id }

// Email returns the normalized email address.
func (u User) Email() string { return u.email }

// PasswordHash returns the stored bcrypt hash.
func (u User) PasswordHash() string { return u.passwordHash }

// Role returns the Identity-owned role.
func (u User) Role() Role { return u.role }

// CreatedAt returns the account creation time.
func (u User) CreatedAt() time.Time { return u.createdAt }
