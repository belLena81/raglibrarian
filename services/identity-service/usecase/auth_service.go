// Package usecase contains the application layer for authentication.
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

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
)

const refreshTokenBytes = 32

// This valid, fixed bcrypt hash is compared for unknown users so login performs
// one password comparison regardless of account existence.
const dummyPasswordHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW" // #nosec G101 -- deliberately public non-credential timing hash

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
	users       repository.UserRepository
	sessions    repository.SessionRepository
	issuer      tokenIssuer
	sessionTTL  time.Duration
	now         func() time.Time
	bcryptSlots chan struct{}
	hash        func(string) (string, error)
	check       func(string, string) error
}

// NewAuthService constructs Identity authentication with bounded bcrypt work.
func NewAuthService(users repository.UserRepository, sessions repository.SessionRepository, issuer tokenIssuer, sessionTTL time.Duration, bcryptConcurrency ...int) *AuthService {
	if users == nil || sessions == nil || issuer == nil || sessionTTL <= 0 {
		panic("usecase: invalid authentication dependencies")
	}
	limit := 4
	if len(bcryptConcurrency) > 0 && bcryptConcurrency[0] > 0 {
		limit = bcryptConcurrency[0]
	}
	return &AuthService{
		users: users, sessions: sessions, issuer: issuer, sessionTTL: sessionTTL,
		now: time.Now, bcryptSlots: make(chan struct{}, limit),
		hash: auth.HashPassword, check: auth.CheckPassword,
	}
}

// Register validates and normalizes credentials before running bcrypt.
func (s *AuthService) Register(ctx context.Context, email, password string, role domain.Role) (AuthResult, domain.User, error) {
	email = normalizeEmail(email)
	if _, err := domain.NewUser(email, "validation-placeholder", role); err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: invalid user: %w", err)
	}
	if err := auth.ValidatePassword(password); err != nil {
		return AuthResult{}, domain.User{}, fmt.Errorf("register: invalid password: %w", err)
	}
	hash, err := s.hashPassword(ctx, password)
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

// Login verifies normalized credentials without revealing account existence.
func (s *AuthService) Login(ctx context.Context, email, password string) (AuthResult, error) {
	email = normalizeEmail(email)
	if _, err := domain.NewUser(email, "validation-placeholder", domain.RoleReader); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	if err := auth.ValidatePassword(password); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		_ = s.checkPassword(ctx, dummyPasswordHash, password)
		if errors.Is(err, domain.ErrUserNotFound) {
			return AuthResult{}, domain.ErrInvalidCredentials
		}
		return AuthResult{}, fmt.Errorf("login: find user: %w", err)
	}
	if err = s.checkPassword(ctx, user.PasswordHash(), password); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials
	}
	result, err := s.createSession(ctx, user)
	if err != nil {
		return AuthResult{}, fmt.Errorf("login: create session: %w", err)
	}
	return result, nil
}

// Refresh rotates a one-time refresh token and returns a new session token set.
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

// ValidateSession confirms a token's server-side session remains active.
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

// Logout revokes the session immediately.
func (s *AuthService) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return domain.ErrInvalidCredentials
	}
	if err := s.sessions.Logout(ctx, sessionID, s.now().UTC()); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func (s *AuthService) hashPassword(ctx context.Context, password string) (string, error) {
	if err := s.acquireBcrypt(ctx); err != nil {
		return "", err
	}
	defer s.releaseBcrypt()
	return s.hash(password)
}

func (s *AuthService) checkPassword(ctx context.Context, hash, password string) error {
	if err := s.acquireBcrypt(ctx); err != nil {
		return err
	}
	defer s.releaseBcrypt()
	return s.check(hash, password)
}

func (s *AuthService) acquireBcrypt(ctx context.Context) error {
	select {
	case s.bcryptSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *AuthService) releaseBcrypt() {
	<-s.bcryptSlots
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
