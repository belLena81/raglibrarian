package main

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/bootstrap"
)

var (
	runtimeMu     sync.Mutex
	sharedRuntime *bootstrap.CleanupRuntime
)

func main() { lambda.Start(handle) }

func handle(ctx context.Context) error {
	runtimeValue, err := getRuntime(ctx)
	if err != nil {
		return err
	}
	return runtimeValue.Cleaner.RunOnce(ctx)
}

func getRuntime(ctx context.Context) (*bootstrap.CleanupRuntime, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if sharedRuntime != nil {
		return sharedRuntime, nil
	}
	if os.Geteuid() == 0 {
		return nil, errors.New("cleanup lambda runtime must be non-root")
	}
	cfg, err := config.LoadCleanupContext(ctx)
	if err != nil {
		return nil, err
	}
	runtimeValue, err := bootstrap.NewCleanup(ctx, cfg)
	if err != nil {
		return nil, err
	}
	sharedRuntime = runtimeValue
	return sharedRuntime, nil
}
