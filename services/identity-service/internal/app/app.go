package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/grpcauth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	"github.com/belLena81/raglibrarian/services/identity-service/config"
	"github.com/belLena81/raglibrarian/services/identity-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/identity-service/email"
	identitygrpc "github.com/belLena81/raglibrarian/services/identity-service/grpc"
	"github.com/belLena81/raglibrarian/services/identity-service/password"
	"github.com/belLena81/raglibrarian/services/identity-service/repository"
	"github.com/belLena81/raglibrarian/services/identity-service/securevalue"
	identitytoken "github.com/belLena81/raglibrarian/services/identity-service/token"
	"github.com/belLena81/raglibrarian/services/identity-service/usecase"
)

type systemClock struct{}

// Now implements the application clock with UTC-normalized caller usage.
func (systemClock) Now() time.Time { return time.Now() }

type systemIDs struct{}

// NewID returns a new opaque application identifier.
func (systemIDs) NewID() string { return uuid.NewString() }

// Run composes and manages the Identity process lifecycle.
func Run(ctx context.Context, cfg config.Config, diagnostics *diagnostic.Recorder) error {
	if diagnostics == nil {
		panic("app: diagnostics are required")
	}
	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err != nil {
		return fmt.Errorf("database connection: %w", err)
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = pool.Ping(pingCtx); err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	signer, err := auth.NewSignerWithKeyID(cfg.SigningKey, 15*time.Minute, cfg.SigningKeyID)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = listener.Close() }()
	credentials, err := internaltls.ServerCredentials(cfg.TLS)
	if err != nil {
		return fmt.Errorf("TLS: %w", err)
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	policy := grpcauth.Policy{Service: "identity.v1.IdentityService", DNSName: "edge-api"}
	server := grpc.NewServer(
		grpc.Creds(credentials),
		grpc.ChainUnaryInterceptor(
			grpcauth.UnaryServerInterceptor(policy),
			diagnostics.UnaryServerInterceptor,
		),
		grpc.ChainStreamInterceptor(
			grpcauth.StreamServerInterceptor(policy),
			diagnostics.StreamServerInterceptor,
		),
	)
	sessions := repository.NewPostgresSessionRepository(pool)
	users := repository.NewPostgresUserRepository(pool)
	identityStore := repository.NewPostgresIdentityRepository(pool)
	passwords := password.NewLimitedHasher(password.BcryptHasher{}, cfg.BcryptConcurrency)
	issuer := identitytoken.NewIssuer(signer)
	sessionService := usecase.NewSessionService(users, sessions, issuer, passwords, systemClock{}, 30*24*time.Hour)
	protector, err := securevalue.New(cfg.FingerprintKey, cfg.OutboxKey, cfg.OutboxKeyID)
	if err != nil {
		return fmt.Errorf("secure values unavailable: %w", err)
	}
	sender, err := email.NewSMTPSender(cfg.SMTP)
	if err != nil {
		return fmt.Errorf("email adapter unavailable: %w", err)
	}
	verificationService := usecase.NewVerificationService(identityStore, passwords, protector, protector, systemIDs{}, systemClock{})
	passwordResetService := usecase.NewPasswordResetService(identityStore, passwords, protector, protector, systemIDs{}, systemClock{}, cfg.PasswordResetKey)
	bootstrapService := usecase.NewBootstrapService(identityStore, passwords, protector, systemIDs{}, systemClock{}, cfg.BootstrapVerifier)
	approvalService := usecase.NewApprovalService(identityStore, systemClock{})
	notifications := repository.NewPostgresNotifications(pool)
	identityv1.RegisterIdentityServiceServer(server, identitygrpc.NewServer(verificationService, sessionService, passwordResetService, bootstrapService, approvalService, notifications))
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	backgroundCtx, stopBackground := context.WithCancel(ctx)
	defer stopBackground()
	go monitorDatabaseHealth(backgroundCtx, pool, healthServer)
	go cleanupExpiredSessions(backgroundCtx, sessions, diagnostics)
	go cleanupIdentityState(backgroundCtx, verificationService, approvalService, passwordResetService, diagnostics)
	go deliverVerificationEmails(backgroundCtx, identityStore, protector, sender, diagnostics)
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	select {
	case err = <-errCh:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
		stopBackground()
		healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
		gracefulStop(server, 10*time.Second)
		return nil
	}
}
