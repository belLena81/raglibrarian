package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// PostgresRegistrationRepository persists a user and initial session in one
// transaction so registration cannot leave a partial account.
type PostgresRegistrationRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRegistrationRepository constructs the atomic registration store.
func NewPostgresRegistrationRepository(pool *pgxpool.Pool) *PostgresRegistrationRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresRegistrationRepository{pool: pool}
}

// CreateRegistration atomically inserts the user, session, and refresh token.
func (r *PostgresRegistrationRepository) CreateRegistration(
	ctx context.Context,
	registration port.Registration,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("registration: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err = insertUser(ctx, tx, registration.User); err != nil {
		return err
	}
	err = insertSessionAndToken(
		ctx,
		tx,
		registration.Session,
		registration.CreatedAt,
		registration.RefreshTokenHash,
	)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("registration: commit: %w", err)
	}
	return nil
}
