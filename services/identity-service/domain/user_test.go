package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

func TestNewUserNormalizesOnlyAtApplicationBoundary(t *testing.T) {
	user, err := domain.NewUser("reader@example.com", "hash", domain.RoleReader)
	require.NoError(t, err)
	assert.NotEmpty(t, user.ID())
	assert.Equal(t, domain.RoleReader, user.Role())
}

func TestNewUserRejectsInvalidState(t *testing.T) {
	_, err := domain.NewUser("invalid", "hash", domain.RoleReader)
	assert.ErrorIs(t, err, domain.ErrInvalidEmail)
	_, err = domain.NewUser("a@example.com", "", domain.RoleReader)
	assert.ErrorIs(t, err, domain.ErrEmptyPasswordHash)
	_, err = domain.NewUser("a@example.com", "hash", domain.Role("owner"))
	assert.ErrorIs(t, err, domain.ErrInvalidRole)
}
