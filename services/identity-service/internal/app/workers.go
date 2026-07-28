package app

import (
	"context"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/services/identity-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase/port"
)

type EmailDeliveryPolicy struct {
	Interval          time.Duration
	ClaimTTL          time.Duration
	ClaimBatchSize    int
	RetryBaseInterval time.Duration
	RetryMaxInterval  time.Duration
	RetryMaxAttempts  int
}

func monitorDatabaseHealth(ctx context.Context, pool *pgxpool.Pool, healthServer *health.Server, probeTimeout, pollInterval time.Duration) {
	check := func() {
		pingCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		defer cancel()
		status := grpc_health_v1.HealthCheckResponse_SERVING
		if pool.Ping(pingCtx) != nil {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		healthServer.SetServingStatus("", status)
	}
	check()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check()
		}
	}
}

func deliverVerificationEmails(ctx context.Context, outbox port.EmailOutbox, opener port.EmailOpener, sender port.EmailSender, diagnostics *diagnostic.Recorder, policy EmailDeliveryPolicy) {
	ticker := time.NewTicker(policy.Interval)
	defer ticker.Stop()
	for {
		deliverEmailBatch(ctx, outbox, opener, sender, diagnostics, policy)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func deliverEmailBatch(ctx context.Context, outbox port.EmailOutbox, opener port.EmailOpener, sender port.EmailSender, diagnostics *diagnostic.Recorder, policy EmailDeliveryPolicy) {
	now := time.Now().UTC()
	deliveries, err := outbox.Claim(ctx, now, policy.ClaimTTL, policy.ClaimBatchSize)
	if err != nil {
		diagnostics.WorkerFailed(diagnostic.StageEmailClaim)
		return
	}
	for _, delivery := range deliveries {
		email, token, openErr := opener.OpenVerification(delivery)
		if openErr == nil {
			if delivery.MessageType == "password_reset_code" {
				openErr = sender.SendPasswordReset(ctx, email, token)
			} else {
				openErr = sender.SendVerification(ctx, email, token)
			}
		}
		if openErr == nil {
			if markErr := outbox.Delivered(ctx, delivery.ID, time.Now().UTC()); markErr != nil {
				diagnostics.WorkerFailed(diagnostic.StageEmailMark)
			}
			continue
		}
		terminal := delivery.Attempts >= policy.RetryMaxAttempts
		minutes := math.Pow(2, float64(delivery.Attempts-1))
		retryDelay := time.Duration(minutes * float64(policy.RetryBaseInterval))
		if retryDelay > policy.RetryMaxInterval {
			retryDelay = policy.RetryMaxInterval
		}
		retryAt := time.Now().UTC().Add(retryDelay)
		if markErr := outbox.Failed(ctx, delivery.ID, retryAt, terminal); markErr != nil {
			diagnostics.WorkerFailed(diagnostic.StageEmailRetry)
		}
		if terminal {
			diagnostics.WorkerFailed(diagnostic.StageEmailExhausted)
		}
	}
}

type expiredSessionCleaner interface {
	CleanupExpired(context.Context, time.Time) (int64, error)
}

func cleanupExpiredSessions(ctx context.Context, sessions expiredSessionCleaner, diagnostics *diagnostic.Recorder, timeout, interval time.Duration) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		deleted, err := sessions.CleanupExpired(cleanupCtx, time.Now().UTC())
		if err != nil {
			diagnostics.WorkerFailed(diagnostic.StageSessionCleanup)
			return
		}
		if deleted > 0 {
			diagnostics.WorkerCompleted(diagnostic.StageSessionCleanup)
		}
	}
	cleanup()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

type verificationCleaner interface {
	Cleanup(context.Context) (int64, error)
}

type rejectedCleaner interface {
	CleanupRejected(context.Context) (int64, error)
}

type passwordResetCleaner interface {
	Cleanup(context.Context) (int64, error)
}

func cleanupIdentityState(ctx context.Context, verifications verificationCleaner, rejected rejectedCleaner, passwordResets passwordResetCleaner, diagnostics *diagnostic.Recorder, timeout, interval time.Duration) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cleanupIdentityStateOnce(cleanupCtx, verifications, rejected, passwordResets, diagnostics)
	}
	cleanup()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup()
		}
	}
}

func cleanupIdentityStateOnce(ctx context.Context, verifications verificationCleaner, rejected rejectedCleaner, passwordResets passwordResetCleaner, diagnostics *diagnostic.Recorder) {
	if _, err := verifications.Cleanup(ctx); err != nil {
		diagnostics.WorkerFailed(diagnostic.StageVerificationCleanup)
	}
	if _, err := rejected.CleanupRejected(ctx); err != nil {
		diagnostics.WorkerFailed(diagnostic.StageRejectedCleanup)
	}
	if _, err := passwordResets.Cleanup(ctx); err != nil {
		diagnostics.WorkerFailed(diagnostic.StagePasswordResetCleanup)
	}
}
