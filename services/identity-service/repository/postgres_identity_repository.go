package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

// PostgresIdentityRepository persists identity aggregates, verification state,
// approvals, and the encrypted email outbox.
type PostgresIdentityRepository struct{ pool *pgxpool.Pool }

// NewPostgresIdentityRepository constructs the Identity PostgreSQL adapter.
func NewPostgresIdentityRepository(pool *pgxpool.Pool) *PostgresIdentityRepository {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresIdentityRepository{pool: pool}
}

// CreateOrIgnore atomically creates a registration and its encrypted outbox
// message while preserving a generic duplicate-email result.
func (r *PostgresIdentityRepository) CreateOrIgnore(ctx context.Context, registration port.VerificationRegistration, email port.SealedEmail) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("verification: begin registration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (
        SELECT 1 FROM identity.users WHERE email=$1 OR email_fingerprint=$2
        UNION ALL
        SELECT 1 FROM identity.registration_verifications WHERE email_fingerprint=$2 AND consumed_at IS NULL
    )`, registration.Email, registration.EmailFingerprint).Scan(&exists)
	if err != nil {
		return fmt.Errorf("verification: check registration: %w", err)
	}
	if exists {
		return tx.Commit(ctx)
	}
	result, err := tx.Exec(ctx, `INSERT INTO identity.registration_verifications
        (id,token_hash,display_name,email,email_fingerprint,password_hash,role,expires_at,last_sent_at,created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
        ON CONFLICT DO NOTHING`,
		registration.ID, registration.TokenHash, registration.Name, registration.Email,
		registration.EmailFingerprint, registration.PasswordHash, string(registration.Role),
		registration.ExpiresAt, registration.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("verification: insert registration: %w", err)
	}
	if result.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	if err = insertEmailOutbox(ctx, tx, email); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("verification: commit registration: %w", err)
	}
	return nil
}

// RotateForResend replaces eligible verification material and schedules its
// encrypted email in one transaction.
func (r *PostgresIdentityRepository) RotateForResend(ctx context.Context, normalizedEmail string, tokenHash []byte, expiresAt, cooldownBefore time.Time, email port.SealedEmail) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("verification: begin resend: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE identity.registration_verifications
        SET token_hash=$1,expires_at=$2,last_sent_at=$3
        WHERE email=$4 AND consumed_at IS NULL AND last_sent_at <= $5`,
		tokenHash, expiresAt, email.CreatedAt, normalizedEmail, cooldownBefore,
	)
	if err != nil {
		return fmt.Errorf("verification: rotate resend: %w", err)
	}
	if result.RowsAffected() == 1 {
		if err = insertEmailOutbox(ctx, tx, email); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("verification: commit resend: %w", err)
	}
	return nil
}

func insertEmailOutbox(ctx context.Context, tx pgx.Tx, email port.SealedEmail) error {
	_, err := tx.Exec(ctx, `INSERT INTO identity.email_outbox
        (id,message_type,key_id,nonce,ciphertext,next_attempt_at,created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$6)`,
		email.ID, email.MessageType, email.KeyID, email.Nonce, email.Ciphertext, email.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("verification: insert email outbox: %w", err)
	}
	return nil
}

