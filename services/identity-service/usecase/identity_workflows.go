package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

const (
	verificationTTL       = 30 * time.Minute
	verificationRetention = 24 * time.Hour
	resendCooldown        = 10 * time.Minute
	bootstrapDomain       = "raglibrarian/admin-bootstrap/v1\x00"
)

// VerificationService coordinates registration, email verification, resend,
// and expiry cleanup without depending on transport or persistence details.
type VerificationService struct {
	store        port.VerificationStore
	passwords    PasswordHasher
	sealer       port.EmailSealer
	fingerprints port.Fingerprinter
	ids          port.IDGenerator
	clock        Clock
}

// NewVerificationService constructs the registration-verification workflow.
func NewVerificationService(
	store port.VerificationStore,
	passwords PasswordHasher,
	sealer port.EmailSealer,
	fingerprints port.Fingerprinter,
	ids port.IDGenerator,
	clock Clock,
) *VerificationService {
	if store == nil || passwords == nil || sealer == nil || fingerprints == nil || ids == nil || clock == nil {
		panic("usecase: invalid verification dependencies")
	}
	return &VerificationService{store: store, passwords: passwords, sealer: sealer, fingerprints: fingerprints, ids: ids, clock: clock}
}

// Register validates an account request and schedules a single-use ownership
// verification. Account creation occurs only when that token is consumed.
func (s *VerificationService) Register(ctx context.Context, name, email, plaintext string, role domain.Role) error {
	name = strings.TrimSpace(name)
	email = normalizeEmail(email)
	statuses := map[domain.Role]domain.Status{
		domain.RoleReader:    domain.StatusActive,
		domain.RoleLibrarian: domain.StatusPending,
	}
	accountStatus, ok := statuses[role]
	if !ok {
		return domain.ErrInvalidRole
	}
	if _, err := domain.NewUnverifiedUser("validation", name, email, nil, "validation", role, accountStatus, s.clock.Now()); err != nil {
		return err
	}
	if err := s.passwords.Validate(plaintext); err != nil {
		return err
	}
	hash, err := s.passwords.Hash(ctx, plaintext)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return err
	}
	registration := port.VerificationRegistration{
		ID:               s.ids.NewID(),
		TokenHash:        tokenHash,
		Name:             name,
		Email:            email,
		EmailFingerprint: s.fingerprints.Fingerprint(email),
		PasswordHash:     hash,
		Role:             role,
		ExpiresAt:        now.Add(verificationTTL),
		CreatedAt:        now,
	}
	messageID := s.ids.NewID()
	sealed, err := s.sealer.SealVerification(messageID, email, token)
	if err != nil {
		return err
	}
	sealed.CreatedAt = now
	return s.store.CreateOrIgnore(ctx, registration, sealed)
}

// Resend rotates verification material subject to repository-enforced cooldown
// while keeping unknown addresses indistinguishable.
func (s *VerificationService) Resend(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if !validEmail(email) {
		return nil
	}
	token, tokenHash, err := newOpaqueToken()
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	messageID := s.ids.NewID()
	sealed, err := s.sealer.SealVerification(messageID, email, token)
	if err != nil {
		return err
	}
	sealed.CreatedAt = now
	return s.store.RotateForResend(ctx, email, tokenHash, now.Add(verificationTTL), now.Add(-resendCooldown), sealed)
}

// Verify consumes a single-use opaque token and returns the verified account.
func (s *VerificationService) Verify(ctx context.Context, token string) (domain.User, error) {
	if len(token) != 43 {
		return domain.User{}, domain.ErrInvalidVerification
	}
	hash := sha256.Sum256([]byte(token))
	return s.store.Consume(ctx, hash[:], s.ids.NewID(), s.clock.Now().UTC())
}

// Cleanup removes verification registrations beyond their retention window.
func (s *VerificationService) Cleanup(ctx context.Context) (int64, error) {
	return s.store.CleanupExpired(ctx, s.clock.Now().UTC().Add(-verificationRetention))
}

