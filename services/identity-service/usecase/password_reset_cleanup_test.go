package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/services/identity-service/domain"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

type passwordResetCleanupStore struct {
	cleanupAt time.Time
}

func (*passwordResetCleanupStore) RequestPasswordReset(context.Context, []byte, []byte, time.Time, port.SealedEmail) (bool, error) {
	return false, nil
}

func (*passwordResetCleanupStore) VerifyPasswordReset(context.Context, []byte, []byte, []byte, time.Time) ([]domain.Role, error) {
	return nil, nil
}

func (*passwordResetCleanupStore) CompletePasswordReset(context.Context, []byte, domain.Role, string, time.Time) error {
	return nil
}

func (s *passwordResetCleanupStore) CleanupPasswordResetChallenges(_ context.Context, now time.Time) (int64, error) {
	s.cleanupAt = now
	return 3, nil
}

func TestPasswordResetServiceCleanupUsesCurrentUTCClock(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 34, 56, 0, time.FixedZone("test", 2*60*60))
	store := &passwordResetCleanupStore{}
	service := &PasswordResetService{store: store, clock: fixedClock{now: now}}

	cleaned, err := service.Cleanup(context.Background())

	require.NoError(t, err)
	require.Equal(t, int64(3), cleaned)
	require.Equal(t, now.UTC(), store.cleanupAt)
}
