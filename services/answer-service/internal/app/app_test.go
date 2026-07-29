package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunServerGroupCleansUpAllServersOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	var cleanupCalls atomic.Int32
	servers := []serverRunner{
		blockingServer("one", stopped),
		blockingServer("two", stopped),
	}
	cancel()

	err := runServerGroup(ctx, servers, func() {
		if cleanupCalls.Add(1) == 1 {
			close(stopped)
		}
	}, time.Second)

	if err != nil {
		t.Fatalf("runServerGroup() error = %v", err)
	}
	if cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls.Load())
	}
}

func TestRunServerGroupCleansUpSiblingAndReturnsServeFailure(t *testing.T) {
	stopped := make(chan struct{})
	var cleanupCalls atomic.Int32
	servers := []serverRunner{
		{name: "gRPC", serve: func() error { return errors.New("private listener detail") }},
		blockingServer("diagnostics", stopped),
	}

	err := runServerGroup(context.Background(), servers, func() {
		if cleanupCalls.Add(1) == 1 {
			close(stopped)
		}
	}, time.Second)

	if err == nil || !strings.Contains(err.Error(), "gRPC listener failed") ||
		strings.Contains(err.Error(), "private listener detail") {
		t.Fatalf("runServerGroup() error = %v", err)
	}
	if cleanupCalls.Load() != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls.Load())
	}
}

func TestRunServerGroupTreatsUnexpectedCleanExitAsFailure(t *testing.T) {
	stopped := make(chan struct{})
	servers := []serverRunner{
		{name: "diagnostics", serve: func() error { return nil }},
		blockingServer("gRPC", stopped),
	}

	err := runServerGroup(context.Background(), servers, func() {
		close(stopped)
	}, time.Second)

	if err == nil || !strings.Contains(err.Error(), "diagnostics listener stopped unexpectedly") {
		t.Fatalf("runServerGroup() error = %v", err)
	}
}

func TestRunServerGroupBoundsSiblingJoinAfterCleanup(t *testing.T) {
	neverStops := make(chan struct{})
	defer close(neverStops)
	servers := []serverRunner{
		{name: "gRPC", serve: func() error { return errors.New("failed") }},
		blockingServer("diagnostics", neverStops),
	}

	started := time.Now()
	err := runServerGroup(context.Background(), servers, func() {}, 20*time.Millisecond)

	if err == nil || !strings.Contains(err.Error(), "listeners did not stop") {
		t.Fatalf("runServerGroup() error = %v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatalf("runServerGroup() exceeded bounded join: %s", time.Since(started))
	}
}

func blockingServer(name string, stopped <-chan struct{}) serverRunner {
	return serverRunner{
		name: name,
		serve: func() error {
			<-stopped
			return nil
		},
	}
}
