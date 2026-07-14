package identityclient

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
)

type fakeHealthClient struct {
	response *grpc_health_v1.HealthCheckResponse
	err      error
}

func (f fakeHealthClient) Check(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	return f.response, f.err
}

func (f fakeHealthClient) List(context.Context, *grpc_health_v1.HealthListRequest, ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, errors.New("not implemented")
}

func (f fakeHealthClient) Watch(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (grpc_health_v1.Health_WatchClient, error) {
	return nil, errors.New("not implemented")
}

func TestClient_CheckReady_ReturnsNilForServingIdentity(t *testing.T) {
	client := New(nil, fakeHealthClient{response: &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}})

	err := client.CheckReady(context.Background())

	assert.NoError(t, err)
}

func TestClient_CheckReady_ReturnsUnavailableWhenHealthIsNotServing(t *testing.T) {
	client := New(nil, fakeHealthClient{response: &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
	}})

	err := client.CheckReady(context.Background())

	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestClient_CheckReady_ReturnsUnavailableWhenHealthCheckFails(t *testing.T) {
	client := New(nil, fakeHealthClient{err: errors.New("connection refused")})

	err := client.CheckReady(context.Background())

	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestClient_CheckReady_ReturnsUnavailableWithoutHealthClient(t *testing.T) {
	client := New(nil)

	err := client.CheckReady(context.Background())

	assert.ErrorIs(t, err, ErrUnavailable)
}
