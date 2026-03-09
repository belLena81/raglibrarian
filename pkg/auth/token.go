// Package auth provides PASETO v4 local (symmetric) token issuance and
// validation for raglibrarian.
//
// Implementation follows https://oneuptime.com/blog/post/2026-01-07-go-paseto-tokens/view
// using aidanwoods.dev/go-paseto — the library recommended in that article.
//
// # Why PASETO v4 local?
//
//   - v4 is the latest PASETO version with the best security margins.
//   - "local" means symmetric encryption (XChaCha20-Poly1305 + BLAKE2b):
//     the payload is encrypted AND authenticated — not just signed.
//     A client cannot read or tamper with their own token claims.
//   - No algorithm negotiation: the algorithm is fixed by the "v4.local." prefix.
//     The JWT "alg:none" and RS256/HS256 confusion class of attacks cannot exist.
//
// # Key management
//
// The symmetric key is 32 bytes, hex-encoded in AUTH_SECRET_KEY.
// It is loaded once at startup via config.Load() and passed here as []byte.
// Key export/import uses V4SymmetricKeyFromBytes / ExportBytes as shown in
// the article's "Key Management Best Practices" section.
package auth

import (
	"errors"
	"fmt"
	"time"

	gopasseto "aidanwoods.dev/go-paseto"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// Claims is the verified, decoded payload stored in request context after
// successful token validation. Handlers and middleware read identity from here —
// they never touch the raw token string.
type Claims struct {
	UserID string
	Email  string
	Role   domain.Role
}

// Issuer creates and validates PASETO v4 local tokens with a symmetric key.
// Safe for concurrent use — all state is read-only after construction.
type Issuer struct {
	key        gopasseto.V4SymmetricKey
	ttl        time.Duration
	timeSource func() time.Time // injectable so tests can control "now"
}

// NewIssuer constructs an Issuer from a 32-byte symmetric key.
//
// The key is imported via V4SymmetricKeyFromBytes, exactly as shown in
// the article's SecureKeyLoader / KeyManager examples. This means the key
// bytes come from config (hex-decoded from AUTH_SECRET_KEY) rather than
// being generated at runtime — the same key survives restarts.
//
// Returns an error (not a panic) so main() can log and exit cleanly when
// the key is missing or the wrong length.
func NewIssuer(rawKey []byte, ttl time.Duration) (*Issuer, error) {
	if len(rawKey) != 32 {
		return nil, fmt.Errorf(
			"auth: symmetric key must be exactly 32 bytes, got %d", len(rawKey),
		)
	}

	// V4SymmetricKeyFromBytes is the article's recommended way to load a key
	// from stored bytes (env var, secrets manager, etc.).
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

// Issue mints a new PASETO v4 local token for the given user.
//
// The token structure follows the article's demonstrateLocalV4 pattern:
//   - Standard claims: iat, nbf, exp, sub, iss
//   - Custom string claims: "email", "role"
//
// The payload is fully encrypted — the client receives an opaque string.
func (is *Issuer) Issue(user domain.User) (string, error) {
	now := is.timeSource()

	token := gopasseto.NewToken()

	// Standard PASETO claims (as used throughout the article).
	token.SetIssuedAt(now)
	token.SetNotBefore(now)
	token.SetExpiration(now.Add(is.ttl))
	token.SetSubject(user.ID())
	token.SetIssuer("raglibrarian")

	// Custom claims — typed accessors used in Validate below.
	token.SetString("email", user.Email())
	token.SetString("role", string(user.Role()))

	// V4Encrypt with nil footer — footer is optional metadata (e.g. key-id).
	// We do not use key rotation in this iteration, so footer is empty.
	encrypted := token.V4Encrypt(is.key, nil)
	return encrypted, nil
}

// Validate decrypts and verifies a PASETO v4 local token string.
//
// The parser setup mirrors the article's NewTokenValidator / demonstrateLocalV4:
//   - NotExpired() — rejects tokens past their exp claim
//   - ValidAt(now) — rejects tokens before their nbf claim
//   - IssuedBy("raglibrarian") — rejects tokens from other issuers
//
// Returns ErrInvalidToken for any failure — expired, tampered, wrong key,
// or malformed. The error is intentionally opaque to prevent oracle attacks.
func (is *Issuer) Validate(tokenStr string) (Claims, error) {
	parser := gopasseto.NewParser()

	// Validation rules from the article's "Token Validation and Rules" section.
	parser.AddRule(gopasseto.NotExpired())
	parser.AddRule(gopasseto.ValidAt(is.timeSource()))
	parser.AddRule(gopasseto.IssuedBy("raglibrarian"))

	token, err := parser.ParseV4Local(is.key, tokenStr, nil)
	if err != nil {
		// All failures collapse to ErrInvalidToken — callers map this to 401.
		return Claims{}, ErrInvalidToken
	}

	// Extract custom claims using the typed GetString accessor from the article.
	userID, err := token.GetSubject()
	if err != nil {
		return Claims{}, ErrInvalidToken
	}

	email, err := token.GetString("email")
	if err != nil {
		return Claims{}, ErrInvalidToken
	}

	roleStr, err := token.GetString("role")
	if err != nil {
		return Claims{}, ErrInvalidToken
	}

	role := domain.Role(roleStr)
	if !role.IsValid() {
		return Claims{}, ErrInvalidToken
	}

	return Claims{
		UserID: userID,
		Email:  email,
		Role:   role,
	}, nil
}

// ErrInvalidToken is returned by Validate for any untrustworthy token.
// Intentionally opaque — callers map it to HTTP 401 Unauthorized.
var ErrInvalidToken = errors.New("auth: token is invalid or expired")
