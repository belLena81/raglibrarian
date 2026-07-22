package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/belLena81/raglibrarian/services/edge-api/authflow"
	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

func TestFixedWindowPrincipalRateLimitUsesTrustedPrincipal(t *testing.T) {
	limiter := qmiddleware.FixedWindowPrincipalRateLimit(1, time.Hour, 10)
	calls := 0
	next := limiter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	req := principalRequest("user-1", "reader")
	next.ServeHTTP(first, req)
	second := httptest.NewRecorder()
	next.ServeHTTP(second, req)
	third := httptest.NewRecorder()
	next.ServeHTTP(third, principalRequest("user-1", "admin"))

	assert.Equal(t, http.StatusNoContent, first.Code)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Equal(t, http.StatusNoContent, third.Code)
	assert.Equal(t, 2, calls)
	assert.Equal(t, "3600", second.Header().Get("Retry-After"))
}

func TestPrincipalRateLimiterAppliesIndependentTenPerMinuteLimit(t *testing.T) {
	limiter := qmiddleware.NewPrincipalRateLimiter(10, time.Minute, 100)

	for range 10 {
		allowed, retryAfter := limiter.Allow("user-1", "reader")
		assert.True(t, allowed)
		assert.Zero(t, retryAfter)
	}
	allowed, retryAfter := limiter.Allow("user-1", "reader")
	assert.False(t, allowed)
	assert.Greater(t, retryAfter, 0*time.Second)
	assert.LessOrEqual(t, retryAfter, time.Minute)

	otherRoleAllowed, _ := limiter.Allow("user-1", "admin")
	otherUserAllowed, _ := limiter.Allow("user-2", "reader")
	assert.True(t, otherRoleAllowed)
	assert.True(t, otherUserAllowed)
}

func TestBoundedConcurrencyRejectsWhenFull(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	limiter := qmiddleware.BoundedConcurrency(1)
	next := limiter(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		next.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/query", nil))
		close(done)
	}()
	<-entered

	second := httptest.NewRecorder()
	next.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/query", nil))
	close(release)

	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, http.StatusNoContent, first.Code)
	assert.Equal(t, "60", second.Header().Get("Retry-After"))
}

func principalRequest(userID, role string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/query", nil)
	return req.WithContext(qmiddleware.WithPrincipal(req.Context(), authflow.Principal{UserID: userID, Role: role, Status: "active"}))
}
