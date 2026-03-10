// Package usecase contains the application layer for authentication.
package usecase

import (
	"context"
	"fmt"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
)

// AuthUseCase is the application-layer contract for registration and login.
type AuthUseCase interface {
	// Register creates a user and returns a ready-to-use token — no separate login needed.
	Register(ctx context.Context, email, password string, role domain.Role) (string, domain.User, error)
	Login(ctx context.Context, email, password string) (string, error)
}

// AuthService is the production implementation of AuthUseCase.
type AuthService struct {
	users  repository.UserRepository
	issuer *auth.Issuer
}

// NewAuthService constructs an AuthService. Panics on nil dependencies.
func NewAuthService(users repository.UserRepository, issuer *auth.Issuer) *AuthService {
	if users == nil {
		panic("usecase: UserRepository must not be nil")
	}
	if issuer == nil {
		panic("usecase: Issuer must not be nil")
	}
	return &AuthService{users: users, issuer: issuer}
}

// Register hashes the password once, persists the user, and issues a token.
func (s *AuthService) Register(ctx context.Context, email, password string, role domain.Role) (string, domain.User, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return "", domain.User{}, fmt.Errorf("register: hash password: %w", err)
	}

	user, err := domain.NewUser(email, hash, role)
	if err != nil {
		return "", domain.User{}, fmt.Errorf("register: invalid user: %w", err)
	}

	if err = s.users.Save(ctx, user); err != nil {
		return "", domain.User{}, fmt.Errorf("register: save user: %w", err)
	}

	token, err := s.issuer.Issue(user)
	if err != nil {
		return "", domain.User{}, fmt.Errorf("register: issue token: %w", err)
	}

	return token, user, nil
}

// Login authenticates by email and password, returning a PASETO token on success.
// Both "user not found" and "wrong password" surface as auth.ErrInvalidCredentials
// to prevent user enumeration.
func (s *AuthService) Login(ctx context.Context, email, password string) (string, error) {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		return "", domain.ErrInvalidCredentials
	}

	if err = auth.CheckPassword(user.PasswordHash(), password); err != nil {
		return "", domain.ErrInvalidCredentials
	}

	token, err := s.issuer.Issue(user)
	if err != nil {
		return "", fmt.Errorf("login: issue token: %w", err)
	}

	return token, nil
}
