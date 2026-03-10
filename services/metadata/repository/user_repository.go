// Package repository defines the persistence port for the auth use case.
package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// UserRepository is the persistence port for the User aggregate.
type UserRepository interface {
	// Save persists a new User. Returns domain.ErrEmailTaken on duplicate email.
	Save(ctx context.Context, user domain.User) error

	// FindByEmail returns the User with the given email.
	// Returns domain.ErrUserNotFound if absent.
	FindByEmail(ctx context.Context, email string) (domain.User, error)

	// FindByID returns the User with the given ID.
	// Returns domain.ErrUserNotFound if absent.
	FindByID(ctx context.Context, id string) (domain.User, error)
}
