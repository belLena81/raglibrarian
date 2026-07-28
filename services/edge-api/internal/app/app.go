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
	answerv1 "github.com/belLena81/raglibrarian/pkg/proto/answer/v1"
	catalogv1 "github.com/belLena81/raglibrarian/pkg/proto/catalog/v1"
	identityv1 "github.com/belLena81/raglibrarian/pkg/proto/identity/v1"
	retrievalv1 "github.com/belLena81/raglibrarian/pkg/proto/retrieval/v1"
	edgeapi "github.com/belLena81/raglibrarian/services/edge-api"
	"github.com/belLena81/raglibrarian/services/edge-api/answerclient"
	"github.com/belLena81/raglibrarian/services/edge-api/bookstatus"
	"github.com/belLena81/raglibrarian/services/edge-api/catalogclient"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/handler"
	"github.com/belLena81/raglibrarian/services/edge-api/identityclient"
	"github.com/belLena81/raglibrarian/services/edge-api/middleware"
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
	// ErrAnswerClientInitialization identifies Answer gRPC client setup failure.
	ErrAnswerClientInitialization = errors.New("answer client initialization failed")
	// ErrHTTPListen identifies HTTP listener creation failure.
	ErrHTTPListen = errors.New("HTTP listen failed")
	// ErrHTTPServe identifies HTTP serving failure after listener creation.
	ErrHTTPServe = errors.New("HTTP serve failed")
	// ErrHTTPShutdown identifies graceful HTTP shutdown failure.
	ErrHTTPShutdown = errors.New("HTTP shutdown failed")
)

