package identitygrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/domain"
)

const testOperationTimeout = 5 * time.Second

type resetUseCaseStub struct {
	verifyErr   error
	completeErr error
}

func (resetUseCaseStub) Request(context.Context, string) error { return nil }
func (s resetUseCaseStub) Verify(context.Context, string, string) (string, []domain.Role, error) {
	return "grant", []domain.Role{domain.RoleReader}, s.verifyErr
}
func (s resetUseCaseStub) Complete(context.Context, string, string, string) error {
	return s.completeErr
}

func TestToStatusPreservesSanitizedContract(t *testing.T) {
	assert.Equal(t, codes.InvalidArgument, status.Code(toStatus(domain.ErrInvalidPassword)))
	assert.Equal(t, codes.InvalidArgument, status.Code(toStatus(domain.ErrInvalidPasswordReset)))
	assert.Equal(t, codes.Unauthenticated, status.Code(toStatus(domain.ErrInvalidCredentials)))
	assert.Equal(t, codes.Canceled, status.Code(toStatus(context.Canceled)))
	assert.Equal(t, codes.DeadlineExceeded, status.Code(toStatus(context.DeadlineExceeded)))
	assert.Equal(t, codes.Internal, status.Code(toStatus(errors.New("database unavailable"))))
}

func TestPasswordResetRPCPreservesDependencyFailures(t *testing.T) {
	server := &Server{passwordReset: resetUseCaseStub{
		verifyErr:   errors.New("database unavailable"),
		completeErr: context.DeadlineExceeded,
	}, policy: Policy{OperationTimeout: testOperationTimeout}}

	_, err := server.VerifyPasswordReset(context.Background(), &identityv1.VerifyPasswordResetRequest{Email: "reader@example.test", Code: "123456"})
	assert.Equal(t, codes.Internal, status.Code(err))
	_, err = server.CompletePasswordReset(context.Background(), &identityv1.CompletePasswordResetRequest{ResetGrant: "grant", Role: "reader", Password: "password-1234"})
	assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

func TestPasswordResetRPCMapsOnlyInvalidResetToInvalidArgument(t *testing.T) {
	server := &Server{passwordReset: resetUseCaseStub{
		verifyErr:   domain.ErrInvalidPasswordReset,
		completeErr: domain.ErrInvalidPasswordReset,
	}, policy: Policy{OperationTimeout: testOperationTimeout}}

	_, err := server.VerifyPasswordReset(context.Background(), &identityv1.VerifyPasswordResetRequest{Email: "reader@example.test", Code: "123456"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.CompletePasswordReset(context.Background(), &identityv1.CompletePasswordResetRequest{ResetGrant: "grant", Role: "reader", Password: "password-1234"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestAuthenticatedOperationAddsOperationTimeout(t *testing.T) {
	started := time.Now()
	server := &Server{policy: Policy{OperationTimeout: testOperationTimeout}}
	ctx, cancel, err := server.authenticatedOperation(context.Background())
	finished := time.Now()
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.False(t, deadline.Before(started.Add(testOperationTimeout)))
	assert.False(t, deadline.After(finished.Add(testOperationTimeout)))
}

func TestAuthenticatedOperationCapsLongerCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 2*testOperationTimeout)
	defer parentCancel()
	started := time.Now()

	server := &Server{policy: Policy{OperationTimeout: testOperationTimeout}}
	ctx, cancel, err := server.authenticatedOperation(parent)
	finished := time.Now()
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.False(t, deadline.Before(started.Add(testOperationTimeout)))
	assert.False(t, deadline.After(finished.Add(testOperationTimeout)))
}

func TestAuthenticatedOperationPreservesShorterCallerDeadline(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), testOperationTimeout/2)
	defer parentCancel()
	parentDeadline, ok := parent.Deadline()
	require.True(t, ok)

	server := &Server{policy: Policy{OperationTimeout: testOperationTimeout}}
	ctx, cancel, err := server.authenticatedOperation(parent)
	defer cancel()

	require.NoError(t, err)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.Equal(t, parentDeadline, deadline)
}
