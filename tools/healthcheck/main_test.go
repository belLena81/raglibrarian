package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCheckHTTPAcceptsSuccessfulStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			withHTTPStatus(t, status)

			if err := checkHTTP(context.Background(), "http://healthcheck.local/readyz"); err != nil {
				t.Fatalf("checkHTTP returned error for status %d: %v", status, err)
			}
		})
	}
}

func TestCheckHTTPRejectsUnsuccessfulStatus(t *testing.T) {
	withHTTPStatus(t, http.StatusServiceUnavailable)

	if err := checkHTTP(context.Background(), "http://healthcheck.local/readyz"); err == nil {
		t.Fatal("checkHTTP returned nil for service unavailable status")
	}
}

func withHTTPStatus(t *testing.T, status int) {
	t.Helper()
	previous := httpClient
	httpClient = &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}),
	}
	t.Cleanup(func() {
		httpClient = previous
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
