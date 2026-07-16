package auth_test

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
)

func TestIssueAndValidatePrimitiveSubject(t *testing.T) {
	signer, verifier := testKeyPair(t)
	subject := auth.Subject{UserID: "user-1", Email: "reader@example.com", Role: auth.RoleReader, SessionID: "session-1"}
	token, err := signer.Issue(subject)
	require.NoError(t, err)
	claims, err := verifier.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, subject.UserID, claims.UserID)
	assert.Equal(t, subject.Role, claims.Role)
	assert.Equal(t, subject.SessionID, claims.SessionID)
}

func TestValidateRejectsTamperedToken(t *testing.T) {
	_, verifier := testKeyPair(t)
	_, err := verifier.Validate("v4.public.invalid")
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestRoleContractRejectsUnknownRole(t *testing.T) {
	assert.False(t, auth.Role("owner").IsValid())
}

func TestIssueRejectsIncompleteSubject(t *testing.T) {
	signer, _ := testKeyPair(t)
	_, err := signer.Issue(auth.Subject{UserID: "user-1", Email: "reader@example.com", Role: auth.RoleReader})
	assert.ErrorIs(t, err, auth.ErrInvalidSubject)
}

func testKeyPair(t *testing.T) (*auth.Signer, *auth.Verifier) {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	signer, err := auth.NewSigner(privateKey, time.Hour)
	require.NoError(t, err)
	verifier, err := auth.NewVerifier(privateKey.Public().(ed25519.PublicKey))
	require.NoError(t, err)
	return signer, verifier
}