// Consume converts a valid single-use verification registration into a user.
func (r *PostgresIdentityRepository) Consume(ctx context.Context, tokenHash []byte, userID string, now time.Time) (domain.User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("verification: begin consume: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var (
		verificationID, name, email, passwordHash, roleValue string
		fingerprint                                          []byte
		createdAt                                            time.Time
	)
	err = tx.QueryRow(ctx, `SELECT id,display_name,email,email_fingerprint,password_hash,role,created_at
        FROM identity.registration_verifications
        WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>$2
        FOR UPDATE`, tokenHash, now).Scan(
		&verificationID, &name, &email, &fingerprint, &passwordHash, &roleValue, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrInvalidVerification
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("verification: lock token: %w", err)
	}
	role := domain.Role(roleValue)
	status := domain.StatusActive
	if role == domain.RoleLibrarian {
		status = domain.StatusPending
	}
	user, err := domain.NewVerifiedUser(userID, name, email, fingerprint, passwordHash, role, status, now, createdAt)
	if err != nil {
		return domain.User{}, domain.ErrInvalidVerification
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.users
        (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		user.ID(), user.Name(), user.Email(), user.EmailFingerprint(), user.PasswordHash(),
		string(user.Role()), string(user.Status()), user.VerifiedAt(), user.CreatedAt(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.User{}, domain.ErrInvalidVerification
		}
		return domain.User{}, fmt.Errorf("verification: insert user: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity.registration_verifications SET consumed_at=$1 WHERE id=$2`, now, verificationID)
	if err != nil {
		return domain.User{}, fmt.Errorf("verification: mark consumed: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("verification: commit consume: %w", err)
	}
	return user, nil
}

// CleanupExpired removes verification registrations older than the retention cutoff.
func (r *PostgresIdentityRepository) CleanupExpired(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.pool.Exec(ctx, `DELETE FROM identity.registration_verifications
        WHERE (consumed_at IS NOT NULL AND consumed_at<$1) OR (consumed_at IS NULL AND expires_at<$1)`, before)
	if err != nil {
		return 0, fmt.Errorf("verification: cleanup: %w", err)
	}
	return result.RowsAffected(), nil
}

// Required reports whether the database contains no administrator account.
func (r *PostgresIdentityRepository) Required(ctx context.Context) (bool, error) {
	var required bool
	err := r.pool.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM identity.users WHERE role='admin')`).Scan(&required)
	if err != nil {
		return false, fmt.Errorf("bootstrap: status: %w", err)
	}
	return required, nil
}

// CreateAdmin serializes and persists the one-time first-administrator transition.
func (r *PostgresIdentityRepository) CreateAdmin(ctx context.Context, user domain.User) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("bootstrap: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734602101)`); err != nil {
		return fmt.Errorf("bootstrap: lock: %w", err)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users WHERE role='admin')`).Scan(&exists); err != nil {
		return fmt.Errorf("bootstrap: check: %w", err)
	}
	if exists {
		return domain.ErrBootstrapComplete
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.users
        (id,display_name,email,email_fingerprint,password_hash,role,status,email_verified_at,created_at)
        VALUES ($1,$2,$3,$4,$5,'admin','active',$6,$7)`,
		user.ID(), user.Name(), user.Email(), user.EmailFingerprint(), user.PasswordHash(), user.VerifiedAt(), user.CreatedAt())
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrBootstrapComplete
		}
		return fmt.Errorf("bootstrap: insert: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("bootstrap: commit: %w", err)
	}
	return nil
}

// ListPending validates the actor against live state and returns a stable page
// of pending librarians.
func (r *PostgresIdentityRepository) ListPending(ctx context.Context, actor domain.Principal, size int, cursor *port.PendingCursor, now time.Time) (port.PendingPage, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return port.PendingPage{}, fmt.Errorf("approval: begin list: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = validateActor(ctx, tx, actor, now, false); err != nil {
		return port.PendingPage{}, err
	}
	query := `SELECT id,display_name,email,password_hash,email_fingerprint,role,status,email_verified_at,created_at,reviewed_by,reviewed_at
        FROM identity.users WHERE role='librarian' AND status='pending'`
	args := []any{}
	if cursor != nil {
		query += ` AND (created_at,id)>($1,$2)`
		args = append(args, cursor.CreatedAt, cursor.UserID)
	}
	query += fmt.Sprintf(" ORDER BY created_at,id LIMIT $%d", len(args)+1)
	args = append(args, size+1)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return port.PendingPage{}, fmt.Errorf("approval: list: %w", err)
	}
	defer rows.Close()
	users := make([]domain.User, 0, size+1)
	for rows.Next() {
		user, scanErr := scanIdentityUser(rows)
		if scanErr != nil {
			return port.PendingPage{}, scanErr
		}
		users = append(users, user)
	}
	if err = rows.Err(); err != nil {
		return port.PendingPage{}, fmt.Errorf("approval: list rows: %w", err)
	}
	page := port.PendingPage{Users: users}
	if len(users) > size {
		last := users[size-1]
		page.Users = users[:size]
		page.Next = &port.PendingCursor{CreatedAt: last.CreatedAt(), UserID: last.ID()}
	}
	if err = tx.Commit(ctx); err != nil {
		return port.PendingPage{}, fmt.Errorf("approval: commit list: %w", err)
	}
	return page, nil
}

