package storage

import "context"

type ReadinessProbe interface {
	Ready(context.Context) bool
}

// AllReady checks only controlled bucket metadata through narrow adapters. It
// never reads uploaded object bodies.
func AllReady(ctx context.Context, probes ...ReadinessProbe) bool {
	if len(probes) == 0 {
		return false
	}
	for _, probe := range probes {
		if probe == nil || !probe.Ready(ctx) {
			return false
		}
	}
	return true
}
