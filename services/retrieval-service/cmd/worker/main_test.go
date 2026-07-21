package main

import (
	"context"
	"errors"
	"testing"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/diagnostic"
)

type stubRuntime struct {
	runErr error
	calls  int
}

func (s *stubRuntime) Run(context.Context) error {
	s.calls++
	return s.runErr
}

func TestRunDropsPrivilegesBeforeRuntimeConstruction(t *testing.T) {
	previousLoad := loadWorkerConfig
	previousDrop := dropPrivileges
	previousNew := newRuntime
	t.Cleanup(func() {
		loadWorkerConfig = previousLoad
		dropPrivileges = previousDrop
		newRuntime = previousNew
	})

	steps := make([]string, 0, 3)
	cfg := config.WorkerConfig{RunAs: process.Identity{UID: 123, GID: 456}}
	runtimeValue := &stubRuntime{}

	loadWorkerConfig = func() (config.WorkerConfig, error) {
		steps = append(steps, "load")
		return cfg, nil
	}
	dropPrivileges = func(identity process.Identity) error {
		if identity != cfg.RunAs {
			t.Fatalf("dropPrivileges() identity = %#v", identity)
		}
		steps = append(steps, "drop")
		return nil
	}
	newRuntime = func(context.Context, config.WorkerConfig, *diagnostic.Recorder) (runtime, error) {
		steps = append(steps, "new")
		return runtimeValue, nil
	}

	err := run(context.Background(), logger.Must("retrieval-worker-test"))
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if runtimeValue.calls != 1 {
		t.Fatalf("runtime Run() calls = %d", runtimeValue.calls)
	}
	if len(steps) != 3 || steps[0] != "load" || steps[1] != "drop" || steps[2] != "new" {
		t.Fatalf("run() steps = %#v", steps)
	}
}

func TestRunStopsWhenPrivilegeDropFails(t *testing.T) {
	previousLoad := loadWorkerConfig
	previousDrop := dropPrivileges
	previousNew := newRuntime
	t.Cleanup(func() {
		loadWorkerConfig = previousLoad
		dropPrivileges = previousDrop
		newRuntime = previousNew
	})

	dropErr := errors.New("permission denied")
	cfg := config.WorkerConfig{RunAs: process.Identity{UID: 123, GID: 456}}
	loadWorkerConfig = func() (config.WorkerConfig, error) {
		return cfg, nil
	}
	dropPrivileges = func(process.Identity) error {
		return dropErr
	}
	newRuntime = func(context.Context, config.WorkerConfig, *diagnostic.Recorder) (runtime, error) {
		t.Fatal("newRuntime() should not be called after privilege-drop failure")
		return nil, nil
	}

	err := run(context.Background(), logger.Must("retrieval-worker-test"))
	if !errors.Is(err, dropErr) {
		t.Fatalf("run() error = %v, want %v", err, dropErr)
	}
}
