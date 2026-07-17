package usecase

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

const passwordResetTTL = 10 * time.Minute

type PasswordResetService struct {
	store        port.PasswordResetStore
	passwords    PasswordHasher
	fingerprints port.Fingerprinter
	sealer       port.EmailSealer
	ids          port.IDGenerator
	clock        Clock
	key          []byte
}

func NewPasswordResetService(store port.PasswordResetStore, passwords PasswordHasher, fingerprints port.Fingerprinter, sealer port.EmailSealer, ids port.IDGenerator, clock Clock, key []byte) *PasswordResetService {
	if store == nil || passwords == nil || fingerprints == nil || sealer == nil || ids == nil || clock == nil || len(key) != 32 {
		panic("usecase: invalid password reset dependencies")
	}
	return &PasswordResetService{store: store, passwords: passwords, fingerprints: fingerprints, sealer: sealer, ids: ids, clock: clock, key: append([]byte(nil), key...)}
}

func (s *PasswordResetService) Request(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return nil
	}
	code, err := newResetCode()
	if err != nil {
		return err
	}
	message, err := s.sealer.SealPasswordReset(s.ids.NewID(), email, code)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	message.CreatedAt = now
	_, err = s.store.RequestPasswordReset(ctx, s.fingerprints.Fingerprint(email), s.mac("code", code), now.Add(passwordResetTTL), message)
	return err
}

func (s *PasswordResetService) Verify(ctx context.Context, email, code string) (string, []domain.Role, error) {
	email = normalizeEmail(email)
	if !validEmail(email) || len(code) != 6 {
		return "", nil, domain.ErrInvalidPasswordReset
	}
	grant, _, err := newRefreshToken()
	if err != nil {
		return "", nil, err
	}
	roles, err := s.store.VerifyPasswordReset(ctx, s.fingerprints.Fingerprint(email), s.mac("code", code), s.mac("grant", grant), s.clock.Now().UTC())
	if err != nil {
		return "", nil, err
	}
	return grant, roles, nil
}

func (s *PasswordResetService) Complete(ctx context.Context, grant, roleValue, plaintext string) error {
	if err := s.passwords.Validate(plaintext); err != nil {
		return domain.ErrInvalidPasswordReset
	}
	hash, err := s.passwords.Hash(ctx, plaintext)
	if err != nil {
		return err
	}
	role := domain.Role(roleValue)
	if !role.IsValid() {
		return domain.ErrInvalidPasswordReset
	}
	return s.store.CompletePasswordReset(ctx, s.mac("grant", grant), role, hash, s.clock.Now().UTC())
}

func (s *PasswordResetService) mac(kind, value string) []byte {
	h := hmac.New(sha256.New, s.key)
	_, _ = h.Write([]byte("raglibrarian/password-reset/" + kind + "/v1\x00"))
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}
func newResetCode() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	return fmt.Sprintf("%06d", n%1000000), nil
}
