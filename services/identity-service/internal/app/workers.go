package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/services/identity-service/repository"
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

func cleanupExpiredSessions(ctx context.Context, sessions repository.SessionRepository, log *zap.Logger) {
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		deleted, err := sessions.CleanupExpired(cleanupCtx, time.Now().UTC())
		if err != nil {
			log.Warn("expired session cleanup failed", zap.Error(err))
			return
		}
		if deleted > 0 {
			log.Info("expired sessions removed", zap.Int64("count", deleted))
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
