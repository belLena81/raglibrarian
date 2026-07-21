package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/pkg/process"
	"github.com/belLena81/raglibrarian/services/retrieval-service/config"
	"github.com/belLena81/raglibrarian/services/retrieval-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/worker"
	"go.uber.org/zap"
)

type runtime interface {
	Run(context.Context) error
}

var (
	loadWorkerConfig = config.LoadWorker
	newRuntime       = func(ctx context.Context, configuration config.WorkerConfig, recorder *diagnostic.Recorder) (runtime, error) {
		return worker.New(ctx, configuration, recorder)
	}
	dropPrivileges = process.DropPrivileges
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, logger.Must("retrieval-worker")); err != nil {
		worker.LogFailure()
		os.Exit(1)
	}
}

func run(ctx context.Context, log *zap.Logger) error {
	configuration, err := loadWorkerConfig()
	if err != nil {
		return err
	}
	if err = dropPrivileges(configuration.RunAs); err != nil {
		return err
	}
	runtimeValue, err := newRuntime(ctx, configuration, diagnostic.New(log))
	if err != nil {
		return err
	}
	return runtimeValue.Run(ctx)
}
