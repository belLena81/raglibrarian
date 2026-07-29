package throttle

import (
	"context"
	"errors"
	"sync"
	"time"
)

type reservation struct {
	target time.Time
}

type Limiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
	waiters  []*reservation
	notify   chan struct{}
}

func New(requestsPerSecond int) (*Limiter, error) {
	return newLimiter(time.Second, requestsPerSecond)
}

func NewPerMinute(requestsPerMinute int) (*Limiter, error) {
	return newLimiter(time.Minute, requestsPerMinute)
}

func newLimiter(window time.Duration, requests int) (*Limiter, error) {
	if requests < 0 {
		return nil, errors.New("invalid rate limit")
	}
	if requests == 0 {
		return nil, nil
	}
	interval := window / time.Duration(requests)
	if interval < time.Nanosecond {
		interval = time.Nanosecond
	}
	return &Limiter{interval: interval, notify: make(chan struct{})}, nil
}

func (l *Limiter) Wait(ctx context.Context) (time.Duration, error) {
	if l == nil {
		return 0, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	started := time.Now()
	reservation := &reservation{}
	l.mu.Lock()
	l.waiters = append(l.waiters, reservation)
	l.rebalanceLocked(time.Now())
	l.signalLocked()
	l.mu.Unlock()

	for {
		l.mu.Lock()
		index := l.indexOfReservationLocked(reservation)
		if index < 0 {
			l.mu.Unlock()
			return 0, nil
		}
		now := time.Now()
		if index == 0 && !reservation.target.After(now) {
			if err := ctx.Err(); err != nil {
				l.waiters = l.waiters[1:]
				l.rebalanceLocked(now)
				l.signalLocked()
				l.mu.Unlock()
				return time.Since(started), err
			}
			l.waiters = l.waiters[1:]
			l.next = now.Add(l.interval)
			l.rebalanceLocked(now)
			l.signalLocked()
			l.mu.Unlock()
			return 0, nil
		}
		wait := reservation.target.Sub(now)
		if wait < 0 {
			wait = 0
		}
		notify := l.notify
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			l.mu.Lock()
			if index := l.indexOfReservationLocked(reservation); index >= 0 {
				l.waiters = append(l.waiters[:index], l.waiters[index+1:]...)
				l.rebalanceLocked(time.Now())
				l.signalLocked()
			}
			l.mu.Unlock()
			return time.Since(started), ctx.Err()
		case <-timer.C:
			l.mu.Lock()
			now := time.Now()
			if index := l.indexOfReservationLocked(reservation); index < 0 {
				l.mu.Unlock()
				return time.Since(started), nil
			} else if index == 0 && !reservation.target.After(now) {
				if err := ctx.Err(); err != nil {
					l.waiters = append(l.waiters[:index], l.waiters[index+1:]...)
					l.rebalanceLocked(now)
					l.signalLocked()
					l.mu.Unlock()
					return time.Since(started), err
				}
				l.waiters = append(l.waiters[:index], l.waiters[index+1:]...)
				l.next = now.Add(l.interval)
				l.rebalanceLocked(now)
				l.signalLocked()
				l.mu.Unlock()
				return time.Since(started), nil
			}
			l.mu.Unlock()
			continue
		case <-notify:
			stopTimer(timer)
		}
	}
}

func (l *Limiter) indexOfReservationLocked(target *reservation) int {
	for index, candidate := range l.waiters {
		if candidate == target {
			return index
		}
	}
	return -1
}

func (l *Limiter) rebalanceLocked(now time.Time) {
	start := l.next
	if start.IsZero() || start.Before(now) {
		start = now
	}
	for index, waiter := range l.waiters {
		waiter.target = start.Add(time.Duration(index) * l.interval)
	}
}

func (l *Limiter) signalLocked() {
	close(l.notify)
	l.notify = make(chan struct{})
}

func stopTimer(timer *time.Timer) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
