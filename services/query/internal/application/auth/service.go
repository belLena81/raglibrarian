// Package auth is the application layer for the auth bounded context.
// It orchestrates domain objects and infrastructure ports.
// No HTTP, gRPC, or SQL concerns belong here.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
)

// ── Ports ─────────────────────────────────────────────────────────────────────

// PasswordHasher abstracts bcrypt.
type PasswordHasher interface {
	Hash(plaintext string) (string, error)
	Compare(hash, plaintext string) error
}

// TokenIssuer abstracts PASETO signing.
type TokenIssuer interface {
	Issue(claims TokenClaims) (string, error)
}

// TokenClaims carries the data encoded into a PASETO access token.
type TokenClaims struct {
	UserID uuid.UUID
	Email  string
	Role   user.Role
}

// ── Config ────────────────────────────────────────────────────────────────────

// Config holds tunable parameters for the auth service.
type Config struct {
	AccessTokenTTL  time.Duration // default: 15 minutes
	RefreshTokenTTL time.Duration // default: 7 days
}

func DefaultConfig() Config {
	return Config{
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 7 * 24 * time.Hour,
	}
}

// ── DTOs ──────────────────────────────────────────────────────────────────────

type RegisterInput struct {
	Name     string
	Email    string
	Password string
}

type LoginInput struct {
	Email    string
	Password string
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string // raw opaque bytes (hex-encoded), NOT the stored hash
}

type AuthOutput struct {
	UserID uuid.UUID
	Name   string
	Email  string
	Role   user.Role
	Tokens TokenPair
}

type MeOutput struct {
	UserID uuid.UUID
	Name   string
	Email  string
	Role   user.Role
}

// ── Errors ────────────────────────────────────────────────────────────────────

var (
	ErrPasswordTooShort    = errors.New("auth: password must be at least 8 characters")
	ErrPasswordTooLong     = errors.New("auth: password must be 72 characters or fewer")
	ErrRefreshTokenInvalid = errors.New("auth: refresh token is invalid or expired")
)

// ── Service ───────────────────────────────────────────────────────────────────

type Service struct {
	users         user.Repository
	tokens        user.RefreshTokenRepository
	hasher        PasswordHasher
	issuer        TokenIssuer
	cfg           Config
}

func NewService(
	users user.Repository,
	tokens user.RefreshTokenRepository,
	hasher PasswordHasher,
	issuer TokenIssuer,
	cfg Config,
) *Service {
	return &Service{users: users, tokens: tokens, hasher: hasher, issuer: issuer, cfg: cfg}
}

// Register creates a new Reader account and returns an access+refresh token pair.
func (s *Service) Register(ctx context.Context, in RegisterInput) (AuthOutput, error) {
	if err := validatePassword(in.Password); err != nil {
		return AuthOutput{}, err
	}

	exists, err := s.users.ExistsByEmail(ctx, in.Email)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Register: check email: %w", err)
	}
	if exists {
		return AuthOutput{}, user.ErrEmailTaken
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Register: hash password: %w", err)
	}

	u, err := user.NewUser(in.Name, in.Email, hash, user.RoleReader)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Register: build user: %w", err)
	}
	if err = s.users.Save(ctx, u); err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Register: save user: %w", err)
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Register: issue tokens: %w", err)
	}

	return AuthOutput{UserID: u.ID(), Name: u.Name(), Email: u.Email(), Role: u.Role(), Tokens: pair}, nil
}

// Login authenticates by email + password and returns a token pair.
// Both ErrUserNotFound and ErrInvalidPassword are returned wrapped —
// callers should map both to 401 to prevent user enumeration.
func (s *Service) Login(ctx context.Context, in LoginInput) (AuthOutput, error) {
	u, err := s.users.FindByEmail(ctx, in.Email)
	if err != nil {
		if errors.Is(err, user.ErrUserNotFound) {
			return AuthOutput{}, fmt.Errorf("auth.Login: %w", user.ErrUserNotFound)
		}
		return AuthOutput{}, fmt.Errorf("auth.Login: find user: %w", err)
	}

	if err = s.hasher.Compare(u.PasswordHash(), in.Password); err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Login: %w", user.ErrInvalidPassword)
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return AuthOutput{}, fmt.Errorf("auth.Login: issue tokens: %w", err)
	}

	return AuthOutput{UserID: u.ID(), Name: u.Name(), Email: u.Email(), Role: u.Role(), Tokens: pair}, nil
}

