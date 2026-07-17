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

func monitorDatabaseHealth(ctx context.Context, pool *pgxpool.Pool, healthServer *health.Server) {
	check := func() {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status := grpc_health_v1.HealthCheckResponse_SERVING
		if pool.Ping(pingCtx) != nil {
			status = grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
		healthServer.SetServingStatus("", status)
	}
	check()
	ticker := time.NewTicker(2 * time.Second)
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

func deliverVerificationEmails(ctx context.Context, outbox port.EmailOutbox, opener port.EmailOpener, sender port.EmailSender, diagnostics *diagnostic.Recorder) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		deliverEmailBatch(ctx, outbox, opener, sender, diagnostics)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func deliverEmailBatch(ctx context.Context, outbox port.EmailOutbox, opener port.EmailOpener, sender port.EmailSender, diagnostics *diagnostic.Recorder) {
	now := time.Now().UTC()
	deliveries, err := outbox.Claim(ctx, now, time.Minute, 25)
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
		terminal := delivery.Attempts >= 10
		minutes := math.Pow(2, float64(delivery.Attempts-1))
		if minutes > 60 {
			minutes = 60
		}
		retryAt := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
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

func cleanupExpiredSessions(ctx context.Context, sessions expiredSessionCleaner, diagnostics *diagnostic.Recorder) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
	ticker := time.NewTicker(time.Hour)
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

func cleanupIdentityState(ctx context.Context, verifications verificationCleaner, rejected rejectedCleaner, diagnostics *diagnostic.Recorder) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := verifications.Cleanup(cleanupCtx); err != nil {
			diagnostics.WorkerFailed(diagnostic.StageVerificationCleanup)
		}
		if _, err := rejected.CleanupRejected(cleanupCtx); err != nil {
			diagnostics.WorkerFailed(diagnostic.StageRejectedCleanup)
		}
	}
	cleanup()
	ticker := time.NewTicker(time.Hour)
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