func httpWriteTimeout(answerDeadline, retrievalDeadline, headroom, minimum time.Duration) time.Duration {
	timeout := answerDeadline + retrievalDeadline + headroom
	if timeout < minimum {
		return minimum
	}
	return timeout
}

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
	answerCredentials, err := internaltls.ClientCredentials(cfg.TLS, "answer-service")
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
	answerConnection, err := grpc.NewClient(cfg.AnswerAddress, grpc.WithTransportCredentials(answerCredentials))
	if err != nil {
		return appFailure(ErrAnswerClientInitialization, err)
	}
	defer func() { _ = answerConnection.Close() }()
	identity := identityclient.New(identityv1.NewIdentityServiceClient(connection), grpc_health_v1.NewHealthClient(connection), identityclient.Policy{
		RPCDeadline: cfg.IdentityRPCDeadline,
	})
	catalog := catalogclient.New(catalogv1.NewCatalogServiceClient(catalogConnection), catalogclient.Policy{
		ReadinessTimeout: cfg.CatalogReadinessTimeout,
		UploadTimeout:    cfg.CatalogUploadTimeout,
		ListTimeout:      cfg.CatalogListTimeout,
		PreviewTimeout:   cfg.CatalogPreviewDeadline,
	})
	retrieval := retrievalclient.New(retrievalv1.NewRetrievalServiceClient(retrievalConnection), retrievalclient.Policy{
		ReadinessTimeout: cfg.RetrievalReadinessTimeout,
		SearchDeadline:   cfg.RetrievalSearchDeadline,
	})
	answer := answerclient.New(answerv1.NewAnswerServiceClient(answerConnection), cfg.AnswerDeadline, cfg.MinimumEvidenceScore)
	authHandler := handler.NewAuthHandler(identity, diagnostics, handler.CookieConfig{
		Secure:              cfg.SecureCookie,
		RefreshCookieMaxAge: cfg.RefreshCookieMaxAge,
	})
	answerAdmission := middleware.NewPrincipalRateLimiter(cfg.AnswerRateLimit, cfg.AnswerRateWindow, cfg.QueryRateMaxKeys)
	queryHandler := handler.NewQueryHandler(retrieval, cfg.MinimumEvidenceScore, handler.QueryPolicy{
		MaxQuestionLength: cfg.QueryMaxQuestionLength,
		MaxTags:           cfg.QueryMaxTags,
		MaxTagLength:      cfg.QueryMaxTagLength,
		MaxAuthorLength:   cfg.QueryMaxAuthorLength,
		DefaultLimit:      cfg.QueryDefaultLimit,
		MaxLimit:          cfg.QueryMaxLimit,
	}, handler.WithAnswer(answer, answerAdmission))
	healthHandler := handler.NewHealthHandler(readiness{
		identity:                   identity,
		catalog:                    catalog,
		retrieval:                  retrieval,
		retrievalReadinessRequired: cfg.RetrievalReadinessRequired,
	})
	booksHandler := handler.NewBooksHandler(catalog, handler.BooksPolicy{
		UploadTimeout:    cfg.BookUploadDeadline,
		ListTimeout:      cfg.BooksListTimeout,
		PreviewTimeout:   cfg.CatalogPreviewDeadline,
		LifecycleTimeout: cfg.BooksLifecycleTimeout,
	})
	bookStatusHub := handler.NewBookStatusHub(cfg.BookStatusHubCapacity)
	booksHandler.EnableEvents(handler.BookEventsConfig{
		Sessions: identity, Hub: bookStatusHub, PublicOrigin: cfg.PublicOrigin, EnforceOrigin: cfg.EnforceBrowserOrigin,
		Timing: handler.SSEPolicy{
			HeartbeatInterval:  cfg.SSEHeartbeatInterval,
			RevalidateInterval: cfg.SSERevalidateInterval,
			MaximumDuration:    cfg.SSEMaximumDuration,
			WriteTimeout:       cfg.SSEWriteTimeout,
		},
	})
	go bookstatus.Run(ctx, cfg.StatusRabbitURI, cfg.StatusQueue, bookStatusHub, bookstatus.Policy{
		ReconnectInitialBackoff: cfg.BookStatusReconnectInitialBackoff,
		ReconnectMaxBackoff:     cfg.BookStatusReconnectMaxBackoff,
		DialTimeout:             cfg.BookStatusDialTimeout,
		HeartbeatTimeout:        cfg.BookStatusHeartbeatTimeout,
		Prefetch:                cfg.BookStatusPrefetch,
		QueueMaxLengthBytes:     int64(cfg.BookStatusQueueMaxLengthBytes),
	})
	setupHandler := handler.NewSetupHandler(identity)
	hub := handler.NewPendingHub(cfg.PendingHubCapacity)
	adminHandler := handler.NewAdminHandler(identity, hub)
	adminHandler.SetSSETiming(handler.SSEPolicy{
		HeartbeatInterval:  cfg.SSEHeartbeatInterval,
		RevalidateInterval: cfg.SSERevalidateInterval,
		MaximumDuration:    cfg.SSEMaximumDuration,
		WriteTimeout:       cfg.SSEWriteTimeout,
	})
	go watchPendingChanges(ctx, identity, hub, cfg.PendingWatchReconnectInitialBackoff, cfg.PendingWatchReconnectMaxBackoff)
	server := &http.Server{
		Addr: cfg.Addr,
		Handler: edgeapi.NewRouter(queryHandler, authHandler, healthHandler, setupHandler, adminHandler, verifier, identity, diagnostics, edgeapi.RouterConfig{
			TrustedProxyCIDRs: cfg.TrustedProxyCIDRs, PublicOrigin: cfg.PublicOrigin, EnforceBrowserOrigin: cfg.EnforceBrowserOrigin,
			QueryRateLimit: cfg.QueryRateLimit, QueryRateWindow: cfg.QueryRateWindow, QueryRateMaxKeys: cfg.QueryRateMaxKeys,
			QueryConcurrency: cfg.QueryConcurrency, QueryConcurrencyRetryAfter: cfg.QueryConcurrencyRetryAfter,
			AuthRegisterRateLimit: cfg.AuthRegisterRateLimit, AuthRegisterRateWindow: cfg.AuthRegisterRateWindow, AuthRegisterRateMaxKeys: cfg.AuthRegisterRateMaxKeys,
			AuthVerifyEmailRateLimit: cfg.AuthVerifyEmailRateLimit, AuthVerifyEmailRateWindow: cfg.AuthVerifyEmailRateWindow, AuthVerifyEmailRateMaxKeys: cfg.AuthVerifyEmailRateMaxKeys,
			AuthLoginRateLimit: cfg.AuthLoginRateLimit, AuthLoginRateWindow: cfg.AuthLoginRateWindow, AuthLoginRateMaxKeys: cfg.AuthLoginRateMaxKeys,
			AuthResendVerificationRateLimit: cfg.AuthResendVerificationRateLimit, AuthResendVerificationRateWindow: cfg.AuthResendVerificationRateWindow, AuthResendVerificationRateMaxKeys: cfg.AuthResendVerificationRateMaxKeys,
			AuthPasswordResetRequestRateLimit: cfg.AuthPasswordResetRequestRateLimit, AuthPasswordResetRequestRateWindow: cfg.AuthPasswordResetRequestRateWindow, AuthPasswordResetRequestRateMaxKeys: cfg.AuthPasswordResetRequestRateMaxKeys,
			AuthPasswordResetVerifyRateLimit: cfg.AuthPasswordResetVerifyRateLimit, AuthPasswordResetVerifyRateWindow: cfg.AuthPasswordResetVerifyRateWindow, AuthPasswordResetVerifyRateMaxKeys: cfg.AuthPasswordResetVerifyRateMaxKeys,
			AuthPasswordResetCompleteRateLimit: cfg.AuthPasswordResetCompleteRateLimit, AuthPasswordResetCompleteRateWindow: cfg.AuthPasswordResetCompleteRateWindow, AuthPasswordResetCompleteRateMaxKeys: cfg.AuthPasswordResetCompleteRateMaxKeys,
			SetupAdminRateLimit: cfg.SetupAdminRateLimit, SetupAdminRateWindow: cfg.SetupAdminRateWindow, SetupAdminRateMaxKeys: cfg.SetupAdminRateMaxKeys,
			BookUploadRateLimit: cfg.BookUploadRateLimit, BookUploadRateWindow: cfg.BookUploadRateWindow,
			BookUploadRateMaxKeys: cfg.BookUploadRateMaxKeys, BookUploadDeadline: cfg.BookUploadDeadline,
		}, booksHandler),
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      httpWriteTimeout(cfg.AnswerDeadline, cfg.RetrievalSearchDeadline, cfg.HTTPWriteTimeoutHeadroom, cfg.HTTPMinimumWriteTimeout),
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.HTTPMaxHeaderBytes,
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTPShutdownTimeout)
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

