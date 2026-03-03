package paseto_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appauth "github.com/yourname/raglibrarian/services/query/internal/application/auth"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
	"github.com/yourname/raglibrarian/services/query/internal/infrastructure/paseto"
	"github.com/yourname/raglibrarian/pkg/tokenverifier"
)

var testKey = make([]byte, 32) // 32 zero bytes — test only, never production

func newIssuer(t *testing.T, ttl time.Duration) *paseto.Issuer {
	t.Helper()
	// Use a deterministic non-zero key for tests
	key := make([]byte, 32)
	for i := range key { key[i] = byte(i + 1) }
	i, err := paseto.NewIssuer(key, ttl)
	require.NoError(t, err)
	return i
}

func TestNewIssuer_KeyTooShort(t *testing.T) {
	_, err := paseto.NewIssuer([]byte("tooshort"), time.Minute)
	assert.Error(t, err)
}

func TestNewIssuer_ZeroTTL(t *testing.T) {
	_, err := paseto.NewIssuer(testKey, 0)
	assert.Error(t, err)
}

func TestIssueAndVerify_RoundTrip(t *testing.T) {
	issuer := newIssuer(t, 15*time.Minute)
	id := uuid.New()

	tokenStr, err := issuer.Issue(appauth.TokenClaims{
		UserID: id,
		Email:  "ada@example.com",
		Role:   user.RoleLibrarian,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, tokenStr)

	claims, err := issuer.Verify(context.Background(), tokenStr)
	require.NoError(t, err)
	assert.Equal(t, id, claims.UserID)
	assert.Equal(t, "ada@example.com", claims.Email)
	assert.Equal(t, tokenverifier.RoleLibrarian, claims.Role)
}

func TestVerify_ExpiredToken(t *testing.T) {
	issuer := newIssuer(t, -time.Second)
	tokenStr, err := issuer.Issue(appauth.TokenClaims{
		UserID: uuid.New(), Email: "ada@example.com", Role: user.RoleReader,
	})
	require.NoError(t, err)

	_, err = issuer.Verify(context.Background(), tokenStr)
	assert.ErrorIs(t, err, paseto.ErrTokenExpired)
}

func TestVerify_TamperedToken(t *testing.T) {
	issuer := newIssuer(t, 15*time.Minute)
	tokenStr, err := issuer.Issue(appauth.TokenClaims{
		UserID: uuid.New(), Email: "ada@example.com", Role: user.RoleReader,
	})
	require.NoError(t, err)

	tampered := tokenStr[:len(tokenStr)-4] + "XXXX"
	_, err = issuer.Verify(context.Background(), tampered)
	assert.Error(t, err)
}

func TestVerify_WrongKey(t *testing.T) {
	issuerA := newIssuer(t, 15*time.Minute)

	keyB := make([]byte, 32)
	for i := range keyB { keyB[i] = 0xFF }
	issuerB, _ := paseto.NewIssuer(keyB, time.Minute)

	tokenStr, _ := issuerA.Issue(appauth.TokenClaims{
		UserID: uuid.New(), Email: "ada@example.com", Role: user.RoleReader,
	})
	_, err := issuerB.Verify(context.Background(), tokenStr)
	assert.Error(t, err)
}
