package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// PostgresSessionRepository implements durable sessions. Rotation locks the
// current token and its session in one transaction, preventing double use.
type PostgresSessionRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresSessionRepository constructs the durable session repository.
func NewPostgresSessionRepository(pool *pgxpool.Pool) *PostgresSessionRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresSessionRepository{pool: pool}
}

// Create persists a prepared session and its initial refresh-token hash.
func (r *PostgresSessionRepository) Create(ctx context.Context, session port.Session, createdAt time.Time, tokenHash []byte) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("session: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err = insertSessionAndToken(ctx, tx, session, createdAt, tokenHash); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("session: commit create: %w", err)
	}
	return nil
}

func insertSessionAndToken(
	ctx context.Context,
	tx pgx.Tx,
	session port.Session,
	createdAt time.Time,
	tokenHash []byte,
) error {
	createdAt = createdAt.UTC()
	_, err := tx.Exec(ctx, `INSERT INTO identity.sessions (id, user_id, family_id, expires_at, created_at, last_used_at) VALUES ($1,$2,$3,$4,$5,$5)`, session.ID, session.UserID, session.FamilyID, session.ExpiresAt, createdAt)
	if err != nil {
		return fmt.Errorf("session: insert: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.refresh_tokens (id, session_id, token_hash, expires_at, created_at) VALUES ($1,$2,$3,$4,$5)`, uuid.NewString(), session.ID, tokenHash, session.ExpiresAt, createdAt)
	if err != nil {
		return fmt.Errorf("session: insert refresh token: %w", err)
	}
	return nil
}

// Rotate prepares and atomically commits a one-time refresh-token successor.
func (r *PostgresSessionRepository) Rotate(
	ctx context.Context,
	tokenHash []byte,
	successorHash []byte,
	now time.Time,
	prepare port.PrepareRefresh,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("session: begin rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var session port.Session
	var tokenID string
	var consumedAt pgtype.Timestamptz
	var revokedAt pgtype.Timestamptz
	var (
		userID     string
		name       string
		email      string
		role       string
		userStatus string
	)
	err = tx.QueryRow(ctx, `SELECT s.id,s.user_id,s.family_id,s.expires_at,s.revoked_at,t.id,t.consumed_at,u.id,u.display_name,u.email,u.role,u.status FROM identity.refresh_tokens t JOIN identity.sessions s ON s.id=t.session_id JOIN identity.users u ON u.id=s.user_id WHERE t.token_hash=$1 AND u.status='active' FOR UPDATE OF t,s,u`, tokenHash).Scan(
		&session.ID,
		&session.UserID,
		&session.FamilyID,
		&session.ExpiresAt,
		&revokedAt,
		&tokenID,
		&consumedAt,
		&userID,
		&name,
		&email,
		&role,
		&userStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.ErrRefreshTokenInvalid
	}
	if err != nil {
		return fmt.Errorf("session: lock refresh principal: %w", err)
	}
	if consumedAt.Valid {
		_, err = tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE family_id=$2 AND revoked_at IS NULL`, now, session.FamilyID)
		if err != nil {
			return fmt.Errorf("session: revoke reused family: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("session: commit reuse revocation: %w", err)
		}
		return port.ErrRefreshTokenReused
	}
	if !session.ExpiresAt.After(now) {
		return port.ErrRefreshTokenInvalid
	}

	if revokedAt.Valid {
		return port.ErrRefreshTokenInvalid
	}
	if domain.Status(userStatus) != domain.StatusActive {
		return port.ErrRefreshTokenInvalid
	}

	principal := port.RefreshPrincipal{
		Session:    session,
		UserID:     userID,
		Name:       name,
		Email:      email,
		Role:       domain.Role(role),
		Status:     domain.Status(userStatus),
		VerifiedAt: time.Time{},
	}
	if err = prepare(principal); err != nil {
		return fmt.Errorf("session: prepare rotation: %w", err)
	}

	successorID := uuid.NewString()
	_, err = tx.Exec(ctx, `INSERT INTO identity.refresh_tokens (id,session_id,token_hash,expires_at,created_at) VALUES ($1,$2,$3,$4,$5)`, successorID, session.ID, successorHash, session.ExpiresAt, now)
	if err != nil {
		return fmt.Errorf("session: insert successor: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity.refresh_tokens SET consumed_at=$1,replaced_by_id=$2 WHERE id=$3`, now, successorID, tokenID)
	if err != nil {
		return fmt.Errorf("session: consume refresh token: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity.sessions SET last_used_at=$1 WHERE id=$2`, now, session.ID)
	if err != nil {
		return fmt.Errorf("session: update last use: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("session: commit rotation: %w", err)
	}
	return nil
}

// Validate confirms an active session belongs to the requested user.
func (r *PostgresSessionRepository) Validate(ctx context.Context, userID, sessionID string, now time.Time) error {
	_, err := r.ValidatePrincipal(ctx, userID, sessionID, now)
	return err
}

// ValidatePrincipal returns Identity's current authorization facts together
// with authoritative session state.
func (r *PostgresSessionRepository) ValidatePrincipal(ctx context.Context, userID, sessionID string, now time.Time) (domain.Principal, error) {
	var principal domain.Principal
	err := r.pool.QueryRow(ctx, `SELECT u.id,u.display_name,u.email,u.role,u.status,s.id
		FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id
		WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND s.expires_at>$3
		  AND u.status='active'`, sessionID, userID, now).Scan(
		&principal.UserID, &principal.Name, &principal.Email, &principal.Role, &principal.Status, &principal.SessionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, port.ErrSessionInvalid
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("session: validate principal: %w", err)
	}
	return principal, nil
}

// Logout revokes an active session.
func (r *PostgresSessionRepository) Logout(ctx context.Context, sessionID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE id=$2 AND revoked_at IS NULL`, now, sessionID)
	if err != nil {
		return fmt.Errorf("session: logout: %w", err)
	}
	return nil
}

// CleanupExpired removes expired session families. Refresh tokens are removed
// by the session foreign key's ON DELETE CASCADE rule.
func (r *PostgresSessionRepository) CleanupExpired(ctx context.Context, now time.Time) (int64, error) {
	result, err := r.pool.Exec(ctx, `DELETE FROM identity.sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, fmt.Errorf("session: clean expired sessions: %w", err)
	}
	return result.RowsAffected(), nil
}
