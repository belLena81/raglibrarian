package user

import (
	"context"

	"github.com/google/uuid"
)

// Repository is the port for User persistence.
type Repository interface {
	Save(ctx context.Context, u *User) error
	FindByID(ctx context.Context, id uuid.UUID) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	ExistsByEmail(ctx context.Context, email string) (bool, error)
}

// RefreshTokenRepository is the port for refresh token persistence.
// Kept separate from Repository because the access patterns differ
// (high-frequency reads on tokenHash vs low-frequency user lookups).
type RefreshTokenRepository interface {
	// SaveRefreshToken persists a new refresh token.
	SaveRefreshToken(ctx context.Context, t *RefreshToken) error

	// FindRefreshTokenByHash looks up by the SHA-256 hash of the raw token.
	// Returns ErrTokenNotFound when no row matches.
	FindRefreshTokenByHash(ctx context.Context, hash string) (*RefreshToken, error)

	// RevokeRefreshToken marks a token revoked. Idempotent.
	RevokeRefreshToken(ctx context.Context, id uuid.UUID) error

	// RevokeAllForUser revokes all active tokens for a user (logout-everywhere).
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
}
