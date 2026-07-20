package main

import (
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/lambdaadapter"
)

var (
	runtimeMu sync.Mutex
	runtime   cleanupRuntime
)

type cleanupRuntime interface {
	Cleanup(context.Context) error
}

func handler(ctx context.Context) error {
	runtimeValue, err := getRuntime(ctx)
	if err != nil || runtimeValue == nil {
		return errors.New("retrieval cleanup unavailable")
	}
	return runtimeValue.Cleanup(ctx)
}

func getRuntime(ctx context.Context) (cleanupRuntime, error) {
	return getRuntimeWithLoader(ctx, func(ctx context.Context) (cleanupRuntime, error) {
		return lambdaadapter.NewCleanupRuntime(ctx)
	})
}

func getRuntimeWithLoader(ctx context.Context, load func(context.Context) (cleanupRuntime, error)) (cleanupRuntime, error) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if runtime != nil {
		return runtime, nil
	}
	runtimeValue, err := load(ctx)
	if err != nil {
		return nil, err
	}
	runtime = runtimeValue
	return runtime, nil
}

func main() { lambda.Start(handler) }
