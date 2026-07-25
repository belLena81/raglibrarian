package throttle

import (
	"context"
	"testing"
	"time"
)

func TestLimiterPacesCalls(t *testing.T) {
	limiter, err := New(10)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if wait, err := limiter.Wait(context.Background()); err != nil || wait != 0 {
		t.Fatalf("first Wait() = %s, %v", wait, err)
	}
	if wait, err := limiter.Wait(context.Background()); err != nil || wait < 90*time.Millisecond {
		t.Fatalf("second Wait() = %s, %v", wait, err)
	}
	if elapsed := time.Since(started); elapsed < 90*time.Millisecond {
		t.Fatalf("limiter did not pace calls, elapsed=%s", elapsed)
	}
}

func TestLimiterHonorsContextCancellation(t *testing.T) {
	limiter, err := New(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limiter.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = limiter.Wait(ctx); err == nil {
		t.Fatal("Wait() error = nil")
	}
}

func TestLimiterPacesPerMinuteCalls(t *testing.T) {
	limiter, err := NewPerMinute(15)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := limiter.Wait(context.Background()); err != nil || wait != 0 {
		t.Fatalf("first Wait() = %s, %v", wait, err)
	}
	if wait, err := limiter.Wait(context.Background()); err != nil || wait < 3*time.Second {
		t.Fatalf("second Wait() = %s, %v", wait, err)
	}
}
