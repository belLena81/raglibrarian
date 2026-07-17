// Package port defines Identity application-owned persistence boundaries.
package port

import (
	"context"
	"errors"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

var (
	// ErrRefreshTokenInvalid indicates an absent, revoked, or expired token.
	ErrRefreshTokenInvalid = errors.New("session: refresh token is invalid or expired")
	// ErrRefreshTokenReused indicates replay of a token already rotated.
	ErrRefreshTokenReused = errors.New("session: refresh token has already been used")
	// ErrSessionInvalid indicates an absent, revoked, expired, or mismatched session.
	ErrSessionInvalid = errors.New("session: session is invalid or expired")
)

// Session is the application view of durable Identity session state.
type Session struct {
	ID        string
	UserID    string
	FamilyID  string
	ExpiresAt time.Time
}

// RefreshPrincipal is loaded and locked before a refresh token is consumed.
type RefreshPrincipal struct {
	Session    Session
	UserID     string
	Name       string
	Email      string
	Role       domain.Role
	Status     domain.Status
	VerifiedAt time.Time
}

// PrepareRefresh performs bounded local work before rotation is committed.
// Implementations must not call a database, network, filesystem, or blocking
// external dependency while the store holds transaction locks.
type PrepareRefresh func(RefreshPrincipal) error

// UserReader supplies credential-bearing users for login.
type UserReader interface {
	FindByEmail(context.Context, string) (domain.User, error)
}

// RoleAccountReader supplies at most one account for each supported role. The
// bounded result lets authentication keep password work independent of how
// many accounts share an address.
type RoleAccountReader interface {
	FindByEmailRoles(context.Context, string) ([]domain.User, error)
}

// SessionStore owns session creation, one-time refresh rotation, validation,
// and revocation for the session use case.
type SessionStore interface {
	Create(ctx context.Context, session Session, createdAt time.Time, refreshTokenHash []byte) error
	Rotate(ctx context.Context, currentHash, successorHash []byte, now time.Time, prepare PrepareRefresh) error
	Validate(ctx context.Context, userID, sessionID string, now time.Time) error
	Logout(ctx context.Context, sessionID string, now time.Time) error
}
