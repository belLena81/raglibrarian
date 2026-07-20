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
	runtime   plannerRuntime
)

type plannerRuntime interface {
	Plan(context.Context, lambdaadapter.RabbitEvent) error
}

func handler(ctx context.Context, event lambdaadapter.RabbitEvent) error {
	runtimeValue, err := getRuntime(ctx)
	if err != nil || runtimeValue == nil {
		return errors.New("retrieval planner unavailable")
	}
	return runtimeValue.Plan(ctx, event)
}

func getRuntime(ctx context.Context) (plannerRuntime, error) {
	return getRuntimeWithLoader(ctx, func(ctx context.Context) (plannerRuntime, error) {
		return lambdaadapter.NewPlannerRuntime(ctx)
	})
}

func getRuntimeWithLoader(ctx context.Context, load func(context.Context) (plannerRuntime, error)) (plannerRuntime, error) {
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
