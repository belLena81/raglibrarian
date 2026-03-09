package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the work factor used when hashing passwords.
// 12 is the current community recommendation: expensive enough to resist
// offline brute-force, fast enough that login latency stays under ~300ms
// on modern hardware.
// Do NOT lower this value below 12 in production. Increasing it is safe
// (old hashes produced at cost 12 continue to verify correctly).
const bcryptCost = 12

// HashPassword hashes a plaintext password using bcrypt.
// Returns the encoded hash string suitable for storage.
func HashPassword(plaintext string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(hashed), nil
}

// CheckPassword reports whether plaintext matches the stored bcrypt hash.
// Maps bcrypt.ErrMismatchedHashAndPassword to domain.ErrInvalidPassword so
// callers never need to import bcrypt directly.
func CheckPassword(hash, plaintext string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
	if err != nil {
		// Return the domain sentinel regardless of whether the error is
		// "mismatch" or "malformed hash" — both mean authentication fails.
		return fmt.Errorf("%w: %v", ErrInvalidCredentials, err)
	}
	return nil
}

// ErrInvalidCredentials is returned when a password does not match its hash.
// It is separate from domain.ErrInvalidPassword to keep the auth package
// self-contained and the error chain unambiguous.
var ErrInvalidCredentials = fmt.Errorf("auth: invalid credentials")
