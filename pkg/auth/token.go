// Package auth provides PASETO v4 public token issuance and validation.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	gopasseto "aidanwoods.dev/go-paseto"
)

var (
	// ErrInvalidToken is returned for every untrustworthy token failure.
	ErrInvalidToken = errors.New("auth: token is invalid or expired")
	// ErrInvalidSubject prevents Identity from minting incomplete access tokens.
	ErrInvalidSubject = errors.New("auth: token subject is invalid")
)

// Role is the stable access-token role claim shared by issuer and verifier.
type Role string

// Supported access-token role claims.
const (
	RoleAdmin     Role = "admin"
	RoleLibrarian Role = "librarian"
	RoleReader    Role = "reader"
)

// IsValid reports whether the role is safe for authorization decisions.
func (r Role) IsValid() bool { return r == RoleAdmin || r == RoleLibrarian || r == RoleReader }

// Claims holds the verified token payload stored in request context.
type Claims struct {
	UserID    string
	Email     string
	Role      Role
	SessionID string
	TokenID   string
	KeyID     string
}

// Subject is the primitive token input; it deliberately contains no service aggregate.
type Subject struct {
	UserID    string
	Email     string
	SessionID string
	Role      Role
}

// Signer creates PASETO v4 public tokens. Only identity-service receives its
// private key.
type Signer struct {
	key        gopasseto.V4AsymmetricSecretKey
	ttl        time.Duration
	timeSource func() time.Time // injectable so tests can control "now"
	keyID      string
}

// Verifier validates PASETO v4 public tokens. It can never mint a token.
type Verifier struct {
	key        gopasseto.V4AsymmetricPublicKey
	timeSource func() time.Time
}

// Keyring validates with an active key and, during rotation, one previous key.
// It tries a bounded set and never trusts an unverified key identifier.
type Keyring struct{ verifiers []*Verifier }

// NewKeyring constructs a bounded verifier set from the active public key and
// an optional previous public key used during rotation.
func NewKeyring(active []byte, previous []byte) (*Keyring, error) {
	activeVerifier, err := NewVerifier(active)
	if err != nil {
		return nil, err
	}
	keyring := &Keyring{verifiers: []*Verifier{activeVerifier}}
	if len(previous) > 0 {
		previousVerifier, previousErr := NewVerifier(previous)
		if previousErr != nil {
			return nil, previousErr
		}
		keyring.verifiers = append(keyring.verifiers, previousVerifier)
	}
	return keyring, nil
}

// Validate accepts a token signed by either configured key and returns one
// stable error for all untrustworthy inputs.
func (k *Keyring) Validate(token string) (Claims, error) {
	if k == nil {
		return Claims{}, ErrInvalidToken
	}
	for _, verifier := range k.verifiers {
		claims, err := verifier.Validate(token)
		if err == nil {
			return claims, nil
		}
	}
	return Claims{}, ErrInvalidToken
}

// NewSigner constructs a Signer from a 64-byte Ed25519 private key.
func NewSigner(rawKey []byte, ttl time.Duration) (*Signer, error) {
	if len(rawKey) != ed25519.PrivateKeySize {
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
		keyID:      "active-v1",
	}, nil
}

// NewSignerWithKeyID constructs a signer that identifies its active key in
// issued token metadata without changing validation trust decisions.
func NewSignerWithKeyID(rawKey []byte, ttl time.Duration, keyID string) (*Signer, error) {
	if strings.TrimSpace(keyID) == "" || len(keyID) > 64 {
		return nil, ErrInvalidSubject
	}
	signer, err := NewSigner(rawKey, ttl)
	if err != nil {
		return nil, err
	}
	signer.keyID = keyID
	return signer, nil
}

// NewVerifier constructs a Verifier from a 32-byte Ed25519 public key.
func NewVerifier(rawKey []byte) (*Verifier, error) {
	if len(rawKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("auth: verification key must be exactly 32 bytes, got %d", len(rawKey))
	}
	key, err := gopasseto.NewV4AsymmetricPublicKeyFromBytes(rawKey)
	if err != nil {
		return nil, fmt.Errorf("auth: load verification key: %w", err)
	}
	return &Verifier{key: key, timeSource: time.Now}, nil
}

// Issue mints a PASETO v4 public token for the given user.
func (s *Signer) Issue(subject Subject) (string, error) {
	if strings.TrimSpace(subject.UserID) == "" || strings.TrimSpace(subject.SessionID) == "" {
		return "", ErrInvalidSubject
	}
	if subject.Role != "" && !subject.Role.IsValid() {
		return "", ErrInvalidSubject
	}
	now := s.timeSource()

	token := gopasseto.NewToken()

	token.SetIssuedAt(now)
	token.SetNotBefore(now)
	token.SetExpiration(now.Add(s.ttl))
	token.SetSubject(subject.UserID)
	token.SetIssuer("raglibrarian")
	token.SetAudience("edge-api")
	tokenIDRaw := make([]byte, 16)
	if _, err := rand.Read(tokenIDRaw); err != nil {
		return "", ErrInvalidSubject
	}
	token.SetJti(hex.EncodeToString(tokenIDRaw))
	token.SetString("kid", s.keyID)

	if subject.Email != "" {
		token.SetString("email", subject.Email)
	}
	if subject.Role != "" {
		token.SetString("role", string(subject.Role))
	}
	token.SetString("session_id", subject.SessionID)

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
		return Claims{}, ErrInvalidToken
	}

	userID, err := token.GetSubject()
	if err != nil {
		return Claims{}, ErrInvalidToken
	}

	email, _ := token.GetString("email")
	roleStr, _ := token.GetString("role")
	role := Role(roleStr)
	if role != "" && !role.IsValid() {
		return Claims{}, ErrInvalidToken
	}

	sessionID, err := token.GetString("session_id")
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	tokenID, _ := token.GetJti()
	keyID, _ := token.GetString("kid")

	return Claims{
		UserID:    userID,
		Email:     email,
		Role:      role,
		SessionID: sessionID,
		TokenID:   tokenID,
		KeyID:     keyID,
	}, nil
}
