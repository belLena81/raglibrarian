package password_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/password"
)

type observingHasher struct{ active, maximum atomic.Int32 }

func (*observingHasher) Validate(string) error { return nil }
func (h *observingHasher) Hash(context.Context, string) (string, error) {
	h.observe()
	return "hash", nil
}
func (h *observingHasher) Compare(context.Context, string, string) error { h.observe(); return nil }
func (h *observingHasher) observe() {
	current := h.active.Add(1)
	defer h.active.Add(-1)
	for {
		observed := h.maximum.Load()
		if current <= observed || h.maximum.CompareAndSwap(observed, current) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond)
}

func TestLimitedHasherBoundsConcurrentWork(t *testing.T) {
	next := &observingHasher{}
	limited := password.NewLimitedHasher(next, 2)
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() { defer group.Done(); _, _ = limited.Hash(context.Background(), "password-1234") }()
	}
	group.Wait()
	assert.LessOrEqual(t, next.maximum.Load(), int32(2))
	assert.Zero(t, next.active.Load())
}

func TestBcryptHasherAcceptsLegacyShortPasswordForVerification(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	require.NoError(t, err)

	err = (password.BcryptHasher{}).Compare(context.Background(), string(hash), "secret")

	require.NoError(t, err)
}

func TestBcryptHasherKeepsRegistrationPasswordMinimum(t *testing.T) {
	_, err := (password.BcryptHasher{}).Hash(context.Background(), "secret")

	assert.ErrorIs(t, err, domain.ErrInvalidPassword)
}

func TestBcryptHasherRejectsInvalidVerificationBounds(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("password-1234"), bcrypt.MinCost)
	require.NoError(t, err)

	for name, plaintext := range map[string]string{
		"empty":     "",
		"oversized": string(make([]byte, 73)),
	} {
		t.Run(name, func(t *testing.T) {
			err := (password.BcryptHasher{}).Compare(context.Background(), string(hash), plaintext)
			assert.ErrorIs(t, err, domain.ErrInvalidCredentials)
		})
	}
}
