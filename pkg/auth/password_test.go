package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/auth"
)

func TestHashPassword_ProducesNonEmptyHash(t *testing.T) {
	hash, err := auth.HashPassword("s3cr3t!")
	require.NoError(t, err)
	assert.NotEmpty(t, hash)
}

func TestHashPassword_TwoCallsDifferentHashes(t *testing.T) {
	// bcrypt generates a random salt — same input must never produce the same output.
	a, _ := auth.HashPassword("same-password")
	b, _ := auth.HashPassword("same-password")
	assert.NotEqual(t, a, b)
}

func TestCheckPassword_CorrectPassword_NoError(t *testing.T) {
	hash, err := auth.HashPassword("correct-horse-battery-staple")
	require.NoError(t, err)

	err = auth.CheckPassword(hash, "correct-horse-battery-staple")
	assert.NoError(t, err)
}

func TestCheckPassword_WrongPassword_ReturnsError(t *testing.T) {
	hash, _ := auth.HashPassword("correct")

	err := auth.CheckPassword(hash, "incorrect")
	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestCheckPassword_EmptyPlaintext_ReturnsError(t *testing.T) {
	hash, _ := auth.HashPassword("nonempty")

	err := auth.CheckPassword(hash, "")
	assert.Error(t, err)
}
