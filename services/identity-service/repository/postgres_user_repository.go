package repository

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

// pgUniqueViolation is the Postgres SQLSTATE code for unique constraint violations.
const pgUniqueViolation = "23505"
const findUserByEmailQuery = `
	SELECT id, display_name, email, password_hash, email_fingerprint, role, status, email_verified_at, created_at, reviewed_by, reviewed_at
	FROM identity.users WHERE email=$1`
const findUsersByEmailQuery = `
	SELECT id, display_name, email, password_hash, email_fingerprint, role, status, email_verified_at, created_at, reviewed_by, reviewed_at
	FROM identity.users WHERE email=$1 AND role IN ('admin', 'librarian', 'reader')`

// PostgresUserRepository is the pgx/v5 implementation of UserRepository.
type PostgresUserRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresUserRepository constructs the repository with the given pool.
// Panics if pool is nil.
func NewPostgresUserRepository(pool *pgxpool.Pool) *PostgresUserRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresUserRepository{pool: pool}
}

// FindByEmail looks up a user by email. Returns domain.ErrUserNotFound when absent.
func (r *PostgresUserRepository) FindByEmail(ctx context.Context, email string) (domain.User, error) {
	row := r.pool.QueryRow(ctx,
		findUserByEmailQuery,
		email,
	)
	return scanUser(row)
}

// FindByEmailRoles returns every role-scoped account for an address.
func (r *PostgresUserRepository) FindByEmailRoles(ctx context.Context, email string) ([]domain.User, error) {
	rows, err := r.pool.Query(ctx, findUsersByEmailQuery, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]domain.User, 0, 3)
	for rows.Next() {
		user, scanErr := scanIdentityUser(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		users = append(users, user)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return users, nil
}

func scanUser(row pgx.Row) (domain.User, error) {
	user, err := scanIdentityUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, err
	}
	return user, nil
}
