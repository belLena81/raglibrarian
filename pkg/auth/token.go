// Package auth provides PASETO v4 public token issuance and validation.
package auth

import (
	"crypto/ed25519"
	"fmt"
	"time"

	gopasseto "aidanwoods.dev/go-paseto"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// Claims holds the verified token payload stored in request context.
type Claims struct {
	UserID    string
	Email     string
	Role      domain.Role
	SessionID string
}

// SessionTokens is a transport-neutral result of an Identity session action.
// It intentionally has no JSON tags: refresh tokens must never be serialized
// in a public response.
type SessionTokens struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         string
}

// Signer creates PASETO v4 public tokens. Only identity-service receives its
// private key.
type Signer struct {
	key        gopasseto.V4AsymmetricSecretKey
	ttl        time.Duration
	timeSource func() time.Time // injectable so tests can control "now"
}

// Verifier validates PASETO v4 public tokens. It can never mint a token.
type Verifier struct {
	key        gopasseto.V4AsymmetricPublicKey
	timeSource func() time.Time
}

// Issuer is retained for focused tests that need one object capable of both
// issuing and verifying. Production code must use Signer in identity-service
// and Verifier in edge-api so the private key cannot cross the boundary.
type Issuer struct {
	*Signer
	*Verifier
}

// NewIssuer derives an asymmetric key pair from a 32-byte test seed. It is a
// compatibility helper and must not be used for runtime configuration.
func NewIssuer(seed []byte, ttl time.Duration) (*Issuer, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("auth: test seed must be exactly %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	signer, err := NewSigner(privateKey, ttl)
	if err != nil {
		return nil, err
	}
	verifier, err := NewVerifier(privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	return &Issuer{Signer: signer, Verifier: verifier}, nil
}

// NewSigner constructs a Signer from a 64-byte Ed25519 private key.
func NewSigner(rawKey []byte, ttl time.Duration) (*Signer, error) {
	if len(rawKey) != 64 {
		return nil, fmt.Errorf(
			"auth: signing key must be exactly 64 bytes, got %d", len(rawKey),
		)
	}

	key, err := gopasseto.NewV4AsymmetricSecretKeyFromBytes(rawKey)
	if err != nil {
		return nil, fmt.Errorf("auth: load signing key: %w", err)
	}

	return &Signer{
		key:        key,
		ttl:        ttl,
		timeSource: time.Now,
	}, nil
}

// NewVerifier constructs a Verifier from a 32-byte Ed25519 public key.
func NewVerifier(rawKey []byte) (*Verifier, error) {
	if len(rawKey) != 32 {
		return nil, fmt.Errorf("auth: verification key must be exactly 32 bytes, got %d", len(rawKey))
	}
	key, err := gopasseto.NewV4AsymmetricPublicKeyFromBytes(rawKey)
	if err != nil {
		return nil, fmt.Errorf("auth: load verification key: %w", err)
	}
	return &Verifier{key: key, timeSource: time.Now}, nil
}

// Issue mints a PASETO v4 public token for the given user.
func (s *Signer) Issue(user domain.User, sessionIDs ...string) (string, error) {
	now := s.timeSource()

	token := gopasseto.NewToken()

	token.SetIssuedAt(now)
	token.SetNotBefore(now)
	token.SetExpiration(now.Add(s.ttl))
	token.SetSubject(user.ID())
	token.SetIssuer("raglibrarian")
	token.SetAudience("edge-api")

	token.SetString("email", user.Email())
	token.SetString("role", string(user.Role()))
	if len(sessionIDs) > 0 && sessionIDs[0] != "" {
		token.SetString("session_id", sessionIDs[0])
	}

	return token.V4Sign(s.key, nil), nil
}

// Validate verifies a PASETO v4 public token string. Returns ErrInvalidToken
// for any failure — expired, tampered, wrong key, or malformed.
func (v *Verifier) Validate(tokenStr string) (Claims, error) {
	parser := gopasseto.NewParser()

	parser.AddRule(gopasseto.NotExpired())
	parser.AddRule(gopasseto.ValidAt(v.timeSource()))
	parser.AddRule(gopasseto.IssuedBy("raglibrarian"))
	parser.AddRule(gopasseto.ForAudience("edge-api"))

	token, err := parser.ParseV4Public(v.key, tokenStr, nil)
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

	sessionID, err := token.GetString("session_id")
	if err != nil {
		// Tokens issued before session support remain valid until they expire.
		sessionID = ""
	}

	return Claims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		SessionID: sessionID,
	}, nil
}
