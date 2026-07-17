// Package password implements Identity-owned password policy and hashing.
package password

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

const (
	minBytes   = 6
	maxBytes   = 72
	bcryptCost = 12
)

// BcryptHasher validates and hashes Identity passwords with bcrypt.
type BcryptHasher struct{}

// Validate applies the password bounds before expensive bcrypt work.
func (BcryptHasher) Validate(plaintext string) error {
	if len(plaintext) < minBytes || len(plaintext) > maxBytes {
		return domain.ErrInvalidPassword
	}
	return nil
}

// Hash returns a bcrypt hash after validation.
func (h BcryptHasher) Hash(_ context.Context, plaintext string) (string, error) {
	if err := h.Validate(plaintext); err != nil {
		return "", err
	}
	encoded, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(encoded), nil
}

// Compare checks a password only when it satisfies the current password policy.
func (BcryptHasher) Compare(_ context.Context, hash, plaintext string) error {
	if len(plaintext) < minBytes || len(plaintext) > maxBytes {
		return domain.ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)); err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) || errors.Is(err, bcrypt.ErrHashTooShort) {
			return domain.ErrInvalidCredentials
		}
		return fmt.Errorf("compare password: %w", err)
	}
	return nil
}

// LimitedHasher bounds expensive password work and honors caller cancellation.
type LimitedHasher struct {
	next interface {
		Validate(string) error
		Hash(context.Context, string) (string, error)
		Compare(context.Context, string, string) error
	}
	slots chan struct{}
}

// NewLimitedHasher decorates a hasher with a cancellation-aware concurrency bound.
func NewLimitedHasher(next interface {
	Validate(string) error
	Hash(context.Context, string) (string, error)
	Compare(context.Context, string, string) error
}, concurrency int) *LimitedHasher {
	if next == nil || concurrency < 1 {
		panic("password: invalid limiter dependencies")
	}
	return &LimitedHasher{next: next, slots: make(chan struct{}, concurrency)}
}

// Validate delegates inexpensive policy validation.
func (h *LimitedHasher) Validate(plaintext string) error { return h.next.Validate(plaintext) }

// Hash reserves one bounded work slot.
func (h *LimitedHasher) Hash(ctx context.Context, plaintext string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return h.next.Hash(ctx, plaintext)
}

// Compare reserves one bounded work slot.
func (h *LimitedHasher) Compare(ctx context.Context, hash, plaintext string) error {
	if err := h.acquire(ctx); err != nil {
		return err
	}
	defer h.release()
	return h.next.Compare(ctx, hash, plaintext)
}
func (h *LimitedHasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (h *LimitedHasher) release() { <-h.slots }
