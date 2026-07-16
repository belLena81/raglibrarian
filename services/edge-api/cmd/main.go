package main

import (
	"context"
	"errors"
	"os/signal"
	"syscall"

	"github.com/belLena81/raglibrarian/pkg/logger"
	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/app"
)

func main() {
	log := logger.Must("edge-api")
	defer func() { _ = log.Sync() }()
	diagnostics := diagnostic.New(log)
	cfg, err := config.Load()
	if err != nil {
		diagnostics.ServiceStartFailed(configFailureReason(err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err = app.Run(ctx, cfg, diagnostics); err != nil {
		diagnostics.ServiceRunFailed(appFailureReason(err))
	}
}

func configFailureReason(err error) diagnostic.ServiceFailureReason {
	switch {
	case errors.Is(err, config.ErrRequiredConfiguration):
		return diagnostic.ServiceFailureConfigRequiredMissing
	case errors.Is(err, config.ErrVerifyKeyConfiguration):
		return diagnostic.ServiceFailureConfigVerifyKeyInvalid
	case errors.Is(err, config.ErrTrustedProxyConfiguration):
		return diagnostic.ServiceFailureConfigTrustedProxyInvalid
	case errors.Is(err, config.ErrRefreshCookieConfiguration):
		return diagnostic.ServiceFailureConfigRefreshCookieInvalid
	case errors.Is(err, config.ErrRunIdentityConfiguration):
		return diagnostic.ServiceFailureConfigRunIdentityInvalid
	default:
		return diagnostic.ServiceFailureUnknown
	}
}

func appFailureReason(err error) diagnostic.ServiceFailureReason {
	switch {
	case errors.Is(err, app.ErrTokenVerifierInitialization):
		return diagnostic.ServiceFailureTokenVerifierInitialization
	case errors.Is(err, app.ErrInternalTLSFilesUnreadable):
		return diagnostic.ServiceFailureInternalTLSFilesUnreadable
	case errors.Is(err, app.ErrInternalTLSMaterialInvalid):
		return diagnostic.ServiceFailureInternalTLSMaterialInvalid
	case errors.Is(err, app.ErrPrivilegeDrop):
		return diagnostic.ServiceFailurePrivilegeDrop
	case errors.Is(err, app.ErrIdentityClientInitialization):
		return diagnostic.ServiceFailureIdentityClientInitialization
	case errors.Is(err, app.ErrHTTPListen):
		return diagnostic.ServiceFailureHTTPListen
	case errors.Is(err, app.ErrHTTPServe):
		return diagnostic.ServiceFailureHTTPServe
	case errors.Is(err, app.ErrHTTPShutdown):
		return diagnostic.ServiceFailureHTTPShutdown
	default:
		return diagnostic.ServiceFailureUnknown
	}
}
