package throttle

import (
	"context"
	"errors"
	"sync"
	"time"
)

const maximumRequestsPerSecond = 1000

type Limiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
}

func New(requestsPerSecond int) (*Limiter, error) {
	return newLimiter(time.Second, requestsPerSecond)
}

func NewPerMinute(requestsPerMinute int) (*Limiter, error) {
	return newLimiter(time.Minute, requestsPerMinute)
}

func newLimiter(window time.Duration, requests int) (*Limiter, error) {
	if requests < 0 || requests > maximumRequestsPerSecond {
		return nil, errors.New("invalid rate limit")
	}
	if requests == 0 {
		return nil, nil
	}
	interval := window / time.Duration(requests)
	if interval < time.Nanosecond {
		interval = time.Nanosecond
	}
	return &Limiter{interval: interval}, nil
}

func (l *Limiter) Wait(ctx context.Context) (time.Duration, error) {
	if l == nil {
		return 0, nil
	}
	now := time.Now()
	l.mu.Lock()
	if l.next.IsZero() || !l.next.After(now) {
		l.next = now.Add(l.interval)
		l.mu.Unlock()
		return 0, nil
	}
	wait := l.next.Sub(now)
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return wait, ctx.Err()
	case <-timer.C:
		return wait, nil
	}
}
