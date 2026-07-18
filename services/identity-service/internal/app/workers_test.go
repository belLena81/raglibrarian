package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/belLena81/raglibrarian/services/identity-service/diagnostic"
)

type identityStateVerificationCleaner struct {
	called bool
	err    error
}

func (c *identityStateVerificationCleaner) Cleanup(context.Context) (int64, error) {
	c.called = true
	return 0, c.err
}

type identityStateRejectedCleaner struct {
	called bool
	err    error
}

func (c *identityStateRejectedCleaner) CleanupRejected(context.Context) (int64, error) {
	c.called = true
	return 0, c.err
}

type identityStatePasswordResetCleaner struct {
	called bool
	err    error
}

func (c *identityStatePasswordResetCleaner) Cleanup(context.Context) (int64, error) {
	c.called = true
	return 0, c.err
}

func TestCleanupIdentityStateOnceInvokesAllCleanersAfterFailure(t *testing.T) {
	verification := &identityStateVerificationCleaner{err: errors.New("database failure")}
	rejected := &identityStateRejectedCleaner{}
	passwordResets := &identityStatePasswordResetCleaner{}
	core, logs := observer.New(zapcore.WarnLevel)

	cleanupIdentityStateOnce(context.Background(), verification, rejected, passwordResets, diagnostic.New(zap.New(core)))

	assert.True(t, verification.called)
	assert.True(t, rejected.called)
	assert.True(t, passwordResets.called)
	require.Len(t, logs.All(), 1)
	assert.Equal(t, "worker.operation.failed", logs.All()[0].Message)
	assert.Equal(t, "verification_cleanup", logs.All()[0].ContextMap()["stage"])
	assert.NotContains(t, logs.All()[0].Message, "database failure")
}

func TestCleanupIdentityStateOnceRecordsPasswordResetFailure(t *testing.T) {
	verification := &identityStateVerificationCleaner{}
	rejected := &identityStateRejectedCleaner{}
	passwordResets := &identityStatePasswordResetCleaner{err: errors.New("sensitive failure")}
	core, logs := observer.New(zapcore.WarnLevel)

	cleanupIdentityStateOnce(context.Background(), verification, rejected, passwordResets, diagnostic.New(zap.New(core)))

	require.Len(t, logs.All(), 1)
	assert.Equal(t, "password_reset_cleanup", logs.All()[0].ContextMap()["stage"])
	assert.NotContains(t, logs.All()[0].Message, "sensitive failure")
}
