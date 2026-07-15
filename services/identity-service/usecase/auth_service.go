// Package usecase contains Identity application workflows.
package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
)

const refreshTokenBytes = 32

// This public fixed hash equalizes password work for unknown accounts.
const dummyPasswordHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW" // #nosec G101 -- timing-only fixture

// AuthResult is the internal result of creating or rotating a session.
type AuthResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         domain.Role
}

// AuthUseCase is the transport-facing Identity application contract.
type AuthUseCase interface {
	Register(context.Context, string, string, domain.Role) (AuthResult, error)
	Login(context.Context, string, string) (AuthResult, error)
	Refresh(context.Context, string) (AuthResult, error)
	ValidateSession(context.Context, string, string) error
	Logout(context.Context, string) error
}

// PasswordHasher owns password validation and bounded expensive work.
type PasswordHasher interface {
	Validate(string) error
	Hash(context.Context, string) (string, error)
	Compare(context.Context, string, string) error
}

// AccessTokenIssuer issues access tokens from primitive Identity values.
type AccessTokenIssuer interface {
	Issue(userID, email string, role domain.Role, sessionID string) (string, error)
}

// Clock supplies deterministic application time.
type Clock interface {
	Now() time.Time
}

// AuthService coordinates Identity repositories and security adapters.
type AuthService struct {
	users      repository.UserRepository
	sessions   repository.SessionRepository
	issuer     AccessTokenIssuer
	passwords  PasswordHasher
	clock      Clock
	sessionTTL time.Duration
}

// NewAuthService constructs authentication with explicit required dependencies.
func NewAuthService(
	users repository.UserRepository,
	sessions repository.SessionRepository,
	issuer AccessTokenIssuer,
	passwords PasswordHasher,
	clock Clock,
	sessionTTL time.Duration,
) *AuthService {
	if users == nil || sessions == nil || issuer == nil || passwords == nil || clock == nil || sessionTTL <= 0 {
		panic("usecase: invalid authentication dependencies")
	}
	return &AuthService{users: users, sessions: sessions, issuer: issuer, passwords: passwords, clock: clock, sessionTTL: sessionTTL}
}

// Register validates credentials, persists a user, and creates a session.
func (s *AuthService) Register(ctx context.Context, email, plaintext string, role domain.Role) (AuthResult, error) {
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
	if err = s.users.Save(ctx, user); err != nil {
		return AuthResult{}, fmt.Errorf("register: save user: %w", err)
	}
	result, err := s.createSession(ctx, user)
	if err != nil {
		return AuthResult{}, fmt.Errorf("register: create session: %w", err)
	}
	return result, nil
}

// Login verifies normalized credentials without revealing account existence.
func (s *AuthService) Login(ctx context.Context, email, plaintext string) (AuthResult, error) {
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
	result, err := s.createSession(ctx, user)
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: create session: %w", err)
	}
	return result, nil
}

// Refresh rotates a one-time refresh token.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (AuthResult, error) {
	if refreshToken == "" {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	plaintext, successorHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, fmt.Errorf("refresh: generate token: %w", err)
	}
	session, err := s.sessions.Rotate(ctx, hashRefreshToken(refreshToken), successorHash, s.clock.Now().UTC())
	if err != nil {
		if errors.Is(err, repository.ErrRefreshTokenInvalid) || errors.Is(err, repository.ErrRefreshTokenReused) {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		return AuthResult{}, fmt.Errorf("refresh: rotate token: %w", err)
	}
	user, err := s.users.FindByID(ctx, session.UserID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("refresh: find user: %w", err)
	}
	return s.issueResult(user, session.ID, plaintext)
}

// ValidateSession checks authoritative server-side revocation state.
func (s *AuthService) ValidateSession(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	err := s.sessions.Validate(ctx, userID, sessionID, s.clock.Now().UTC())
	if errors.Is(err, repository.ErrSessionInvalid) {
		return domain.ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	return nil
}

// Logout revokes a session immediately.
func (s *AuthService) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	if err := s.sessions.Logout(ctx, sessionID, s.clock.Now().UTC()); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func (s *AuthService) createSession(ctx context.Context, user domain.User) (AuthResult, error) {
	plaintext, tokenHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, err
	}
	session, err := s.sessions.Create(ctx, user.ID(), s.clock.Now().UTC().Add(s.sessionTTL), tokenHash)
	if err != nil {
		return AuthResult{}, err
	}
	return s.issueResult(user, session.ID, plaintext)
}

func (s *AuthService) issueResult(user domain.User, sessionID, refreshToken string) (AuthResult, error) {
	accessToken, err := s.issuer.Issue(user.ID(), user.Email(), user.Role(), sessionID)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{AccessToken: accessToken, RefreshToken: refreshToken, SessionID: sessionID, Role: user.Role()}, nil
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func newRefreshToken() (string, []byte, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	plaintext := base64.RawURLEncoding.EncodeToString(raw)
	return plaintext, hashRefreshToken(plaintext), nil
}

func hashRefreshToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
