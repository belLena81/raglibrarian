package usecase

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

type workflowPasswords struct{ hashCalls int }

func (*workflowPasswords) Validate(value string) error {
	if len(value) < 8 {
		return domain.ErrInvalidPassword
	}
	return nil
}
func (p *workflowPasswords) Hash(context.Context, string) (string, error) {
	p.hashCalls++
	return "password-hash", nil
}

type workflowIDs struct{ next int }

func (g *workflowIDs) NewID() string {
	g.next++
	return "id-" + string(rune('0'+g.next))
}

type workflowProtector struct{}

func (workflowProtector) Fingerprint(string) []byte { return make([]byte, 32) }
func (workflowProtector) SealVerification(id, _, _ string) (port.SealedEmail, error) {
	return port.SealedEmail{ID: id, MessageType: "verify_registration", KeyID: "key-v1", Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext")}, nil
}

type workflowVerificationStore struct {
	registration port.VerificationRegistration
	email        port.SealedEmail
}

func (s *workflowVerificationStore) CreateOrIgnore(_ context.Context, registration port.VerificationRegistration, email port.SealedEmail) error {
	s.registration, s.email = registration, email
	return nil
}
func (*workflowVerificationStore) RotateForResend(context.Context, string, []byte, time.Time, time.Time, port.SealedEmail) error {
	return nil
}
func (*workflowVerificationStore) Consume(context.Context, []byte, string, time.Time) (domain.User, error) {
	return domain.User{}, domain.ErrInvalidVerification
}
func (*workflowVerificationStore) CleanupExpired(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestVerificationRegistrationProducesPendingWorkWithoutSession(t *testing.T) {
	store := &workflowVerificationStore{}
	passwords := &workflowPasswords{}
	ids := &workflowIDs{}
	now := time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC)
	service := NewVerificationService(store, passwords, workflowProtector{}, workflowProtector{}, ids, fixedClock{now: now})

	err := service.Register(context.Background(), " Librarian ", " LIBRARIAN@EXAMPLE.TEST ", "password-1234", domain.RoleLibrarian)
	require.NoError(t, err)
	assert.Equal(t, "Librarian", store.registration.Name)
	assert.Equal(t, "librarian@example.test", store.registration.Email)
	assert.Equal(t, domain.RoleLibrarian, store.registration.Role)
	assert.Equal(t, now.Add(30*time.Minute), store.registration.ExpiresAt)
	assert.Len(t, store.registration.TokenHash, 32)
	assert.Equal(t, 1, passwords.hashCalls)
	assert.NotEmpty(t, store.email.Ciphertext)
}

type workflowBootstrapStore struct {
	created domain.User
	err     error
}

func (*workflowBootstrapStore) Required(context.Context) (bool, error) { return true, nil }
func (s *workflowBootstrapStore) CreateAdmin(_ context.Context, user domain.User) error {
	s.created = user
	return s.err
}

func TestBootstrapRequiresDomainSeparatedVerifier(t *testing.T) {
	const code = "0123456789abcdefghijklmnopqrstuv"
	verifier := sha256.Sum256(append([]byte(bootstrapDomain), []byte(code)...))
	store := &workflowBootstrapStore{}
	passwords := &workflowPasswords{}
	service := NewBootstrapService(store, passwords, workflowProtector{}, &workflowIDs{}, fixedClock{now: time.Now().UTC()}, verifier[:])

	assert.ErrorIs(t, service.Create(context.Background(), "Admin", "admin@example.test", "password-1234", "wrong"), domain.ErrInvalidBootstrap)
	require.NoError(t, service.Create(context.Background(), "Admin", "admin@example.test", "password-1234", code))
	assert.Equal(t, domain.RoleAdmin, store.created.Role())
	assert.Equal(t, domain.StatusActive, store.created.Status())
}

func TestApprovalRejectsInvalidPageBoundsBeforeRepository(t *testing.T) {
	service := NewApprovalService(nilApprovalStore{}, fixedClock{now: time.Now().UTC()})
	_, err := service.List(context.Background(), domain.Principal{}, 101, nil)
	assert.True(t, errors.Is(err, domain.ErrConflict))
}

type nilApprovalStore struct{}

func (nilApprovalStore) ListPending(context.Context, domain.Principal, int, *port.PendingCursor, time.Time) (port.PendingPage, error) {
	return port.PendingPage{}, nil
}
func (nilApprovalStore) Decide(context.Context, domain.Principal, string, domain.Status, time.Time) error {
	return nil
}
func (nilApprovalStore) CleanupRejected(context.Context, time.Time) (int64, error) { return 0, nil }
