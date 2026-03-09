// Package repository defines the port (interface) that the auth use case
// depends on for user persistence. Concrete adapters implement this interface;
// the domain and application layers never import them.
package repository

import (
	"context"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// UserRepository is the persistence port for the User aggregate.
// Implementations live in the infra layer and are injected at startup.
type UserRepository interface {
	// Save persists a new User. Returns domain.ErrEmailTaken if the email
	// is already registered.
	Save(ctx context.Context, user domain.User) error

	// FindByEmail returns the User with the given email.
	// Returns domain.ErrUserNotFound if no such user exists.
	FindByEmail(ctx context.Context, email string) (domain.User, error)

	// FindByID returns the User with the given ID.
	// Returns domain.ErrUserNotFound if no such user exists.
	FindByID(ctx context.Context, id string) (domain.User, error)
}
