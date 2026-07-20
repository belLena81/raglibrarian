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

func handler(ctx context.Context) error {
	once.Do(func() { runtime, initializationError = lambdaadapter.NewCleanupRuntime(ctx) })
	if initializationError != nil || runtime == nil {
		return errors.New("retrieval cleanup unavailable")
	}
	return runtime.Cleanup(ctx)
}

func main() { lambda.Start(handler) }
