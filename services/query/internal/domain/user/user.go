// Package user contains the User aggregate root for the auth bounded context.
// All fields are unexported. Use NewUser to construct; setters to mutate.
// Validation is the exclusive responsibility of this package.
package user

import (
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── Sentinel errors ───────────────────────────────────────────────────────────

var (
	ErrEmptyName       = errors.New("user: name must not be empty")
	ErrNameTooLong     = errors.New("user: name must be 100 characters or fewer")
	ErrInvalidEmail    = errors.New("user: email address is invalid")
	ErrEmptyPassword   = errors.New("user: password hash must not be empty")
	ErrInvalidRole     = errors.New("user: role is not recognised")
	ErrUserNotFound    = errors.New("user: not found")
	ErrEmailTaken      = errors.New("user: email is already registered")
	ErrInvalidPassword = errors.New("user: password is incorrect")
	ErrTokenNotFound   = errors.New("user: refresh token not found")
	ErrTokenRevoked    = errors.New("user: refresh token has been revoked")
)

// ── Role ──────────────────────────────────────────────────────────────────────

type Role string

const (
	RoleReader    Role = "reader"
	RoleLibrarian Role = "librarian"
	RoleAdmin     Role = "admin"
)

var validRoles = map[Role]struct{}{
	RoleReader: {}, RoleLibrarian: {}, RoleAdmin: {},
}

func (r Role) IsValid() bool {
	_, ok := validRoles[r]
	return ok
}

// ── User aggregate root ───────────────────────────────────────────────────────

var emailRE = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

type User struct {
	id           uuid.UUID
	name         string
	email        string
	passwordHash string
	role         Role
	createdAt    time.Time
	updatedAt    time.Time
}

// NewUser constructs a validated User ready to persist.
// passwordHash must already be a bcrypt hash — hashing is the caller's job.
func NewUser(name, email, passwordHash string, role Role) (*User, error) {
	u := &User{
		id:        uuid.New(),
		createdAt: time.Now().UTC(),
		updatedAt: time.Now().UTC(),
	}
	if err := u.SetName(name); err != nil {
		return nil, err
	}
	if err := u.SetEmail(email); err != nil {
		return nil, err
	}
	if err := u.SetPasswordHash(passwordHash); err != nil {
		return nil, err
	}
	if err := u.SetRole(role); err != nil {
		return nil, err
	}
	return u, nil
}

// Reconstitute rebuilds a User from persisted storage (repository only).
// No validation — the database is the authority for stored values.
func Reconstitute(id uuid.UUID, name, email, passwordHash string, role Role, createdAt, updatedAt time.Time) *User {
	return &User{id: id, name: name, email: email, passwordHash: passwordHash,
		role: role, createdAt: createdAt, updatedAt: updatedAt}
}

// ── Getters ───────────────────────────────────────────────────────────────────

func (u *User) ID() uuid.UUID        { return u.id }
func (u *User) Name() string         { return u.name }
func (u *User) Email() string        { return u.email }
func (u *User) PasswordHash() string { return u.passwordHash }
func (u *User) Role() Role           { return u.role }
func (u *User) CreatedAt() time.Time { return u.createdAt }
func (u *User) UpdatedAt() time.Time { return u.updatedAt }

// ── Setters ───────────────────────────────────────────────────────────────────

func (u *User) SetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return ErrEmptyName
	}
	if len(name) > 100 {
		return ErrNameTooLong
	}
	u.name = name
	u.touch()
	return nil
}

func (u *User) SetEmail(email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRE.MatchString(email) {
		return ErrInvalidEmail
	}
	u.email = email
	u.touch()
	return nil
}

func (u *User) SetPasswordHash(hash string) error {
	if strings.TrimSpace(hash) == "" {
		return ErrEmptyPassword
	}
	u.passwordHash = hash
	u.touch()
	return nil
}

func (u *User) SetRole(role Role) error {
	if !role.IsValid() {
		return ErrInvalidRole
	}
	u.role = role
	u.touch()
	return nil
}

func (u *User) touch() { u.updatedAt = time.Now().UTC() }

// ── RefreshToken value object ─────────────────────────────────────────────────

// RefreshToken is a revocable opaque token stored in Postgres.
// It is a value object — no identity of its own beyond its ID.
type RefreshToken struct {
	id        uuid.UUID
	userID    uuid.UUID
	tokenHash string    // SHA-256 of the raw token bytes — never store plaintext
	expiresAt time.Time
	revokedAt *time.Time
	createdAt time.Time
}

// NewRefreshToken builds a RefreshToken value object ready to persist.
func NewRefreshToken(userID uuid.UUID, tokenHash string, ttl time.Duration) (*RefreshToken, error) {
	if strings.TrimSpace(tokenHash) == "" {
		return nil, errors.New("refresh token: hash must not be empty")
	}
	now := time.Now().UTC()
	return &RefreshToken{
		id:        uuid.New(),
		userID:    userID,
		tokenHash: tokenHash,
		expiresAt: now.Add(ttl),
		createdAt: now,
	}, nil
}

func ReconstituteFreshToken(id, userID uuid.UUID, tokenHash string, expiresAt time.Time, revokedAt *time.Time, createdAt time.Time) *RefreshToken {
	return &RefreshToken{id: id, userID: userID, tokenHash: tokenHash,
		expiresAt: expiresAt, revokedAt: revokedAt, createdAt: createdAt}
}

func (t *RefreshToken) ID() uuid.UUID        { return t.id }
func (t *RefreshToken) UserID() uuid.UUID     { return t.userID }
func (t *RefreshToken) TokenHash() string     { return t.tokenHash }
func (t *RefreshToken) ExpiresAt() time.Time  { return t.expiresAt }
func (t *RefreshToken) RevokedAt() *time.Time { return t.revokedAt }
func (t *RefreshToken) CreatedAt() time.Time  { return t.createdAt }

func (t *RefreshToken) IsExpired() bool  { return time.Now().UTC().After(t.expiresAt) }
func (t *RefreshToken) IsRevoked() bool  { return t.revokedAt != nil }
func (t *RefreshToken) IsValid() bool    { return !t.IsExpired() && !t.IsRevoked() }

func (t *RefreshToken) Revoke() {
	now := time.Now().UTC()
	t.revokedAt = &now
}
