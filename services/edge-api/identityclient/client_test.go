package identityclient

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
)

type fakeRPC struct {
	identityv1.IdentityServiceClient
}
type fakeHealth struct {
	response *grpc_health_v1.HealthCheckResponse
	err      error
}

func (f fakeHealth) Check(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (*grpc_health_v1.HealthCheckResponse, error) {
	return f.response, f.err
}
func (f fakeHealth) List(context.Context, *grpc_health_v1.HealthListRequest, ...grpc.CallOption) (*grpc_health_v1.HealthListResponse, error) {
	return nil, errors.New("not implemented")
}
func (f fakeHealth) Watch(context.Context, *grpc_health_v1.HealthCheckRequest, ...grpc.CallOption) (grpc_health_v1.Health_WatchClient, error) {
	return nil, errors.New("not implemented")
}

func TestRegisterErrorMapping(t *testing.T) {
	assert.ErrorIs(t, mapRegisterError(status.Error(codes.AlreadyExists, "")), authflow.ErrEmailTaken)
	assert.ErrorIs(t, mapRegisterError(status.Error(codes.InvalidArgument, "")), authflow.ErrInvalidRegistration)
	assert.ErrorIs(t, mapRegisterError(status.Error(codes.Unavailable, "")), authflow.ErrUnavailable)
}

func TestCredentialErrorMapping(t *testing.T) {
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.Unauthenticated, "")), authflow.ErrInvalidCredentials)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.DeadlineExceeded, "")), authflow.ErrUnavailable)
}

func TestReadinessRequiresServingHealth(t *testing.T) {
	client := New(&fakeRPC{}, fakeHealth{response: &grpc_health_v1.HealthCheckResponse{Status: grpc_health_v1.HealthCheckResponse_SERVING}})
	assert.NoError(t, client.CheckReady(context.Background()))
	client = New(&fakeRPC{}, fakeHealth{err: errors.New("down")})
	assert.ErrorIs(t, client.CheckReady(context.Background()), authflow.ErrUnavailable)
}

func TestConstructorRequiresBothClients(t *testing.T) {
	assert.Panics(t, func() { New(nil, fakeHealth{}) })
}
