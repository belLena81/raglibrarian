package artifact

import (
	"context"
	"errors"
	"time"
)

type OrphanRepository interface {
	OrphanPrefixes(context.Context, time.Time, int) ([]string, error)
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
	prefixes, err := c.repository.OrphanPrefixes(ctx, c.now().UTC().Add(-c.gracePeriod), 100)
	if err != nil {
		return err
	}
	for _, prefix := range prefixes {
		if err = c.store.DeletePrefix(ctx, prefix); err != nil {
			return err
		}
	}
	return nil
}
