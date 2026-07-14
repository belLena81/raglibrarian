package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresSessionRepository implements durable sessions. Rotation locks the
// current token and its session in one transaction, preventing double use.
type PostgresSessionRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresSessionRepository(pool *pgxpool.Pool) *PostgresSessionRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresSessionRepository{pool: pool}
}

func (r *PostgresSessionRepository) Create(ctx context.Context, userID string, expiresAt time.Time, tokenHash []byte) (Session, error) {
	now := time.Now().UTC()
	session := Session{ID: uuid.NewString(), UserID: userID, FamilyID: uuid.NewString(), ExpiresAt: expiresAt.UTC()}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("session: begin create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `INSERT INTO identity.sessions (id, user_id, family_id, expires_at, created_at, last_used_at) VALUES ($1,$2,$3,$4,$5,$5)`, session.ID, session.UserID, session.FamilyID, session.ExpiresAt, now)
	if err != nil {
		return Session{}, fmt.Errorf("session: insert: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.refresh_tokens (id, session_id, token_hash, expires_at, created_at) VALUES ($1,$2,$3,$4,$5)`, uuid.NewString(), session.ID, tokenHash, session.ExpiresAt, now)
	if err != nil {
		return Session{}, fmt.Errorf("session: insert refresh token: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("session: commit create: %w", err)
	}
	return session, nil
}

func (r *PostgresSessionRepository) Rotate(ctx context.Context, tokenHash, successorHash []byte, now time.Time) (Session, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("session: begin rotation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var session Session
	var tokenID string
	var consumedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT s.id,s.user_id,s.family_id,s.expires_at,t.id,t.consumed_at FROM identity.refresh_tokens t JOIN identity.sessions s ON s.id=t.session_id WHERE t.token_hash=$1 FOR UPDATE OF t,s`, tokenHash).Scan(&session.ID, &session.UserID, &session.FamilyID, &session.ExpiresAt, &tokenID, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrRefreshTokenInvalid
	}
	if err != nil {
		return Session{}, fmt.Errorf("session: lock refresh token: %w", err)
	}
	if consumedAt != nil {
		_, err = tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE family_id=$2 AND revoked_at IS NULL`, now, session.FamilyID)
		if err != nil {
			return Session{}, fmt.Errorf("session: revoke reused family: %w", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return Session{}, fmt.Errorf("session: commit reuse revocation: %w", err)
		}
		return Session{}, ErrRefreshTokenReused
	}
	if !session.ExpiresAt.After(now) {
		return Session{}, ErrRefreshTokenInvalid
	}

	var revokedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT revoked_at FROM identity.sessions WHERE id=$1`, session.ID).Scan(&revokedAt)
	if err != nil {
		return Session{}, fmt.Errorf("session: check revocation: %w", err)
	}
	if revokedAt != nil {
		return Session{}, ErrRefreshTokenInvalid
	}

	successorID := uuid.NewString()
	_, err = tx.Exec(ctx, `UPDATE identity.refresh_tokens SET consumed_at=$1,replaced_by_id=$2 WHERE id=$3`, now, successorID, tokenID)
	if err != nil {
		return Session{}, fmt.Errorf("session: consume refresh token: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.refresh_tokens (id,session_id,token_hash,expires_at,created_at) VALUES ($1,$2,$3,$4,$5)`, successorID, session.ID, successorHash, session.ExpiresAt, now)
	if err != nil {
		return Session{}, fmt.Errorf("session: insert successor: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity.sessions SET last_used_at=$1 WHERE id=$2`, now, session.ID)
	if err != nil {
		return Session{}, fmt.Errorf("session: update last use: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("session: commit rotation: %w", err)
	}
	return session, nil
}

func (r *PostgresSessionRepository) Validate(ctx context.Context, userID, sessionID string, now time.Time) error {
	var id string
	err := r.pool.QueryRow(ctx, `SELECT id FROM identity.sessions WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>$3`, sessionID, userID, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionInvalid
	}
	if err != nil {
		return fmt.Errorf("session: validate: %w", err)
	}
	return nil
}

func (r *PostgresSessionRepository) Logout(ctx context.Context, sessionID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE id=$2 AND revoked_at IS NULL`, now, sessionID)
	if err != nil {
		return fmt.Errorf("session: logout: %w", err)
	}
	return nil
}
