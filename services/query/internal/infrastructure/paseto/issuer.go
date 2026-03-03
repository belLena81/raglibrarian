// Package paseto implements the auth.TokenIssuer and tokenverifier.Verifier ports
// using PASETO v2 local (symmetric encryption, XChaCha20-Poly1305).
// Only the Query Service holds the symmetric key; other services call
// AuthService.ValidateToken over gRPC (see pkg/tokenverifier).
package paseto

import (
	"context"
	"errors"
	"fmt"
	"time"

	"aidanwoods.dev/go-paseto"
	"github.com/google/uuid"
	appauth "github.com/yourname/raglibrarian/services/query/internal/application/auth"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
	"github.com/yourname/raglibrarian/pkg/tokenverifier"
)

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrTokenExpired  = errors.New("paseto: token has expired")
	ErrTokenInvalid  = errors.New("paseto: token is invalid")
)

// ── Issuer / Verifier ─────────────────────────────────────────────────────────

// Issuer signs PASETO v2 local tokens (symmetric).
// It also implements tokenverifier.Verifier so the Query Service can verify
// tokens locally without a round-trip gRPC call to itself.
type Issuer struct {
	key paseto.V2SymmetricKey
	ttl time.Duration
}

// NewIssuer creates an Issuer with a 32-byte symmetric key.
// key must be exactly 32 bytes (PASETO v2 local requirement).
func NewIssuer(key []byte, ttl time.Duration) (*Issuer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("paseto: key must be exactly 32 bytes, got %d", len(key))
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("paseto: TTL must be positive")
	}
	var k paseto.V2SymmetricKey
	copy(k[:], key)
	return &Issuer{key: k, ttl: ttl}, nil
}

// Issue encrypts a new PASETO token embedding the provided claims.
// Implements auth.TokenIssuer.
func (i *Issuer) Issue(c appauth.TokenClaims) (string, error) {
	token := paseto.NewToken()
	now := time.Now().UTC()

	token.SetIssuedAt(now)
	token.SetExpiration(now.Add(i.ttl))
	token.SetIssuer("raglibrarian")
	token.SetSubject(c.UserID.String())

	if err := token.Set("uid", c.UserID.String()); err != nil {
		return "", fmt.Errorf("paseto.Issue: set uid: %w", err)
	}
	if err := token.Set("email", c.Email); err != nil {
		return "", fmt.Errorf("paseto.Issue: set email: %w", err)
	}
	if err := token.Set("role", string(c.Role)); err != nil {
		return "", fmt.Errorf("paseto.Issue: set role: %w", err)
	}

	encrypted, err := token.V2Encrypt(i.key)
	if err != nil {
		return "", fmt.Errorf("paseto.Issue: encrypt: %w", err)
	}
	return encrypted, nil
}

// Verify decrypts and validates a PASETO token string.
// Implements tokenverifier.Verifier for local (Query Service internal) use.
func (i *Issuer) Verify(_ context.Context, tokenStr string) (*tokenverifier.Claims, error) {
	parser := paseto.NewParserWithoutExpiryCheck() // we check expiry manually below
	token, err := parser.ParseV2Local(i.key, tokenStr, nil)
	if err != nil {
		return nil, ErrTokenInvalid
	}

	// Manual expiry check so we return a typed error
	exp, err := token.GetExpiration()
	if err != nil || time.Now().UTC().After(exp) {
		return nil, ErrTokenExpired
	}

	var uid, email, role string
	if err = token.Get("uid", &uid); err != nil {
		return nil, ErrTokenInvalid
	}
	if err = token.Get("email", &email); err != nil {
		return nil, ErrTokenInvalid
	}
	if err = token.Get("role", &role); err != nil {
		return nil, ErrTokenInvalid
	}

	id, err := uuid.Parse(uid)
	if err != nil {
		return nil, ErrTokenInvalid
	}

	return &tokenverifier.Claims{
		UserID: id,
		Email:  email,
		Role:   tokenverifier.Role(role),
	}, nil
}

// ParsedClaims mirrors tokenverifier.Claims for infrastructure-internal use.
// Callers outside the infrastructure layer should use tokenverifier.Claims.
type ParsedClaims = tokenverifier.Claims

// ── DomainRole helper ─────────────────────────────────────────────────────────

// DomainRole converts a tokenverifier.Role back to a domain user.Role.
// Used by gRPC interceptors that receive claims from the verifier.
func DomainRole(r tokenverifier.Role) (user.Role, error) {
	dr := user.Role(r)
	if !dr.IsValid() {
		return "", fmt.Errorf("paseto: unknown role %q", r)
	}
	return dr, nil
}
