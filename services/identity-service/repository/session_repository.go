package repository

import (
	"context"
	"errors"
	"time"
)

var (
	ErrRefreshTokenInvalid = errors.New("session: refresh token is invalid or expired")
	ErrRefreshTokenReused  = errors.New("session: refresh token has already been used")
	ErrSessionInvalid      = errors.New("session: session is invalid or expired")
)

// Session is Identity-owned persisted session state. Refresh token plaintext is
// intentionally absent: repositories only receive and store its SHA-256 hash.
type Session struct {
	ID        string
	UserID    string
	FamilyID  string
	ExpiresAt time.Time
}

// SessionRepository owns durable sessions and one-time refresh-token rotation.
type SessionRepository interface {
	Create(ctx context.Context, userID string, expiresAt time.Time, tokenHash []byte) (Session, error)
	Rotate(ctx context.Context, tokenHash, successorHash []byte, now time.Time) (Session, error)
	Validate(ctx context.Context, userID, sessionID string, now time.Time) error
	Logout(ctx context.Context, sessionID string, now time.Time) error
}