// Decide validates a live administrator and records one final librarian decision.
func (r *PostgresIdentityRepository) Decide(ctx context.Context, actor domain.Principal, targetID string, targetStatus domain.Status, now time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("approval: begin decision: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	principal, err := validateActor(ctx, tx, actor, now, true)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE identity.users
        SET status=$1,reviewed_by=$2,reviewed_at=$3
        WHERE id=$4 AND role='librarian' AND status='pending'`,
		string(targetStatus), principal.UserID, now, targetID)
	if err != nil {
		return fmt.Errorf("approval: update: %w", err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$1 WHERE user_id=$2 AND revoked_at IS NULL`, now, targetID)
	if err != nil {
		return fmt.Errorf("approval: revoke sessions: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("approval: commit: %w", err)
	}
	return nil
}

func validateActor(ctx context.Context, tx pgx.Tx, actor domain.Principal, now time.Time, lock bool) (domain.Principal, error) {
	query := `SELECT u.id,u.display_name,u.email,u.role,u.status,s.id
        FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id
        WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND s.expires_at>$3
          AND u.role='admin' AND u.status='active'`
	if lock {
		query += ` FOR UPDATE OF s,u`
	}
	var principal domain.Principal
	err := tx.QueryRow(ctx, query, actor.SessionID, actor.UserID, now).Scan(
		&principal.UserID, &principal.Name, &principal.Email, &principal.Role, &principal.Status, &principal.SessionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Principal{}, domain.ErrForbidden
	}
	if err != nil {
		return domain.Principal{}, fmt.Errorf("approval: validate actor: %w", err)
	}
	return principal, nil
}

// CleanupRejected removes rejected accounts older than the retention cutoff.
func (r *PostgresIdentityRepository) CleanupRejected(ctx context.Context, before time.Time) (int64, error) {
	result, err := r.pool.Exec(ctx, `UPDATE identity.users
		SET display_name=NULL,email=NULL,email_fingerprint=NULL,password_hash=NULL
		WHERE role='librarian' AND status='rejected' AND reviewed_at<$1
		  AND (display_name IS NOT NULL OR email IS NOT NULL OR email_fingerprint IS NOT NULL OR password_hash IS NOT NULL)`, before)
	if err != nil {
		return 0, fmt.Errorf("approval: cleanup rejected: %w", err)
	}
	return result.RowsAffected(), nil
}

func scanIdentityUser(row pgx.Row) (domain.User, error) {
	var (
		id, name, roleValue, statusValue string
		email, passwordHash              pgtype.Text
		fingerprint                      []byte
		verifiedAt, createdAt            time.Time
		reviewedBy                       pgtype.Text
		reviewedAt                       pgtype.Timestamptz
	)
	if err := row.Scan(&id, &name, &email, &passwordHash, &fingerprint, &roleValue, &statusValue, &verifiedAt, &createdAt, &reviewedBy, &reviewedAt); err != nil {
		return domain.User{}, fmt.Errorf("identity: scan user: %w", err)
	}
	return domain.RehydrateUser(id, name, email.String, passwordHash.String, fingerprint, domain.Role(roleValue), domain.Status(statusValue), verifiedAt, createdAt, reviewedBy.String, reviewedAt.Time), nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}

// Claim leases a bounded batch of due encrypted outbox messages.
func (r *PostgresIdentityRepository) Claim(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]port.EmailDelivery, error) {
	if limit < 1 || limit > 100 {
		return nil, domain.ErrConflict
	}
	rows, err := r.pool.Query(ctx, `WITH candidates AS (
		SELECT id FROM identity.email_outbox
		WHERE delivered_at IS NULL AND attempts < 10 AND next_attempt_at <= $1
		  AND (leased_until IS NULL OR leased_until < $1)
		ORDER BY next_attempt_at,created_at
		FOR UPDATE SKIP LOCKED LIMIT $2
	)
	UPDATE identity.email_outbox o
	SET leased_until=$3,attempts=attempts+1
	FROM candidates c WHERE o.id=c.id
	RETURNING o.id,o.key_id,o.nonce,o.ciphertext,o.attempts`, now, limit, now.Add(lease))
	if err != nil {
		return nil, fmt.Errorf("email outbox: claim: %w", err)
	}
	defer rows.Close()
	var deliveries []port.EmailDelivery
	for rows.Next() {
		var delivery port.EmailDelivery
		if err = rows.Scan(&delivery.ID, &delivery.KeyID, &delivery.Nonce, &delivery.Ciphertext, &delivery.Attempts); err != nil {
			return nil, fmt.Errorf("email outbox: scan claim: %w", err)
		}
		deliveries = append(deliveries, delivery)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("email outbox: claim rows: %w", err)
	}
	return deliveries, nil
}

// Delivered marks an outbox message as successfully delivered.
func (r *PostgresIdentityRepository) Delivered(ctx context.Context, id string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `UPDATE identity.email_outbox
		SET delivered_at=$1,nonce=NULL,ciphertext=NULL,leased_until=NULL
		WHERE id=$2 AND delivered_at IS NULL`, now, id)
	if err != nil {
		return fmt.Errorf("email outbox: mark delivered: %w", err)
	}
	return nil
}

// Failed schedules an outbox retry or marks delivery as permanently exhausted.
func (r *PostgresIdentityRepository) Failed(ctx context.Context, id string, retryAt time.Time, terminal bool) error {
	if terminal {
		_, err := r.pool.Exec(ctx, `UPDATE identity.email_outbox SET attempts=10,leased_until=NULL WHERE id=$1`, id)
		if err != nil {
			return fmt.Errorf("email outbox: mark terminal: %w", err)
		}
		return nil
	}
	_, err := r.pool.Exec(ctx, `UPDATE identity.email_outbox
		SET next_attempt_at=$1,leased_until=NULL WHERE id=$2 AND delivered_at IS NULL`, retryAt, id)
	if err != nil {
		return fmt.Errorf("email outbox: retry: %w", err)
	}
	return nil
}
