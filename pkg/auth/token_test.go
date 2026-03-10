package auth_test

import (
	"testing"
	"time"

	gopasseto "aidanwoods.dev/go-paseto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
)

// testKey is a 32-byte all-zeros key. Acceptable in tests only — never in prod.
var testKey = make([]byte, 32)

func newTestIssuer(t *testing.T) *auth.Issuer {
	t.Helper()
	issuer, err := auth.NewIssuer(testKey, time.Hour)
	require.NoError(t, err)
	return issuer
}

func testUser(t *testing.T) domain.User {
	t.Helper()
	u, err := domain.NewUser("alice@example.com", "hashed-pw", domain.RoleAdmin)
	require.NoError(t, err)
	return u
}

// ── NewIssuer ─────────────────────────────────────────────────────────────────

func TestNewIssuer_WrongKeyLength(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
	}{
		{"empty", []byte{}},
		{"too short (16 bytes)", make([]byte, 16)},
		{"too long (64 bytes)", make([]byte, 64)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := auth.NewIssuer(tc.key, time.Hour)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "32 bytes")
		})
	}
}

func TestNewIssuer_CorrectKeyLength(t *testing.T) {
	_, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
}

// ── Issue ─────────────────────────────────────────────────────────────────────

func TestIssuer_Issue_ReturnsNonEmptyToken(t *testing.T) {
	issuer := newTestIssuer(t)
	token, err := issuer.Issue(testUser(t))
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

func TestIssuer_Issue_TokenHasV4LocalPrefix(t *testing.T) {
	// Every token from aidanwoods.dev/go-paseto v4 local starts with "v4.local."
	// This confirms the correct algorithm version is in use.
	issuer := newTestIssuer(t)
	token, err := issuer.Issue(testUser(t))
	require.NoError(t, err)
	assert.True(t, len(token) > 9 && token[:9] == "v4.local.",
		"expected v4.local. prefix, got: %s", token)
}

func TestIssuer_Issue_TwoCallsProduceDifferentTokens(t *testing.T) {
	// v4 local generates a random nonce per encrypt call — same plaintext
	// must never produce the same ciphertext.
	issuer := newTestIssuer(t)
	u := testUser(t)
	a, err := issuer.Issue(u)
	require.NoError(t, err)
	b, err := issuer.Issue(u)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}

// ── Validate — happy path ─────────────────────────────────────────────────────

func TestIssuer_Validate_ValidToken_ReturnsClaims(t *testing.T) {
	issuer := newTestIssuer(t)
	u := testUser(t)

	token, err := issuer.Issue(u)
	require.NoError(t, err)

	claims, err := issuer.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, u.ID(), claims.UserID)
	assert.Equal(t, u.Email(), claims.Email)
	assert.Equal(t, u.Role(), claims.Role)
}

func TestIssuer_Validate_ReaderRole_RoundTrips(t *testing.T) {
	issuer := newTestIssuer(t)
	u, err := domain.NewUser("bob@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)

	token, err := issuer.Issue(u)
	require.NoError(t, err)

	claims, err := issuer.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, domain.RoleReader, claims.Role)
	assert.False(t, claims.Role.CanWrite())
}

// ── Validate — error path ─────────────────────────────────────────────────────

func TestIssuer_Validate_TamperedToken_ReturnsErrInvalidToken(t *testing.T) {
	issuer := newTestIssuer(t)
	token, err := issuer.Issue(testUser(t))
	require.NoError(t, err)

	// Corrupt the last byte — breaks the AEAD authentication tag.
	tampered := token[:len(token)-1] + "X"
	if tampered == token {
		tampered = token[:len(token)-1] + "Y"
	}

	_, err = issuer.Validate(tampered)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestIssuer_Validate_GarbageString_ReturnsErrInvalidToken(t *testing.T) {
	issuer := newTestIssuer(t)
	_, err := issuer.Validate("not.a.paseto.token")
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestIssuer_Validate_EmptyString_ReturnsErrInvalidToken(t *testing.T) {
	issuer := newTestIssuer(t)
	_, err := issuer.Validate("")
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestIssuer_Validate_ExpiredToken_ReturnsErrInvalidToken(t *testing.T) {
	// Issue with a 1 ns TTL then sleep 1 ms — the article's expired-token
	// demo uses the same approach.
	issuer, err := auth.NewIssuer(testKey, time.Nanosecond)
	require.NoError(t, err)

	token, err := issuer.Issue(testUser(t))
	require.NoError(t, err)

	time.Sleep(time.Millisecond)

	_, err = issuer.Validate(token)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestIssuer_Validate_WrongKey_ReturnsErrInvalidToken(t *testing.T) {
	issuerA, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)

	keyB := make([]byte, 32)
	keyB[0] = 0xFF
	issuerB, err := auth.NewIssuer(keyB, time.Hour)
	require.NoError(t, err)

	token, err := issuerA.Issue(testUser(t))
	require.NoError(t, err)

	_, err = issuerB.Validate(token)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestIssuer_Validate_WrongIssuer_ReturnsErrInvalidToken(t *testing.T) {
	// Build a token with iss="other-service" directly via go-paseto —
	// the Issuer.Validate parser enforces IssuedBy("raglibrarian") so
	// this must be rejected even though the key and all other claims are valid.
	rawKey := make([]byte, 32)
	issuer, err := auth.NewIssuer(rawKey, time.Hour)
	require.NoError(t, err)

	key, err := gopasseto.V4SymmetricKeyFromBytes(rawKey)
	require.NoError(t, err)

	spoofed := gopasseto.NewToken()
	spoofed.SetIssuedAt(time.Now())
	spoofed.SetNotBefore(time.Now())
	spoofed.SetExpiration(time.Now().Add(time.Hour))
	spoofed.SetSubject("user-1")
	spoofed.SetIssuer("other-service") // wrong issuer
	spoofed.SetString("email", "x@y.com")
	spoofed.SetString("role", "reader")
	tokenStr := spoofed.V4Encrypt(key, nil)

	_, err = issuer.Validate(tokenStr)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}
