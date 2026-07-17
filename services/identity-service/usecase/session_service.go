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
	passwords  PasswordVerifier
	clock      Clock
	sessionTTL time.Duration
}

// NewSessionService constructs session workflows with explicit dependencies.
func NewSessionService(
	users port.UserReader,
	sessions port.SessionStore,
	issuer AccessTokenIssuer,
	passwords PasswordVerifier,
	clock Clock,
	sessionTTL time.Duration,
) *SessionService {
	if users == nil || sessions == nil || issuer == nil || passwords == nil || clock == nil || sessionTTL <= 0 {
		panic("usecase: invalid session dependencies")
	}
	return &SessionService{users: users, sessions: sessions, issuer: issuer, passwords: passwords, clock: clock, sessionTTL: sessionTTL}
}

// Login verifies all three possible role hashes before deciding whether role
// selection is necessary. This keeps the bcrypt work fixed for every request.
func (s *SessionService) Login(ctx context.Context, email, plaintext string, selectedRoles ...string) (AuthResult, error) {
	selectedRole := ""
	if len(selectedRoles) > 0 {
		selectedRole = selectedRoles[0]
	}
	email = normalizeEmail(email)
	if !validEmail(email) {
		for range []domain.Role{domain.RoleAdmin, domain.RoleLibrarian, domain.RoleReader} {
			_ = s.passwords.Compare(ctx, dummyPasswordHash, plaintext)
		}
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	roleReader, ok := s.users.(port.RoleAccountReader)
	if !ok {
		return AuthResult{}, fmt.Errorf("login: role account reader unsupported")
	}
	users, err := roleReader.FindByEmailRoles(ctx, email)
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: find user: %w", err)
	}
	byRole := make(map[domain.Role]domain.User, len(users))
	for _, user := range users {
		byRole[user.Role()] = user
	}
	matches := make([]domain.User, 0, 3)
	for _, role := range []domain.Role{domain.RoleAdmin, domain.RoleLibrarian, domain.RoleReader} {
		user, present := byRole[role]
		hash := dummyPasswordHash
		if present {
			hash = user.PasswordHash()
		}
		compareErr := s.passwords.Compare(ctx, hash, plaintext)
		if compareErr != nil && !errors.Is(compareErr, domain.ErrInvalidCredentials) {
			return AuthResult{}, fmt.Errorf("login: compare password: %w", compareErr)
		}
		if present && compareErr == nil && user.CanAuthenticate() {
			matches = append(matches, user)
		}
	}
	if selectedRole != "" {
		role := domain.Role(selectedRole)
		if !role.IsValid() {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		for _, user := range matches {
			if user.Role() == role {
				return s.createSession(ctx, user)
			}
		}
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	if len(matches) == 1 {
		return s.createSession(ctx, matches[0])
	}
	if len(matches) > 1 {
		roles := make([]domain.Role, 0, len(matches))
		for _, user := range matches {
			roles = append(roles, user.Role())
		}
		return AuthResult{AvailableRoles: roles}, nil
	}
	return AuthResult{}, domain.ErrInvalidCredentials
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

// ValidatePrincipal returns current Identity-owned authorization facts. It is
// deliberately separate from access-token claims.
func (s *SessionService) ValidatePrincipal(ctx context.Context, userID, sessionID string) (domain.Principal, error) {
	if userID == "" || sessionID == "" {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	store, ok := s.sessions.(port.PrincipalStore)
	if !ok {
		return domain.Principal{}, fmt.Errorf("validate principal: store unsupported")
	}
	principal, err := store.ValidatePrincipal(ctx, userID, sessionID, s.clock.Now().UTC())
	if errors.Is(err, port.ErrSessionInvalid) {
		return domain.Principal{}, domain.ErrInvalidCredentials
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("validate principal: %w", err)
	}
	return principal, nil
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
