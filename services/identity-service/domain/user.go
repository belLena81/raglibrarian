// Package domain contains Identity-owned business rules and aggregates.
package domain

import (
	"errors"
	"strings"
	"time"
)

// Identity domain errors are stable values used by application and transport
// layers to map failures without inspecting sensitive details.
var (
	ErrEmptyEmail           = errors.New("user: email must not be empty")
	ErrInvalidEmail         = errors.New("user: email format is invalid")
	ErrEmptyName            = errors.New("user: name must not be empty")
	ErrEmptyPasswordHash    = errors.New("user: password hash must not be empty")
	ErrInvalidRole          = errors.New("user: role is invalid")
	ErrInvalidStatus        = errors.New("user: status is invalid")
	ErrInvalidTransition    = errors.New("user: status transition is invalid")
	ErrUserNotFound         = errors.New("user: not found")
	ErrInvalidPassword      = errors.New("user: password is invalid")
	ErrInvalidCredentials   = errors.New("auth: invalid credentials")
	ErrInvalidVerification  = errors.New("verification: token is invalid or expired")
	ErrInvalidBootstrap     = errors.New("bootstrap: request is invalid")
	ErrBootstrapComplete    = errors.New("bootstrap: administrator already exists")
	ErrForbidden            = errors.New("authorization: forbidden")
	ErrConflict             = errors.New("identity: state conflict")
	ErrInvalidPasswordReset = errors.New("password reset: invalid or expired")
)

// Role defines an account's authorization category.
type Role string

// Supported account roles.
const (
	RoleAdmin     Role = "admin"
	RoleLibrarian Role = "librarian"
	RoleReader    Role = "reader"
)

// IsValid reports whether the role belongs to the domain's closed role set.
func (r Role) IsValid() bool {
	return r == RoleAdmin || r == RoleLibrarian || r == RoleReader
}

// Status defines an account's lifecycle state.
type Status string

// Supported account lifecycle states.
const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusRejected Status = "rejected"
)

// IsValid reports whether the status belongs to the closed lifecycle set.
func (s Status) IsValid() bool {
	return s == StatusPending || s == StatusActive || s == StatusRejected
}

// User is Identity's aggregate root. Sensitive values are exposed only to
// application-owned ports and are never safe diagnostic fields.
type User struct {
	id               string
	name             string
	email            string
	emailFingerprint []byte
	passwordHash     string
	role             Role
	status           Status
	verifiedAt       time.Time
	createdAt        time.Time
	reviewedBy       string
	reviewedAt       time.Time
}

// NewVerifiedUser validates and creates a user whose email ownership was
// verified at the supplied time.
func NewVerifiedUser(
	id, name, email string,
	fingerprint []byte,
	passwordHash string,
	role Role,
	status Status,
	verifiedAt, createdAt time.Time,
) (User, error) {
	return newUser(id, name, email, fingerprint, passwordHash, role, status, verifiedAt, createdAt)
}

// NewUnverifiedUser validates and creates a user without asserting ownership
// of the supplied email address.
func NewUnverifiedUser(
	id, name, email string,
	fingerprint []byte,
	passwordHash string,
	role Role,
	status Status,
	createdAt time.Time,
) (User, error) {
	return newUser(id, name, email, fingerprint, passwordHash, role, status, time.Time{}, createdAt)
}

