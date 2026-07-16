package repository

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const pendingChannel = "identity_pending_librarians_changed"

// PostgresNotifications adapts PostgreSQL notifications into pending-list
// invalidation events.
type PostgresNotifications struct{ pool *pgxpool.Pool }

// NewPostgresNotifications constructs a PostgreSQL notification adapter.
func NewPostgresNotifications(pool *pgxpool.Pool) *PostgresNotifications {
	if pool == nil {
		panic("repository: pgxpool must not be nil")
	}
	return &PostgresNotifications{pool: pool}
}

// Watch returns invalidation events until the context ends or the connection fails.
func (n *PostgresNotifications) Watch(ctx context.Context) (<-chan struct{}, error) {
	connection, err := n.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("notifications: acquire: %w", err)
	}
	if _, err = connection.Exec(ctx, "LISTEN "+pendingChannel); err != nil {
		connection.Release()
		return nil, fmt.Errorf("notifications: listen: %w", err)
	}
	changes := make(chan struct{}, 1)
	go func() {
		defer close(changes)
		defer connection.Release()
		for {
			if _, waitErr := connection.Conn().WaitForNotification(ctx); waitErr != nil {
				return
			}
			select {
			case changes <- struct{}{}:
			default:
			}
		}
	}()
	return changes, nil
}
