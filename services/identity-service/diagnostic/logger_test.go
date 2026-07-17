package diagnostic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryServerInterceptorLogsAllowlistedOperationOnly(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	recorder := New(zap.New(core))
	request := "sensitive-request"
	response, err := recorder.UnaryServerInterceptor(
		context.Background(), request,
		&grpc.UnaryServerInfo{FullMethod: "/identity.v1.IdentityService/Register"},
		func(context.Context, any) (any, error) {
			return "sensitive-response", status.Error(codes.InvalidArgument, "sensitive-error")
		},
	)

	assert.Equal(t, "sensitive-response", response)
	require.Error(t, err)
	require.Len(t, logs.All(), 1)
	entry := logs.All()[0]
	assert.Equal(t, "grpc.request.completed", entry.Message)
	assert.Equal(t, "register", entry.ContextMap()["operation"])
	assert.Equal(t, "InvalidArgument", entry.ContextMap()["code"])
	serialized := entry.Message + " " + entry.ContextMap()["operation"].(string) + " " + entry.ContextMap()["code"].(string)
	assert.NotContains(t, serialized, request)
	assert.NotContains(t, serialized, "sensitive-response")
	assert.NotContains(t, serialized, "sensitive-error")
}

func TestUnaryServerInterceptorSkipsUnknownOperation(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	recorder := New(zap.New(core))

	_, err := recorder.UnaryServerInterceptor(
		context.Background(), "request",
		&grpc.UnaryServerInfo{FullMethod: "/identity.v1.IdentityService/Unknown"},
		func(context.Context, any) (any, error) { return nil, nil },
	)

	require.NoError(t, err)
	assert.Zero(t, logs.Len())
}

func TestUnaryServerInterceptorLogsBootstrapAdmin(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	recorder := New(zap.New(core))

	_, err := recorder.UnaryServerInterceptor(
		context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/identity.v1.IdentityService/BootstrapAdmin"},
		func(context.Context, any) (any, error) { return nil, nil },
	)

	require.NoError(t, err)
	require.Len(t, logs.All(), 1)
	assert.Equal(t, "create_admin", logs.All()[0].ContextMap()["operation"])
}

func TestStreamServerInterceptorLogsAllowlistedOperationOnly(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	recorder := New(zap.New(core))
	err := recorder.StreamServerInterceptor(nil, nil,
		&grpc.StreamServerInfo{FullMethod: "/identity.v1.IdentityService/WatchPendingLibrarians"},
		func(any, grpc.ServerStream) error { return status.Error(codes.Unavailable, "sensitive stream failure") },
	)
	require.Error(t, err)
	require.Len(t, logs.All(), 1)
	assert.Equal(t, "watch_pending_librarians", logs.All()[0].ContextMap()["operation"])
	assert.Equal(t, "Unavailable", logs.All()[0].ContextMap()["code"])
	assert.NotContains(t, logs.All()[0].Message, "sensitive")
}