type readinessDependencyError struct {
	dependency string
	err        error
}

func (err readinessDependencyError) Error() string {
	return fmt.Sprintf("readiness dependency %s unavailable", err.dependency)
}

func (err readinessDependencyError) DependencyName() string {
	return err.dependency
}

func (err readinessDependencyError) Unwrap() error {
	return err.err
}

func (r readiness) CheckReady(ctx context.Context) error {
	if err := r.identity.CheckReady(ctx); err != nil {
		return readinessDependencyError{dependency: "identity", err: err}
	}
	if err := r.catalog.CheckReady(ctx); err != nil {
		return readinessDependencyError{dependency: "catalog", err: err}
	}
	if !r.retrievalReadinessRequired {
		return nil
	}
	if err := r.retrieval.CheckReady(ctx); err != nil {
		return readinessDependencyError{dependency: "retrieval", err: err}
	}
	return nil
}

type pendingWatcher interface {
	WatchPending(context.Context, chan<- struct{}) error
}

type pendingPublisher interface{ Publish() }

func watchPendingChanges(ctx context.Context, watcher pendingWatcher, publisher pendingPublisher, initialBackoff, maximumBackoff time.Duration) {
	backoff := initialBackoff
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
		if backoff < maximumBackoff {
			backoff *= 2
			if backoff > maximumBackoff {
				backoff = maximumBackoff
			}
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
