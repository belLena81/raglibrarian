package auth

import (
	"errors"
	"fmt"

	"github.com/belLena81/raglibrarian/pkg/domain"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the bcrypt work factor. 12 balances offline-attack resistance and login latency.
const bcryptCost = 12

// HashPassword hashes plaintext with bcrypt and returns the encoded hash.
func HashPassword(plaintext string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hashed), nil
}

// CheckPassword reports whether plaintext matches the stored bcrypt hash.
// Returns ErrInvalidCredentials on any mismatch or malformed hash.
// Returns ErrHashTooShort on other bcrypt errors.
func CheckPassword(hash, plaintext string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return domain.ErrInvalidCredentials
		}
		return err
	}
	return nil
}
