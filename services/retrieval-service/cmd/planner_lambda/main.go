package main

import (
	"context"
	"errors"
	"sync"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/lambdaadapter"
)

var once sync.Once
var runtime *lambdaadapter.Runtime
var initializationError error

func handler(ctx context.Context, event lambdaadapter.RabbitEvent) error {
	once.Do(func() { runtime, initializationError = lambdaadapter.NewPlannerRuntime(ctx) })
	if initializationError != nil || runtime == nil {
		return errors.New("retrieval planner unavailable")
	}
	return runtime.Plan(ctx, event)
}

func main() { lambda.Start(handler) }
