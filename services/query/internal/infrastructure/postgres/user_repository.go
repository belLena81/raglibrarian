// Package postgres implements user.Repository and user.RefreshTokenRepository
// using pgx/v5 directly. No ORM.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yourname/raglibrarian/services/query/internal/domain/user"
)

// ── UserRepository ────────────────────────────────────────────────────────────

type UserRepository struct {
	pool *pgxpool.Pool
}

func NewUserRepository(pool *pgxpool.Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

func (r *UserRepository) Save(ctx context.Context, u *user.User) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (id, name, email, password_hash, role, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE
		SET name          = EXCLUDED.name,
		    email         = EXCLUDED.email,
		    password_hash = EXCLUDED.password_hash,
		    role          = EXCLUDED.role,
		    updated_at    = EXCLUDED.updated_at
	`, u.ID(), u.Name(), u.Email(), u.PasswordHash(), string(u.Role()), u.CreatedAt(), u.UpdatedAt())
	if err != nil {
		if isUniqueViolation(err, "users_email_key") {
			return user.ErrEmailTaken
		}
		return fmt.Errorf("postgres.UserRepository.Save: %w", err)
	}
	return nil
}

func (r *UserRepository) FindByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, email, password_hash, role, created_at, updated_at
		FROM users WHERE id = $1
	`, id)
	return scanUser(row)
}

func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*user.User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, email, password_hash, role, created_at, updated_at
		FROM users WHERE LOWER(email) = LOWER($1)
	`, strings.ToLower(strings.TrimSpace(email)))
	return scanUser(row)
}

func (r *UserRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE LOWER(email) = LOWER($1))`,
		strings.ToLower(strings.TrimSpace(email)),
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("postgres.UserRepository.ExistsByEmail: %w", err)
	}
	return exists, nil
}

func scanUser(row pgx.Row) (*user.User, error) {
	var (
		id           uuid.UUID
		name, email, passwordHash, role string
		createdAt, updatedAt            time.Time
	)
	if err := row.Scan(&id, &name, &email, &passwordHash, &role, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, user.ErrUserNotFound
		}
		return nil, fmt.Errorf("postgres.scanUser: %w", err)
	}
	return user.Reconstitute(id, name, email, passwordHash, user.Role(role), createdAt, updatedAt), nil
}

// ── RefreshTokenRepository ────────────────────────────────────────────────────

type RefreshTokenRepository struct {
	pool *pgxpool.Pool
}

func NewRefreshTokenRepository(pool *pgxpool.Pool) *RefreshTokenRepository {
	return &RefreshTokenRepository{pool: pool}
}

func (r *RefreshTokenRepository) SaveRefreshToken(ctx context.Context, t *user.RefreshToken) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, t.ID(), t.UserID(), t.TokenHash(), t.ExpiresAt(), t.CreatedAt())
	if err != nil {
		return fmt.Errorf("postgres.RefreshTokenRepository.Save: %w", err)
	}
	return nil
}

func (r *RefreshTokenRepository) FindRefreshTokenByHash(ctx context.Context, hash string) (*user.RefreshToken, error) {
	var (
		id, userID                    uuid.UUID
		tokenHash                     string
		expiresAt, createdAt          time.Time
		revokedAt                     *time.Time
	)
	err := r.pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, expires_at, revoked_at, created_at
		FROM refresh_tokens WHERE token_hash = $1
	`, hash).Scan(&id, &userID, &tokenHash, &expiresAt, &revokedAt, &createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, user.ErrTokenNotFound
		}
		return nil, fmt.Errorf("postgres.RefreshTokenRepository.FindByHash: %w", err)
	}
	return user.ReconstituteFreshToken(id, userID, tokenHash, expiresAt, revokedAt, createdAt), nil
}

func (r *RefreshTokenRepository) RevokeRefreshToken(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("postgres.RefreshTokenRepository.Revoke: %w", err)
	}
	return nil
}

func (r *RefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return fmt.Errorf("postgres.RefreshTokenRepository.RevokeAllForUser: %w", err)
	}
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func isUniqueViolation(err error, constraint string) bool {
	return err != nil &&
		strings.Contains(err.Error(), "23505") &&
		strings.Contains(err.Error(), constraint)
}
