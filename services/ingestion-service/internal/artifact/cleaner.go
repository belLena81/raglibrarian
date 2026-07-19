package artifact

import (
	"context"
	"errors"
	"time"
)

type Orphan struct {
	JobID  string
	Prefix string
}

type OrphanRepository interface {
	ClaimOrphans(context.Context, time.Time, time.Time, time.Duration, int) ([]Orphan, error)
	CompleteOrphanCleanup(context.Context, string, time.Time) error
	RetryOrphanCleanup(context.Context, string, time.Time) error
}

type PrefixStore interface {
	DeletePrefix(context.Context, string) error
}

type Cleaner struct {
	repository  OrphanRepository
	store       PrefixStore
	interval    time.Duration
	gracePeriod time.Duration
	now         func() time.Time
}

func NewCleaner(repository OrphanRepository, store PrefixStore, interval, gracePeriod time.Duration) (*Cleaner, error) {
	if repository == nil || store == nil || interval <= 0 || gracePeriod <= 0 {
		return nil, errors.New("invalid artifact cleaner")
	}
	return &Cleaner{repository: repository, store: store, interval: interval, gracePeriod: gracePeriod, now: time.Now}, nil
}

func (c *Cleaner) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		if err := c.runOnce(ctx); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce executes one bounded cleanup pass for scheduled runtimes such as Lambda.
func (c *Cleaner) RunOnce(ctx context.Context) error {
	return c.runOnce(ctx)
}

func (c *Cleaner) runOnce(ctx context.Context) error {
	now := c.now().UTC()
	orphans, err := c.repository.ClaimOrphans(ctx, now, now.Add(-c.gracePeriod), time.Minute, 100)
	if err != nil {
		return err
	}
	var result error
	for _, orphan := range orphans {
		if err = c.store.DeletePrefix(ctx, orphan.Prefix); err != nil {
			result = errors.Join(result, err, c.repository.RetryOrphanCleanup(ctx, orphan.JobID, now))
			continue
		}
		result = errors.Join(result, c.repository.CompleteOrphanCleanup(ctx, orphan.JobID, now))
	}
	return result
}
