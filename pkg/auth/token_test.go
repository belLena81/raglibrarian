package auth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
)

func TestIssueAndValidatePrimitiveSubject(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	subject := auth.Subject{UserID: "user-1", Email: "reader@example.com", Role: auth.RoleReader, SessionID: "session-1"}
	token, err := issuer.Issue(subject)
	require.NoError(t, err)
	claims, err := issuer.Validate(token)
	require.NoError(t, err)
	assert.Equal(t, subject.UserID, claims.UserID)
	assert.Equal(t, subject.Role, claims.Role)
	assert.Equal(t, subject.SessionID, claims.SessionID)
}

func TestValidateRejectsTamperedToken(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	_, err = issuer.Validate("v4.public.invalid")
	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestRoleContractRejectsUnknownRole(t *testing.T) {
	assert.False(t, auth.Role("owner").IsValid())
}

func TestIssueRejectsIncompleteSubject(t *testing.T) {
	issuer, err := auth.NewIssuer(make([]byte, 32), time.Hour)
	require.NoError(t, err)
	_, err = issuer.Issue(auth.Subject{UserID: "user-1", Email: "reader@example.com", Role: auth.RoleReader})
	assert.ErrorIs(t, err, auth.ErrInvalidSubject)
}
