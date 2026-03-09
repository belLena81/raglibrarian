// Package usecase contains the application layer for authentication.
// It orchestrates the domain, repository port, and auth package — never
// touching HTTP, gRPC, or any infrastructure concern directly.
package usecase

import (
	"context"
	"fmt"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/domain"
	"github.com/belLena81/raglibrarian/services/metadata/repository"
)

// AuthUseCase is the application-layer contract for user registration and login.
// Defining it as an interface keeps the HTTP handler testable with a plain fake.
type AuthUseCase interface {
	Register(ctx context.Context, email, password string, role domain.Role) (domain.User, error)
	Login(ctx context.Context, email, password string) (string, error)
}

// AuthService is the production implementation of AuthUseCase.
type AuthService struct {
	users  repository.UserRepository
	issuer *auth.Issuer
}

// NewAuthService constructs an AuthService.
// Panics on nil dependencies — misconfigured wiring must fail at startup.
func NewAuthService(users repository.UserRepository, issuer *auth.Issuer) *AuthService {
	if users == nil {
		panic("usecase: UserRepository must not be nil")
	}
	if issuer == nil {
		panic("usecase: Issuer must not be nil")
	}
	return &AuthService{users: users, issuer: issuer}
}

// Register creates a new user account.
//
// Password hashing is done here — in the application layer — rather than in
// the domain, because bcrypt is a concrete infrastructure dependency. The
// domain only stores the hash; it never knows how it was produced.
//
// The default role for self-registration is Reader. Admin accounts must be
// created by an existing Admin (a future iteration concern).
func (s *AuthService) Register(ctx context.Context, email, password string, role domain.Role) (domain.User, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return domain.User{}, fmt.Errorf("register: hash password: %w", err)
	}

	user, err := domain.NewUser(email, hash, role)
	if err != nil {
		// Domain validation error — surfaces as 422 at the handler layer.
		return domain.User{}, fmt.Errorf("register: invalid user: %w", err)
	}

	if err := s.users.Save(ctx, user); err != nil {
		return domain.User{}, fmt.Errorf("register: save user: %w", err)
	}

	return user, nil
}

// Login authenticates a user by email and password, returning a PASETO token
// on success.
//
// The use case deliberately does not distinguish "user not found" from
// "wrong password" in its returned error. Both surface as
// auth.ErrInvalidCredentials. This prevents user enumeration: an attacker
// cannot determine whether a given email is registered by comparing error
// messages.
func (s *AuthService) Login(ctx context.Context, email, password string) (string, error) {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		// Map ErrUserNotFound to ErrInvalidCredentials — same error for both
		// "no such user" and "wrong password".
		return "", auth.ErrInvalidCredentials
	}

	if err := auth.CheckPassword(user.PasswordHash(), password); err != nil {
		return "", auth.ErrInvalidCredentials
	}

	token, err := s.issuer.Issue(user)
	if err != nil {
		return "", fmt.Errorf("login: issue token: %w", err)
	}

	return token, nil
}
