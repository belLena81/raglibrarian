package middleware_test

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qmiddleware "github.com/belLena81/raglibrarian/services/edge-api/middleware"
)

func TestRequestIDIgnoresClientValueAndGenerates128Bits(t *testing.T) {
	const clientValue = "sensitive-client-request-id"

	var contextID string
	handler := qmiddleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contextID = chimiddleware.GetReqID(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", clientValue)

	handler.ServeHTTP(recorder, request)

	responseID := recorder.Header().Get("X-Request-ID")
	require.NotEmpty(t, responseID)
	assert.Equal(t, responseID, contextID)
	assert.NotEqual(t, clientValue, responseID)
	decoded, err := hex.DecodeString(responseID)
	require.NoError(t, err)
	assert.Len(t, decoded, 16)
}

func TestRequestIDGeneratesDifferentValues(t *testing.T) {
	handler := qmiddleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRecorder()
	handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.NotEqual(t, first.Header().Get("X-Request-ID"), second.Header().Get("X-Request-ID"))
}