func newUser(
	id, name, email string,
	fingerprint []byte,
	passwordHash string,
	role Role,
	status Status,
	verifiedAt, createdAt time.Time,
) (User, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	if strings.TrimSpace(id) == "" {
		return User{}, ErrUserNotFound
	}
	if name == "" {
		return User{}, ErrEmptyName
	}
	if email == "" {
		return User{}, ErrEmptyEmail
	}
	at := strings.LastIndex(email, "@")
	if at < 1 || at == len(email)-1 {
		return User{}, ErrInvalidEmail
	}
	if strings.TrimSpace(passwordHash) == "" {
		return User{}, ErrEmptyPasswordHash
	}
	if !role.IsValid() || !status.IsValid() {
		if !role.IsValid() {
			return User{}, ErrInvalidRole
		}
		return User{}, ErrInvalidStatus
	}
	if role == RoleAdmin && status != StatusActive {
		return User{}, ErrInvalidStatus
	}
	if role == RoleReader && status != StatusActive {
		return User{}, ErrInvalidStatus
	}
	return User{
		id: id, name: name, email: email,
		emailFingerprint: append([]byte(nil), fingerprint...),
		passwordHash:     passwordHash, role: role, status: status,
		verifiedAt: verifiedAt.UTC(), createdAt: createdAt.UTC(),
	}, nil
}

// RehydrateUser reconstructs a previously validated aggregate from repository
// values without generating new identity or time data.
func RehydrateUser(
	id, name, email, passwordHash string,
	fingerprint []byte,
	role Role,
	status Status,
	verifiedAt, createdAt time.Time,
	reviewedBy string,
	reviewedAt time.Time,
) User {
	return User{
		id: id, name: name, email: email,
		emailFingerprint: append([]byte(nil), fingerprint...),
		passwordHash:     passwordHash, role: role, status: status,
		verifiedAt: verifiedAt, createdAt: createdAt,
		reviewedBy: reviewedBy, reviewedAt: reviewedAt,
	}
}

// Approve performs the final pending-librarian to active transition.
func (u *User) Approve(reviewerID string, at time.Time) error {
	if u.role != RoleLibrarian || u.status != StatusPending || reviewerID == "" {
		return ErrInvalidTransition
	}
	u.status = StatusActive
	u.reviewedBy = reviewerID
	u.reviewedAt = at.UTC()
	return nil
}

// Reject performs the final pending-librarian to rejected transition.
func (u *User) Reject(reviewerID string, at time.Time) error {
	if u.role != RoleLibrarian || u.status != StatusPending || reviewerID == "" {
		return ErrInvalidTransition
	}
	u.status = StatusRejected
	u.reviewedBy = reviewerID
	u.reviewedAt = at.UTC()
	return nil
}

// ID returns the aggregate identifier.
func (u User) ID() string { return u.id }

// Name returns the normalized display name.
func (u User) Name() string { return u.name }

// Email returns the normalized email address.
func (u User) Email() string { return u.email }

// EmailFingerprint returns a defensive copy of the pseudonymous email lookup key.
func (u User) EmailFingerprint() []byte { return append([]byte(nil), u.emailFingerprint...) }

// PasswordHash returns the application-owned password verifier.
func (u User) PasswordHash() string { return u.passwordHash }

// Role returns the account's authorization role.
func (u User) Role() Role { return u.role }

// Status returns the account's lifecycle status.
func (u User) Status() Status { return u.status }

// VerifiedAt returns when ownership of the email address was verified.
func (u User) VerifiedAt() time.Time { return u.verifiedAt }

// CreatedAt returns when the aggregate was created.
func (u User) CreatedAt() time.Time { return u.createdAt }

// ReviewedBy returns the administrator who made the final review decision.
func (u User) ReviewedBy() string { return u.reviewedBy }

// ReviewedAt returns when the final review decision was made.
func (u User) ReviewedAt() time.Time { return u.reviewedAt }

// CanAuthenticate reports whether the account may establish or use sessions.
// Email verification is intentionally independent from account activation.
func (u User) CanAuthenticate() bool { return u.status == StatusActive }

// Principal is current session-bound identity and authorization state.
type Principal struct {
	UserID    string
	SessionID string
	Name      string
	Email     string
	Role      Role
	Status    Status
}

// IsActive reports whether the principal has an active account and valid role.
func (p Principal) IsActive() bool { return p.Status == StatusActive && p.Role.IsValid() }

// IsAdmin reports whether the active principal is an administrator.
func (p Principal) IsAdmin() bool { return p.IsActive() && p.Role == RoleAdmin }
