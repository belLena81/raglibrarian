package identitygrpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

func TestToStatusPreservesSanitizedContract(t *testing.T) {
	assert.Equal(t, codes.InvalidArgument, status.Code(toStatus(domain.ErrInvalidPassword)))
	assert.Equal(t, codes.Unauthenticated, status.Code(toStatus(domain.ErrInvalidCredentials)))
}

func TestAuthenticatedOperationAddsOperationTimeout(t *testing.T) {
	started := time.Now()
	ctx, cancel, err := authenticatedOperation(context.Background())
	finished := time.Now()
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.False(t, deadline.Before(started.Add(operationTimeout)))
	assert.False(t, deadline.After(finished.Add(operationTimeout)))
}

func TestAuthenticatedOperationCapsLongerCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 2*operationTimeout)
	defer parentCancel()
	started := time.Now()

	ctx, cancel, err := authenticatedOperation(parent)
	finished := time.Now()
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.False(t, deadline.Before(started.Add(operationTimeout)))
	assert.False(t, deadline.After(finished.Add(operationTimeout)))
}

func TestAuthenticatedOperationPreservesShorterCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), operationTimeout/2)
	defer parentCancel()
	parentDeadline, ok := parent.Deadline()
	require.True(t, ok)

	ctx, cancel, err := authenticatedOperation(parent)
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Equal(t, parentDeadline, deadline)
}
