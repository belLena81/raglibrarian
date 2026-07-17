package app

import (
	"context"
	"testing"
	"time"
)

func TestRunHealthUpdatesStopsWhenWorkerContextIsCancelled(t *testing.T) {
	parentCtx := context.Background()
	workerCtx, cancelWorker := context.WithCancel(parentCtx)
	done := make(chan struct{})

	go func() {
		runHealthUpdates(workerCtx, time.Hour, func() {})
		close(done)
	}()

	cancelWorker()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("health worker did not stop after worker context cancellation")
	}

	select {
	case <-parentCtx.Done():
		t.Fatal("parent context was unexpectedly cancelled")
	default:
	}
}