// BootstrapService owns the one-time, verifier-protected administrator setup.
type BootstrapService struct {
	store        port.BootstrapStore
	passwords    PasswordHasher
	fingerprints port.Fingerprinter
	ids          port.IDGenerator
	clock        Clock
	verifier     []byte
}

// NewBootstrapService constructs the first-administrator workflow and retains a
// private copy of the bootstrap verifier.
func NewBootstrapService(store port.BootstrapStore, passwords PasswordHasher, fingerprints port.Fingerprinter, ids port.IDGenerator, clock Clock, verifier []byte) *BootstrapService {
	if store == nil || passwords == nil || fingerprints == nil || ids == nil || clock == nil {
		panic("usecase: invalid bootstrap dependencies")
	}
	return &BootstrapService{store: store, passwords: passwords, fingerprints: fingerprints, ids: ids, clock: clock, verifier: append([]byte(nil), verifier...)}
}

// Required reports whether creation of the first administrator remains available.
func (s *BootstrapService) Required(ctx context.Context) (bool, error) {
	return s.store.Required(ctx)
}

// Create validates bootstrap proof and delegates the serialized administrator
// creation transition to the owning repository.
func (s *BootstrapService) Create(ctx context.Context, name, email, plaintext, code string) error {
	if len(s.verifier) != sha256.Size || !s.validCode(code) {
		return domain.ErrInvalidBootstrap
	}
	if err := s.passwords.Validate(plaintext); err != nil {
		return domain.ErrInvalidBootstrap
	}
	hash, err := s.passwords.Hash(ctx, plaintext)
	if err != nil {
		return err
	}
	now := s.clock.Now().UTC()
	user, err := domain.NewVerifiedUser(
		s.ids.NewID(), name, email, s.fingerprints.Fingerprint(email), hash,
		domain.RoleAdmin, domain.StatusActive, now, now,
	)
	if err != nil {
		return domain.ErrInvalidBootstrap
	}
	return s.store.CreateAdmin(ctx, user)
}

func (s *BootstrapService) validCode(code string) bool {
	sum := sha256.Sum256(append([]byte(bootstrapDomain), []byte(code)...))
	return subtle.ConstantTimeCompare(sum[:], s.verifier) == 1
}

// ApprovalService authorizes and coordinates pending-librarian review.
type ApprovalService struct {
	store port.ApprovalStore
	clock Clock
}

// NewApprovalService constructs the librarian-approval workflow.
func NewApprovalService(store port.ApprovalStore, clock Clock) *ApprovalService {
	if store == nil || clock == nil {
		panic("usecase: invalid approval dependencies")
	}
	return &ApprovalService{store: store, clock: clock}
}

// List returns a bounded page of pending librarians for an authorized actor.
func (s *ApprovalService) List(ctx context.Context, actor domain.Principal, size int, cursor *port.PendingCursor) (port.PendingPage, error) {
	if size == 0 {
		size = 25
	}
	if size < 1 || size > 100 {
		return port.PendingPage{}, domain.ErrConflict
	}
	return s.store.ListPending(ctx, actor, size, cursor, s.clock.Now().UTC())
}

// Approve records a final activation decision for a pending librarian.
func (s *ApprovalService) Approve(ctx context.Context, actor domain.Principal, userID string) error {
	return s.store.Decide(ctx, actor, userID, domain.StatusActive, s.clock.Now().UTC())
}

// Reject records a final rejection decision for a pending librarian.
func (s *ApprovalService) Reject(ctx context.Context, actor domain.Principal, userID string) error {
	return s.store.Decide(ctx, actor, userID, domain.StatusRejected, s.clock.Now().UTC())
}

// CleanupRejected removes rejected accounts beyond their retention window.
func (s *ApprovalService) CleanupRejected(ctx context.Context) (int64, error) {
	return s.store.CleanupRejected(ctx, s.clock.Now().UTC().Add(-90*24*time.Hour))
}

func newOpaqueToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func validEmail(email string) bool {
	at := strings.LastIndex(email, "@")
	return at > 0 && at < len(email)-1
}
