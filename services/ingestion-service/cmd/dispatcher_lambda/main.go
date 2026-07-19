package main

import (
	"context"
	"sync"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
)

var (
	runtimeMu sync.Mutex
	runtime   *bootstrap.DispatcherRuntime
)

func main() { lambda.Start(handle) }

func handle(ctx context.Context) error {
	runtimeValue, err := getRuntime(ctx)
	if err != nil {
		return err
	}
	return runtimeValue.Outbox.PublishPending(ctx)
}

func getRuntime(ctx context.Context) (*bootstrap.DispatcherRuntime, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtime != nil {
		return runtime, nil
	}
	cfg, err := config.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	runtimeValue, err := bootstrap.NewDispatcher(ctx, cfg)
	if err != nil {
		return nil, err
	}
	runtime = runtimeValue
	return runtime, nil
}