// Refresh exchanges a valid refresh token for a new access+refresh token pair.
// The old refresh token is revoked (rotation).
func (s *Service) Refresh(ctx context.Context, rawRefreshToken string) (TokenPair, error) {
	hash := hashToken(rawRefreshToken)
	rt, err := s.tokens.FindRefreshTokenByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, user.ErrTokenNotFound) {
			return TokenPair{}, ErrRefreshTokenInvalid
		}
		return TokenPair{}, fmt.Errorf("auth.Refresh: find token: %w", err)
	}

	if !rt.IsValid() {
		return TokenPair{}, ErrRefreshTokenInvalid
	}

	// Revoke the used token (rotation — each refresh token is single-use)
	if err = s.tokens.RevokeRefreshToken(ctx, rt.ID()); err != nil {
		return TokenPair{}, fmt.Errorf("auth.Refresh: revoke old token: %w", err)
	}

	u, err := s.users.FindByID(ctx, rt.UserID())
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth.Refresh: find user: %w", err)
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return TokenPair{}, fmt.Errorf("auth.Refresh: issue tokens: %w", err)
	}
	return pair, nil
}

// Logout revokes all refresh tokens for the user (full sign-out).
func (s *Service) Logout(ctx context.Context, userID uuid.UUID) error {
	if err := s.tokens.RevokeAllForUser(ctx, userID); err != nil {
		return fmt.Errorf("auth.Logout: %w", err)
	}
	return nil
}

// Me returns the profile for an authenticated user.
func (s *Service) Me(ctx context.Context, id uuid.UUID) (MeOutput, error) {
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return MeOutput{}, fmt.Errorf("auth.Me: %w", err)
	}
	return MeOutput{UserID: u.ID(), Name: u.Name(), Email: u.Email(), Role: u.Role()}, nil
}

// SeedAdmin creates an admin account if no user with adminEmail already exists.
// Called once on Query Service startup via env vars ADMIN_EMAIL + ADMIN_PASSWORD.
func (s *Service) SeedAdmin(ctx context.Context, adminEmail, adminPassword string) error {
	exists, err := s.users.ExistsByEmail(ctx, adminEmail)
	if err != nil {
		return fmt.Errorf("auth.SeedAdmin: check email: %w", err)
	}
	if exists {
		return nil // idempotent — admin already seeded
	}

	if err = validatePassword(adminPassword); err != nil {
		return fmt.Errorf("auth.SeedAdmin: %w", err)
	}

	hash, err := s.hasher.Hash(adminPassword)
	if err != nil {
		return fmt.Errorf("auth.SeedAdmin: hash: %w", err)
	}

	u, err := user.NewUser("Admin", adminEmail, hash, user.RoleAdmin)
	if err != nil {
		return fmt.Errorf("auth.SeedAdmin: build user: %w", err)
	}

	if err = s.users.Save(ctx, u); err != nil {
		return fmt.Errorf("auth.SeedAdmin: save: %w", err)
	}
	return nil
}

// ── private helpers ───────────────────────────────────────────────────────────

// issueTokenPair creates a signed access token and a persisted refresh token.
func (s *Service) issueTokenPair(ctx context.Context, u *user.User) (TokenPair, error) {
	accessToken, err := s.issuer.Issue(TokenClaims{UserID: u.ID(), Email: u.Email(), Role: u.Role()})
	if err != nil {
		return TokenPair{}, fmt.Errorf("issue access token: %w", err)
	}

	rawRefresh, err := generateOpaqueToken()
	if err != nil {
		return TokenPair{}, fmt.Errorf("generate refresh token: %w", err)
	}

	rt, err := user.NewRefreshToken(u.ID(), hashToken(rawRefresh), s.cfg.RefreshTokenTTL)
	if err != nil {
		return TokenPair{}, fmt.Errorf("build refresh token: %w", err)
	}
	if err = s.tokens.SaveRefreshToken(ctx, rt); err != nil {
		return TokenPair{}, fmt.Errorf("save refresh token: %w", err)
	}

	return TokenPair{AccessToken: accessToken, RefreshToken: rawRefresh}, nil
}

func validatePassword(p string) error {
	if len(p) < 8 {
		return ErrPasswordTooShort
	}
	if len(p) > 72 {
		return ErrPasswordTooLong
	}
	return nil
}

// generateOpaqueToken produces 32 cryptographically random bytes as hex.
func generateOpaqueToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken returns the hex-encoded SHA-256 of a raw token string.
// We never store plaintext refresh tokens in the database.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
