package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
)

// Run composes and manages the Edge process lifecycle.
func Run(ctx context.Context, cfg config.Config, log *zap.Logger) error {
	verifier, err := auth.NewVerifier(cfg.VerifyKey)
	if err != nil {
		return err
	}
	credentials, err := internaltls.ClientCredentials(cfg.TLS, "identity-service")
	if err != nil {
		return err
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return err
	}
	connection, err := grpc.NewClient(cfg.IdentityAddress, grpc.WithTransportCredentials(credentials))
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	identity := identityclient.New(identityv1.NewIdentityServiceClient(connection), grpc_health_v1.NewHealthClient(connection))
	authHandler := handler.NewAuthHandler(identity, log, handler.CookieConfig{Secure: cfg.SecureCookie})
	queryHandler := handler.NewQueryHandler(log)
	healthHandler := handler.NewHealthHandler(identity)
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           edgeapi.NewRouter(queryHandler, authHandler, healthHandler, verifier, identity, log, edgeapi.RouterConfig{TrustedProxyCIDRs: cfg.TrustedProxyCIDRs}),
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err = <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
