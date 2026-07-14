package auth_test

import (
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
)

func newKeyPair(t *testing.T) ([]byte, []byte) {
	t.Helper()
	signingKey, verificationKey := auth.GenerateKeyPairHex()
	signer, err := auth.NewSigner(mustDecodeHex(t, signingKey), time.Hour)
	require.NoError(t, err)
	_ = signer
	return mustDecodeHex(t, signingKey), mustDecodeHex(t, verificationKey)
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err)
	return decoded
}

func testUser(t *testing.T) domain.User {
	t.Helper()
	user, err := domain.NewUser("alice@example.com", "hashed-password", domain.RoleAdmin)
	require.NoError(t, err)
	return user
}

func TestSignerAndVerifier_RoundTrip(t *testing.T) {
	privateKey, publicKey := newKeyPair(t)
	signer, err := auth.NewSigner(privateKey, time.Hour)
	require.NoError(t, err)
	verifier, err := auth.NewVerifier(publicKey)
	require.NoError(t, err)

	token, err := signer.Issue(testUser(t), "session-1")
	require.NoError(t, err)
	assert.Contains(t, token, "v4.public.")

	claims, err := verifier.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com", claims.Email)
	assert.Equal(t, domain.RoleAdmin, claims.Role)
	assert.Equal(t, "session-1", claims.SessionID)
}

func TestVerifier_RejectsWrongPublicKey(t *testing.T) {
	privateKey, _ := newKeyPair(t)
	_, otherPublicKey := newKeyPair(t)
	signer, err := auth.NewSigner(privateKey, time.Hour)
	require.NoError(t, err)
	verifier, err := auth.NewVerifier(otherPublicKey)
	require.NoError(t, err)

	token, err := signer.Issue(testUser(t))
	require.NoError(t, err)
	_, err = verifier.Validate(token)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestSignerAndVerifier_RejectInvalidKeyLengths(t *testing.T) {
	_, err := auth.NewSigner(make([]byte, 32), time.Hour)
	assert.Error(t, err)
	_, err = auth.NewVerifier(make([]byte, 64))
	assert.Error(t, err)
}
