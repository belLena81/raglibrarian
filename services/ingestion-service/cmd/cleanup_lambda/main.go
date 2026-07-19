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
	runtimeOnce   sync.Once
	sharedRuntime *bootstrap.CleanupRuntime
	runtimeError  error
)

func main() { lambda.Start(handle) }

func handle(ctx context.Context) error {
	runtimeOnce.Do(func() {
		if os.Geteuid() == 0 {
			runtimeError = errors.New("cleanup lambda runtime must be non-root")
			return
		}
		cfg, err := config.LoadCleanup()
		if err != nil {
			runtimeError = err
			return
		}
		sharedRuntime, runtimeError = bootstrap.NewCleanup(ctx, cfg)
	})
	if runtimeError != nil {
		return runtimeError
	}
	return sharedRuntime.Cleaner.RunOnce(ctx)
}
