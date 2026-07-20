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
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/bookstatus"
	"github.com/belLena81/raglibrarian/services/edge-api/catalogclient"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
	"github.com/belLena81/raglibrarian/services/edge-api/retrievalclient"
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
	// ErrRetrievalClientInitialization identifies Retrieval gRPC client setup failure.
	ErrRetrievalClientInitialization = errors.New("retrieval client initialization failed")
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
	verifier, err := auth.NewKeyring(cfg.VerifyKey, cfg.PreviousVerifyKey)
	if err != nil {
		return appFailure(ErrTokenVerifierInitialization, err)
	}
	credentials, err := internaltls.ClientCredentials(cfg.TLS, "identity-service")
	if err != nil {
		return tlsFailure(err)
	}
	catalogCredentials, err := internaltls.ClientCredentials(cfg.TLS, "catalog-service")
	if err != nil {
		return tlsFailure(err)
	}
	retrievalCredentials, err := internaltls.ClientCredentials(cfg.TLS, "retrieval-service")
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
	catalogConnection, err := grpc.NewClient(cfg.CatalogAddress, grpc.WithTransportCredentials(catalogCredentials))
	if err != nil {
		return appFailure(ErrIdentityClientInitialization, err)
	}
	defer func() { _ = catalogConnection.Close() }()
	retrievalConnection, err := grpc.NewClient(cfg.RetrievalAddress, grpc.WithTransportCredentials(retrievalCredentials))
	if err != nil {
		return appFailure(ErrRetrievalClientInitialization, err)
	}
	defer func() { _ = retrievalConnection.Close() }()
	identity := identityclient.New(identityv1.NewIdentityServiceClient(connection), grpc_health_v1.NewHealthClient(connection))
	catalog := catalogclient.New(catalogv1.NewCatalogServiceClient(catalogConnection))
	retrieval := retrievalclient.New(retrievalv1.NewRetrievalServiceClient(retrievalConnection))
	authHandler := handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{Secure: cfg.SecureCookie})
	queryHandler := handler.NewQueryHandler(retrieval)
	healthHandler := handler.NewHealthHandler(readiness{
		identity:                   identity,
		catalog:                    catalog,
		retrieval:                  retrieval,
		retrievalReadinessRequired: cfg.RetrievalReadinessRequired,
	})
	booksHandler := handler.NewBooksHandler(catalog)
	bookStatusHub := handler.NewBookStatusHub(200)
	booksHandler.EnableEvents(handler.BookEventsConfig{
		Sessions: identity, Hub: bookStatusHub, PublicOrigin: cfg.PublicOrigin, EnforceOrigin: cfg.EnforceBrowserOrigin,
	})
	go bookstatus.Run(ctx, cfg.StatusRabbitURI, cfg.StatusQueue, bookStatusHub)
	setupHandler := handler.NewSetupHandler(identity)
	hub := handler.NewPendingHub(200)
	adminHandler := handler.NewAdminHandler(identity, hub)
	go watchPendingChanges(ctx, identity, hub)
	server := &http.Server{
		Addr: cfg.Addr,
		Handler: edgeapi.NewRouter(queryHandler, authHandler, healthHandler, setupHandler, adminHandler, verifier, identity, diagnostics, edgeapi.RouterConfig{
			TrustedProxyCIDRs: cfg.TrustedProxyCIDRs, PublicOrigin: cfg.PublicOrigin, EnforceBrowserOrigin: cfg.EnforceBrowserOrigin,
		}, booksHandler),
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

type readiness struct {
	identity                   interface{ CheckReady(context.Context) error }
	catalog                    interface{ CheckReady(context.Context) error }
	retrieval                  interface{ CheckReady(context.Context) error }
	retrievalReadinessRequired bool
}

func (r readiness) CheckReady(ctx context.Context) error {
	if err := r.identity.CheckReady(ctx); err != nil {
		return err
	}
	if err := r.catalog.CheckReady(ctx); err != nil {
		return err
	}
	if !r.retrievalReadinessRequired {
		return nil
	}
	return r.retrieval.CheckReady(ctx)
}

type pendingWatcher interface {
	WatchPending(context.Context, chan<- struct{}) error
}

type pendingPublisher interface{ Publish() }

func watchPendingChanges(ctx context.Context, watcher pendingWatcher, publisher pendingPublisher) {
	backoff := time.Second
	for ctx.Err() == nil {
		changes := make(chan struct{}, 1)
		done := make(chan error, 1)
		go func() { done <- watcher.WatchPending(ctx, changes) }()
		watching := true
		for watching {
			select {
			case <-ctx.Done():
				return
			case <-changes:
				publisher.Publish()
			case <-done:
				watching = false
			}
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
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
