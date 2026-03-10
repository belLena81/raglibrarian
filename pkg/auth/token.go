// Package auth provides PASETO v4 local token issuance and validation.
package auth

import (
	"fmt"
	"time"

	gopasseto "aidanwoods.dev/go-paseto"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// Claims holds the verified token payload stored in request context.
type Claims struct {
	UserID string
	Email  string
	Role   domain.Role
}

// Issuer creates and validates PASETO v4 local tokens. Safe for concurrent use.
type Issuer struct {
	key        gopasseto.V4SymmetricKey
	ttl        time.Duration
	timeSource func() time.Time // injectable so tests can control "now"
}

// NewIssuer constructs an Issuer from a 32-byte symmetric key.
func NewIssuer(rawKey []byte, ttl time.Duration) (*Issuer, error) {
	if len(rawKey) != 32 {
		return nil, fmt.Errorf(
			"auth: symmetric key must be exactly 32 bytes, got %d", len(rawKey),
		)
	}

	key, err := gopasseto.V4SymmetricKeyFromBytes(rawKey)
	if err != nil {
		return nil, fmt.Errorf("auth: load symmetric key: %w", err)
	}

	return &Issuer{
		key:        key,
		ttl:        ttl,
		timeSource: time.Now,
	}, nil
}

// Issue mints a PASETO v4 local token for the given user.
func (is *Issuer) Issue(user domain.User) (string, error) {
	now := is.timeSource()

	token := gopasseto.NewToken()

	token.SetIssuedAt(now)
	token.SetNotBefore(now)
	token.SetExpiration(now.Add(is.ttl))
	token.SetSubject(user.ID())
	token.SetIssuer("raglibrarian")

	token.SetString("email", user.Email())
	token.SetString("role", string(user.Role()))

	encrypted := token.V4Encrypt(is.key, nil)
	return encrypted, nil
}

// Validate decrypts and verifies a PASETO v4 local token string.
// Returns ErrInvalidToken for any failure — expired, tampered, wrong key, or malformed.
func (is *Issuer) Validate(tokenStr string) (Claims, error) {
	parser := gopasseto.NewParser()

	parser.AddRule(gopasseto.NotExpired())
	parser.AddRule(gopasseto.ValidAt(is.timeSource()))
	parser.AddRule(gopasseto.IssuedBy("raglibrarian"))

	token, err := parser.ParseV4Local(is.key, tokenStr, nil)
	if err != nil {
		return Claims{}, domain.ErrInvalidToken
	}

	userID, err := token.GetSubject()
	if err != nil {
		return Claims{}, domain.ErrInvalidToken
	}

	email, err := token.GetString("email")
	if err != nil {
		return Claims{}, domain.ErrInvalidToken
	}

	roleStr, err := token.GetString("role")
	if err != nil {
		return Claims{}, domain.ErrInvalidToken
	}

	role := domain.Role(roleStr)
	if !role.IsValid() {
		return Claims{}, domain.ErrInvalidToken
	}

	return Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
	}, nil
}
