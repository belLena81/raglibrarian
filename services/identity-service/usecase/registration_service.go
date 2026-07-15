package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// RegistrationService owns account registration and its initial session.
type RegistrationService struct {
	store      port.RegistrationStore
	issuer     AccessTokenIssuer
	passwords  PasswordHasher
	clock      Clock
	sessionTTL time.Duration
}

// NewRegistrationService constructs registration with explicit dependencies.
func NewRegistrationService(
	store port.RegistrationStore,
	issuer AccessTokenIssuer,
	passwords PasswordHasher,
	clock Clock,
	sessionTTL time.Duration,
) *RegistrationService {
	if store == nil || issuer == nil || passwords == nil || clock == nil || sessionTTL <= 0 {
		panic("usecase: invalid registration dependencies")
	}
	return &RegistrationService{store: store, issuer: issuer, passwords: passwords, clock: clock, sessionTTL: sessionTTL}
}

// Register validates credentials and atomically persists the account and its
// initial session family.
func (s *RegistrationService) Register(ctx context.Context, email, plaintext string, role domain.Role) (AuthResult, error) {
	email = normalizeEmail(email)
	if _, err := domain.NewUser(email, "validation-placeholder", role); err != nil {
		return AuthResult{}, fmt.Errorf("register: invalid user: %w", err)
	}
	if err := s.passwords.Validate(plaintext); err != nil {
		return AuthResult{}, fmt.Errorf("register: invalid password: %w", err)
	}
	hash, err := s.passwords.Hash(ctx, plaintext)
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: hash password: %w", err)
	}
	user, err := domain.NewUser(email, hash, role)
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: invalid user: %w", err)
	}
	refreshToken, tokenHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: generate refresh token: %w", err)
	}
	now := s.clock.Now().UTC()
	session := newSession(user.ID(), now.Add(s.sessionTTL))
	accessToken, err := s.issuer.Issue(user.ID(), user.Email(), user.Role(), session.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: issue access token: %w", err)
	}
	err = s.store.CreateRegistration(ctx, port.Registration{
		User:             user,
		Session:          session,
		CreatedAt:        now,
		RefreshTokenHash: tokenHash,
	})
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: persist account and session: %w", err)
	}
	return AuthResult{AccessToken: accessToken, RefreshToken: refreshToken, SessionID: session.ID, Role: user.Role()}, nil
}
