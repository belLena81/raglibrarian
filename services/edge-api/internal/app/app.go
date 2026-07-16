package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/belLena81/raglibrarian/pkg/auth"
	"github.com/belLena81/raglibrarian/pkg/internaltls"
	"github.com/belLena81/raglibrarian/pkg/process"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
)

var (
	// ErrTokenVerifierInitialization identifies access-token verifier setup failure.
	ErrTokenVerifierInitialization = errors.New("token verifier initialization failed")
	// ErrInternalTLSFilesUnreadable identifies inaccessible internal TLS files.
	ErrInternalTLSFilesUnreadable = errors.New("internal TLS files unreadable")
	// ErrInternalTLSMaterialInvalid identifies malformed internal TLS material.
	ErrInternalTLSMaterialInvalid = errors.New("internal TLS material invalid")
	// ErrPrivilegeDrop identifies process privilege reduction failure.
	ErrPrivilegeDrop = errors.New("privilege drop failed")
	// ErrIdentityClientInitialization identifies Identity gRPC client setup failure.
	ErrIdentityClientInitialization = errors.New("identity client initialization failed")
	// ErrHTTPListen identifies HTTP listener creation failure.
	ErrHTTPListen = errors.New("HTTP listen failed")
	// ErrHTTPServe identifies HTTP serving failure after listener creation.
	ErrHTTPServe = errors.New("HTTP serve failed")
	// ErrHTTPShutdown identifies graceful HTTP shutdown failure.
	ErrHTTPShutdown = errors.New("HTTP shutdown failed")
)

// Run composes and manages the Edge process lifecycle.
func Run(ctx context.Context, cfg config.Config, diagnostics *diagnostic.Recorder) error {
	if diagnostics == nil {
		panic("app: diagnostics are required")
	}
	verifier, err := auth.NewVerifier(cfg.VerifyKey)
	if err != nil {
		return appFailure(ErrTokenVerifierInitialization, err)
	}
	credentials, err := internaltls.ClientCredentials(cfg.TLS, "identity-service")
	if err != nil {
		return tlsFailure(err)
	}
	if err = process.DropPrivileges(cfg.RunAs); err != nil {
		return appFailure(ErrPrivilegeDrop, err)
	}
	connection, err := grpc.NewClient(cfg.IdentityAddress, grpc.WithTransportCredentials(credentials))
	if err != nil {
		return appFailure(ErrIdentityClientInitialization, err)
	}
	defer func() { _ = connection.Close() }()
	identity := identityclient.New(identityv1.NewIdentityServiceClient(connection), grpc_health_v1.NewHealthClient(connection))
	authHandler := handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{Secure: cfg.SecureCookie})
	queryHandler := handler.NewQueryHandler(diagnostics)
	healthHandler := handler.NewHealthHandler(identity)
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           edgeapi.NewRouter(queryHandler, authHandler, healthHandler, verifier, identity, diagnostics, edgeapi.RouterConfig{TrustedProxyCIDRs: cfg.TrustedProxyCIDRs}),
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
		return httpServerFailure(err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdownCtx); err != nil {
			return appFailure(ErrHTTPShutdown, err)
		}
		return nil
	}
}

func appFailure(class, cause error) error {
	return fmt.Errorf("%w: %w", class, cause)
}

func tlsFailure(cause error) error {
	var pathError *os.PathError
	if errors.As(cause, &pathError) {
		return appFailure(ErrInternalTLSFilesUnreadable, cause)
	}
	return appFailure(ErrInternalTLSMaterialInvalid, cause)
}

func httpServerFailure(cause error) error {
	var operationError *net.OpError
	if errors.As(cause, &operationError) && operationError.Op == "listen" {
		return appFailure(ErrHTTPListen, cause)
	}
	return appFailure(ErrHTTPServe, cause)
}
