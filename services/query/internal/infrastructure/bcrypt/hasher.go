// Package bcrypt implements auth.PasswordHasher using golang.org/x/crypto/bcrypt.
package bcrypt

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const defaultCost = 12

type Hasher struct{ cost int }

func New() *Hasher                   { return &Hasher{cost: defaultCost} }
func NewWithCost(cost int) *Hasher   { return &Hasher{cost: cost} }

func (h *Hasher) Hash(plaintext string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plaintext), h.cost)
	if err != nil {
		return "", fmt.Errorf("bcrypt.Hash: %w", err)
	}
	return string(b), nil
}

func (h *Hasher) Compare(hash, plaintext string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)); err != nil {
		return fmt.Errorf("bcrypt.Compare: %w", err)
	}
	return nil
}
