package main

import (
	"context"
	"errors"
	"testing"

	"github.com/belLena81/raglibrarian/services/retrieval-service/internal/lambdaadapter"
)

type stubIndexRuntime struct{}

func (stubIndexRuntime) Index(context.Context, lambdaadapter.RabbitEvent) error { return nil }

func TestGetRuntimeRetriesAfterInitializationFailure(t *testing.T) {
	runtimeMu.Lock()
	runtime = nil
	runtimeMu.Unlock()
	loads := 0
	bootstrapErr := errors.New("bootstrap unavailable")

	first, err := getRuntimeWithLoader(context.Background(), func(context.Context) (indexRuntime, error) {
		loads++
		return nil, bootstrapErr
	})
	if !errors.Is(err, bootstrapErr) || first != nil {
		t.Fatalf("first getRuntimeWithLoader() = %#v, %v", first, err)
	}
	second, err := getRuntimeWithLoader(context.Background(), func(context.Context) (indexRuntime, error) {
		loads++
		return stubIndexRuntime{}, nil
	})
	if err != nil || second == nil || loads != 2 {
		t.Fatalf("second getRuntimeWithLoader() = %#v, %v loads=%d", second, err, loads)
	}
	third, err := getRuntimeWithLoader(context.Background(), func(context.Context) (indexRuntime, error) {
		loads++
		return nil, errors.New("must not reload")
	})
	if err != nil || third == nil || loads != 2 {
		t.Fatalf("cached getRuntimeWithLoader() = %#v, %v loads=%d", third, err, loads)
	}
}
