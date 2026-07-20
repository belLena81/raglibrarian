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
	once.Do(func() { runtime, initializationError = lambdaadapter.NewIndexerRuntime(ctx) })
	if initializationError != nil || runtime == nil {
		return errors.New("retrieval indexer unavailable")
	}
	return runtime.Index(ctx, event)
}

func main() { lambda.Start(handler) }
