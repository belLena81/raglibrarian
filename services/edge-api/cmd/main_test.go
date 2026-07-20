package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/services/edge-api/config"
	"github.com/belLena81/raglibrarian/services/edge-api/diagnostic"
	"github.com/belLena81/raglibrarian/services/edge-api/internal/app"
)

func TestConfigFailureReasonMapsClosedClasses(t *testing.T) {
	tests := []struct {
		err      error
		expected diagnostic.ServiceFailureReason
	}{
		{err: config.ErrRequiredConfiguration, expected: diagnostic.ServiceFailureConfigRequiredMissing},
		{err: config.ErrVerifyKeyConfiguration, expected: diagnostic.ServiceFailureConfigVerifyKeyInvalid},
		{err: config.ErrTrustedProxyConfiguration, expected: diagnostic.ServiceFailureConfigTrustedProxyInvalid},
		{err: config.ErrRefreshCookieConfiguration, expected: diagnostic.ServiceFailureConfigRefreshCookieInvalid},
		{err: config.ErrRunIdentityConfiguration, expected: diagnostic.ServiceFailureConfigRunIdentityInvalid},
		{err: errors.New("sensitive unknown cause"), expected: diagnostic.ServiceFailureUnknown},
	}

	for _, test := range tests {
		assert.Equal(t, test.expected, configFailureReason(test.err))
	}
}

func TestAppFailureReasonMapsClosedClasses(t *testing.T) {
	tests := []struct {
		err      error
		expected diagnostic.ServiceFailureReason
	}{
		{err: app.ErrTokenVerifierInitialization, expected: diagnostic.ServiceFailureTokenVerifierInitialization},
		{err: app.ErrInternalTLSFilesUnreadable, expected: diagnostic.ServiceFailureInternalTLSFilesUnreadable},
		{err: app.ErrInternalTLSMaterialInvalid, expected: diagnostic.ServiceFailureInternalTLSMaterialInvalid},
		{err: app.ErrPrivilegeDrop, expected: diagnostic.ServiceFailurePrivilegeDrop},
		{err: app.ErrIdentityClientInitialization, expected: diagnostic.ServiceFailureIdentityClientInitialization},
		{err: app.ErrRetrievalClientInitialization, expected: diagnostic.ServiceFailureRetrievalClientInitialization},
		{err: app.ErrHTTPListen, expected: diagnostic.ServiceFailureHTTPListen},
		{err: app.ErrHTTPServe, expected: diagnostic.ServiceFailureHTTPServe},
		{err: app.ErrHTTPShutdown, expected: diagnostic.ServiceFailureHTTPShutdown},
		{err: errors.New("sensitive unknown cause"), expected: diagnostic.ServiceFailureUnknown},
	}

	for _, test := range tests {
		assert.Equal(t, test.expected, appFailureReason(test.err))
	}
}
