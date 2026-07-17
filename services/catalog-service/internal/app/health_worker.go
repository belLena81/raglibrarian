package app

import (
	"context"
	"time"
)

// runHealthUpdates refreshes health state until the worker group is stopped.
func runHealthUpdates(ctx context.Context, interval time.Duration, update func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}
