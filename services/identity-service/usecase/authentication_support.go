// Package usecase contains Identity application workflows.
package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

const refreshTokenBytes = 32

// This public fixed hash equalizes password work for unknown accounts.
const dummyPasswordHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW" // #nosec G101 -- timing-only fixture

// AuthResult is the internal result of creating or rotating a session.
type AuthResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         domain.Role
}

// PasswordHasher owns password validation and bounded expensive work.
type PasswordHasher interface {
	Validate(string) error
	Hash(context.Context, string) (string, error)
	Compare(context.Context, string, string) error
}

// AccessTokenIssuer issues access tokens from primitive Identity values.
type AccessTokenIssuer interface {
	Issue(userID, email string, role domain.Role, sessionID string) (string, error)
}

// Clock supplies deterministic application time.
type Clock interface {
	Now() time.Time
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func newRefreshToken() (string, []byte, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, hashRefreshToken(plaintext), nil
}

func hashRefreshToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func newSession(userID string, expiresAt time.Time) port.Session {
	return port.Session{
		ID:        uuid.NewString(),
		UserID:    userID,
		FamilyID:  uuid.NewString(),
		ExpiresAt: expiresAt.UTC(),
	}
}
