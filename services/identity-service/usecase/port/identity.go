package port

import (
	"context"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

// VerificationRegistration carries a validated, pre-persistence registration
// and its single-use token hash.
type VerificationRegistration struct {
	ID               string
	TokenHash        []byte
	Name             string
	Email            string
	EmailFingerprint []byte
	PasswordHash     string
	Role             domain.Role
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

// SealedEmail is an authenticated encrypted outbox message.
type SealedEmail struct {
	ID          string
	MessageType string
	KeyID       string
	Nonce       []byte
	Ciphertext  []byte
	CreatedAt   time.Time
}

// VerificationStore persists registration verification state and lifecycle
// transitions atomically.
type VerificationStore interface {
	CreateOrIgnore(context.Context, VerificationRegistration, SealedEmail) error
	RotateForResend(context.Context, string, []byte, time.Time, time.Time, SealedEmail) error
	Consume(context.Context, []byte, string, time.Time) (domain.User, error)
	CleanupExpired(context.Context, time.Time) (int64, error)
}

// EmailSealer protects verification-message contents before persistence.
type EmailSealer interface {
	SealVerification(messageID, email, token string) (SealedEmail, error)
}

// Fingerprinter derives a stable pseudonymous lookup key for an email address.
type Fingerprinter interface {
	Fingerprint(string) []byte
}

// IDGenerator creates opaque identifiers for Identity-owned records.
type IDGenerator interface {
	NewID() string
}

// BootstrapStore owns the serialized first-administrator transition.
type BootstrapStore interface {
	Required(context.Context) (bool, error)
	CreateAdmin(context.Context, domain.User) error
}

// PendingCursor identifies the last item in a stable pending-account page.
type PendingCursor struct {
	CreatedAt time.Time
	UserID    string
}

// PendingPage contains pending librarians and an optional continuation cursor.
type PendingPage struct {
	Users []domain.User
	Next  *PendingCursor
}

// ApprovalStore authorizes and persists librarian review decisions.
type ApprovalStore interface {
	ListPending(context.Context, domain.Principal, int, *PendingCursor, time.Time) (PendingPage, error)
	Decide(context.Context, domain.Principal, string, domain.Status, time.Time) error
	CleanupRejected(context.Context, time.Time) (int64, error)
}

// PrincipalStore resolves a session against current user authorization state.
type PrincipalStore interface {
	ValidatePrincipal(context.Context, string, string, time.Time) (domain.Principal, error)
}

// EmailDelivery contains one leased encrypted outbox message.
type EmailDelivery struct {
	ID         string
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
	Attempts   int
}

// EmailOutbox leases messages and records retry-safe delivery outcomes.
type EmailOutbox interface {
	Claim(context.Context, time.Time, time.Duration, int) ([]EmailDelivery, error)
	Delivered(context.Context, string, time.Time) error
	Failed(context.Context, string, time.Time, bool) error
}

// EmailSender delivers a verification token to its intended recipient.
type EmailSender interface {
	SendVerification(context.Context, string, string) error
}

// EmailOpener authenticates and decrypts an outbox delivery.
type EmailOpener interface {
	OpenVerification(EmailDelivery) (email, token string, err error)
}

// PendingNotifications emits invalidations when pending-librarian state changes.
type PendingNotifications interface {
	Watch(context.Context) (<-chan struct{}, error)
}
