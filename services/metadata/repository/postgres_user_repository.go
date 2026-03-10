package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/pkg/domain"
)

// pgUniqueViolation is the Postgres SQLSTATE code for unique constraint violations.
const pgUniqueViolation = "23505"
const (
	insertUserQuery = `
	INSERT INTO users (id, email, password_hash, role, created_at)
	VALUES ($1, $2, $3, $4, $5)`

	findUserByEmailQuery = `
	SELECT id, email, password_hash, role, created_at
	FROM users WHERE email=$1`

	findUserByIDQuery = `
	SELECT id, email, password_hash, role, created_at
	FROM users WHERE id=$1`
)

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

// Save inserts a new user row. Maps unique-email violations to domain.ErrEmailTaken.
func (r *PostgresUserRepository) Save(ctx context.Context, user domain.User) error {
	_, err := r.pool.Exec(ctx,
		insertUserQuery,
		user.ID(),
		user.Email(),
		user.PasswordHash(),
		string(user.Role()),
		user.CreatedAt(),
	)
	if err != nil {
		if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == pgUniqueViolation {
			return domain.ErrEmailTaken
		}
		return fmt.Errorf("repository: save user: %w", err)
	}
	return nil
}

// FindByEmail looks up a user by email. Returns domain.ErrUserNotFound when absent.
func (r *PostgresUserRepository) FindByEmail(ctx context.Context, email string) (domain.User, error) {
	row := r.pool.QueryRow(ctx,
		findUserByEmailQuery,
		email,
	)
	return scanUser(row)
}

// FindByID looks up a user by UUID. Returns domain.ErrUserNotFound when absent.
func (r *PostgresUserRepository) FindByID(ctx context.Context, id string) (domain.User, error) {
	row := r.pool.QueryRow(ctx,
		findUserByIDQuery,
		id,
	)
	return scanUser(row)
}

func scanUser(row pgx.Row) (domain.User, error) {
	var (
		id        string
		email     string
		hash      string
		roleStr   string
		createdAt time.Time
	)

	if err := row.Scan(&id, &email, &hash, &roleStr, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("repository: scan user: %w", err)
	}

	return domain.NewUserFromDB(id, email, hash, domain.Role(roleStr), createdAt), nil
}
