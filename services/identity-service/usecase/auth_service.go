// Package usecase contains the application layer for authentication.
package usecase

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
)

const refreshTokenBytes = 32

// AuthResult is returned only to an authenticated internal caller. The refresh
// token is opaque and plaintext exists only for the duration of this call.
type AuthResult struct {
	AccessToken  string
	RefreshToken string
	SessionID    string
	Role         domain.Role
}

// AuthUseCase is the Identity application contract.
type AuthUseCase interface {
	Register(ctx context.Context, email, password string, role domain.Role) (AuthResult, domain.User, error)
	Login(ctx context.Context, email, password string) (AuthResult, error)
	Refresh(ctx context.Context, refreshToken string) (AuthResult, error)
	ValidateSession(ctx context.Context, userID, sessionID string) error
	Logout(ctx context.Context, sessionID string) error
}

type tokenIssuer interface {
	Issue(domain.User, ...string) (string, error)
}

// AuthService is the production implementation of AuthUseCase.
type AuthService struct {
	users      repository.UserRepository
	sessions   repository.SessionRepository
	issuer     tokenIssuer
	sessionTTL time.Duration
	now        func() time.Time
}

func NewAuthService(users repository.UserRepository, sessions repository.SessionRepository, issuer tokenIssuer, sessionTTL time.Duration) *AuthService {
	if users == nil || sessions == nil || issuer == nil || sessionTTL <= 0 {
		panic("usecase: invalid authentication dependencies")
	}
	return &AuthService{users: users, sessions: sessions, issuer: issuer, sessionTTL: sessionTTL, now: time.Now}
}

func (s *AuthService) Register(ctx context.Context, email, password string, role domain.Role) (AuthResult, domain.User, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: hash password: %w", err)
	}
	user, err := domain.NewUser(email, hash, role)
	if err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: invalid user: %w", err)
	}
	if err = s.users.Save(ctx, user); err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: save user: %w", err)
	}
	result, err := s.createSession(ctx, user)
	if err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: create session: %w", err)
	}
	return result, user, nil
}

func (s *AuthService) Login(ctx context.Context, email, password string) (AuthResult, error) {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	if err = auth.CheckPassword(user.PasswordHash(), password); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	result, err := s.createSession(ctx, user)
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: create session: %w", err)
	}
	return result, nil
}

func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (AuthResult, error) {
	if refreshToken == "" {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	plaintext, successorHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, fmt.Errorf("refresh: generate token: %w", err)
	}
	currentHash := hashRefreshToken(refreshToken)
	session, err := s.sessions.Rotate(ctx, currentHash, successorHash, s.now().UTC())
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
	accessToken, err := s.issuer.Issue(user, session.ID)
	if err != nil {
		return AuthResult{}, fmt.Errorf("refresh: issue access token: %w", err)
	}
	return AuthResult{AccessToken: accessToken, RefreshToken: plaintext, SessionID: session.ID, Role: user.Role()}, nil
}

func (s *AuthService) ValidateSession(ctx context.Context, userID, sessionID string) error {
	if userID == "" || sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	err := s.sessions.Validate(ctx, userID, sessionID, s.now().UTC())
	if errors.Is(err, repository.ErrSessionInvalid) {
		return domain.ErrInvalidCredentials
	}
	if err != nil {
		return fmt.Errorf("validate session: %w", err)
	}
	return nil
}

func (s *AuthService) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	if err := s.sessions.Logout(ctx, sessionID, s.now().UTC()); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func (s *AuthService) createSession(ctx context.Context, user domain.User) (AuthResult, error) {
	refreshToken, tokenHash, err := newRefreshToken()
	if err != nil {
		return AuthResult{}, err
	}
	session, err := s.sessions.Create(ctx, user.ID(), s.now().UTC().Add(s.sessionTTL), tokenHash)
	if err != nil {
		return AuthResult{}, err
	}
	accessToken, err := s.issuer.Issue(user, session.ID)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{AccessToken: accessToken, RefreshToken: refreshToken, SessionID: session.ID, Role: user.Role()}, nil
}

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
