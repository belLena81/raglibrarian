package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// SessionService owns login and session lifecycle workflows.
type SessionService struct {
	users      port.UserReader
	sessions   port.SessionStore
	issuer     AccessTokenIssuer
	passwords  PasswordHasher
	clock      Clock
	sessionTTL time.Duration
}

// NewSessionService constructs session workflows with explicit dependencies.
func NewSessionService(
	users port.UserReader,
	sessions port.SessionStore,
	issuer AccessTokenIssuer,
	passwords PasswordHasher,
	clock Clock,
	sessionTTL time.Duration,
) *SessionService {
	if users == nil || sessions == nil || issuer == nil || passwords == nil || clock == nil || sessionTTL <= 0 {
		panic("usecase: invalid session dependencies")
	}
	return &SessionService{users: users, sessions: sessions, issuer: issuer, passwords: passwords, clock: clock, sessionTTL: sessionTTL}
}

// Login verifies normalized credentials without revealing account existence.
func (s *SessionService) Login(ctx context.Context, email, plaintext string) (AuthResult, error) {
	email = normalizeEmail(email)
	if _, err := domain.NewUser(email, "validation-placeholder", domain.RoleReader); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	if s.passwords.Validate(plaintext) != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		_ = s.passwords.Compare(ctx, dummyPasswordHash, plaintext)
		if errors.Is(err, domain.ErrUserNotFound) {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		return AuthResult{}, fmt.Errorf("login: find user: %w", err)
	}
	if err = s.passwords.Compare(ctx, user.PasswordHash(), plaintext); err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		return AuthResult{}, fmt.Errorf("login: compare password: %w", err)
	}
	return s.createSession(ctx, user)
}

// Refresh rotates a one-time refresh token. All fallible principal loading and
// token preparation complete before persistence commits the rotation.
func (s *SessionService) Refresh(ctx context.Context, refreshToken string) (AuthResult, error) {
	if refreshToken == "" {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	successor, successorHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, fmt.Errorf("refresh: generate token: %w", err)
	}
	var result AuthResult
	err = s.sessions.Rotate(ctx, hashRefreshToken(refreshToken), successorHash, s.clock.Now().UTC(), func(principal port.RefreshPrincipal) error {
		accessToken, issueErr := s.issuer.Issue(
			principal.UserID,
			principal.Email,
			principal.Role,
			principal.Session.ID,
		)
		if issueErr != nil {
			return issueErr
		}
		result = AuthResult{
			AccessToken:  accessToken,
			RefreshToken: successor,
			SessionID:    principal.Session.ID,
			Role:         principal.Role,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, port.ErrRefreshTokenInvalid) || errors.Is(err, port.ErrRefreshTokenReused) {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		return AuthResult{}, fmt.Errorf("refresh: rotate token: %w", err)
	}
	return result, nil
}

// ValidateSession checks authoritative server-side revocation state.
func (s *SessionService) ValidateSession(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	err := s.sessions.Validate(ctx, userID, sessionID, s.clock.Now().UTC())
	if errors.Is(err, port.ErrSessionInvalid) {
		return domain.ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	return nil
}

// Logout revokes a session immediately.
func (s *SessionService) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	if err := s.sessions.Logout(ctx, sessionID, s.clock.Now().UTC()); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func (s *SessionService) createSession(ctx context.Context, user domain.User) (AuthResult, error) {
	refreshToken, tokenHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: generate refresh token: %w", err)
	}
	now := s.clock.Now().UTC()
	session := newSession(user.ID(), now.Add(s.sessionTTL))
	accessToken, err := s.issuer.Issue(user.ID(), user.Email(), user.Role(), session.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: issue access token: %w", err)
	}
	if err = s.sessions.Create(ctx, session, now, tokenHash); err != nil {
		return AuthResult{}, fmt.Errorf("login: create session: %w", err)
	}
	return AuthResult{AccessToken: accessToken, RefreshToken: refreshToken, SessionID: session.ID, Role: user.Role()}, nil
}
