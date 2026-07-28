package retrydelay

import "time"

// CappedExponential returns base*2^attempt capped at maximum.
func CappedExponential(base, maximum time.Duration, attempt int) time.Duration {
	if base <= 0 {
		panic("retrydelay: base must be positive")
	}
	if maximum < base {
		panic("retrydelay: maximum must be at least base")
	}
	if attempt < 0 {
		attempt = 0
	}
	delay := base << min(attempt, 8)
	if delay > maximum {
		return maximum
	}
	return delay
}
