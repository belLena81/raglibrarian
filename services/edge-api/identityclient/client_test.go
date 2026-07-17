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
	registerErr      error
	verifyResetErr   error
	completeResetErr error
}

func (f *fakeRPC) Register(context.Context, *identityv1.RegisterRequest, ...grpc.CallOption) (*identityv1.RegisterResponse, error) {
	return &identityv1.RegisterResponse{Accepted: true}, f.registerErr
}

func (f *fakeRPC) VerifyPasswordReset(context.Context, *identityv1.PasswordResetVerifyRequest, ...grpc.CallOption) (*identityv1.PasswordResetVerifyResponse, error) {
	return &identityv1.PasswordResetVerifyResponse{ResetGrant: "grant", AvailableRoles: []string{"reader"}}, f.verifyResetErr
}

func (f *fakeRPC) CompletePasswordReset(context.Context, *identityv1.PasswordResetCompleteRequest, ...grpc.CallOption) (*identityv1.PasswordResetCompleteResponse, error) {
	return &identityv1.PasswordResetCompleteResponse{}, f.completeResetErr
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
	assert.NoError(t, mapRegisterError(status.Error(codes.AlreadyExists, "")))
	assert.ErrorIs(t, mapRegisterError(status.Error(codes.InvalidArgument, "")), authflow.ErrInvalidRegistration)
	assert.ErrorIs(t, mapRegisterError(status.Error(codes.Unavailable, "")), authflow.ErrUnavailable)
}

func TestRegisterNormalizesLegacyDuplicateAsAccepted(t *testing.T) {
	client := New(&fakeRPC{registerErr: status.Error(codes.AlreadyExists, "legacy duplicate")}, fakeHealth{})
	assert.NoError(t, client.Register(context.Background(), "Reader", "reader@example.test", "password-1234", "reader"))
}

func TestPasswordResetErrorMapping(t *testing.T) {
	assert.NoError(t, mapPasswordResetError(nil))
	assert.ErrorIs(t, mapPasswordResetError(status.Error(codes.InvalidArgument, "")), authflow.ErrInvalidPasswordReset)
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Unknown} {
		assert.ErrorIs(t, mapPasswordResetError(status.Error(code, "")), authflow.ErrUnavailable, code.String())
	}
}

func TestPasswordResetClientPreservesOutages(t *testing.T) {
	client := New(&fakeRPC{
		verifyResetErr:   status.Error(codes.Unavailable, "database unavailable"),
		completeResetErr: status.Error(codes.DeadlineExceeded, "database timeout"),
	}, fakeHealth{})
	_, _, err := client.VerifyPasswordReset(context.Background(), "reader@example.test", "123456")
	assert.ErrorIs(t, err, authflow.ErrUnavailable)
	assert.ErrorIs(t, client.CompletePasswordReset(context.Background(), "grant", "reader", "password-1234"), authflow.ErrUnavailable)
}

func TestCredentialErrorMapping(t *testing.T) {
	assert.NoError(t, mapCredentialError(nil))
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.InvalidArgument, "")), authflow.ErrInvalidCredentials)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.Unauthenticated, "")), authflow.ErrInvalidCredentials)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.Internal, "")), authflow.ErrUnavailable)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.Unavailable, "")), authflow.ErrUnavailable)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.DeadlineExceeded, "")), authflow.ErrUnavailable)
	assert.ErrorIs(t, mapCredentialError(status.Error(codes.Canceled, "")), authflow.ErrUnavailable)
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
