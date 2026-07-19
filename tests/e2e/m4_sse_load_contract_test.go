//go:build e2e && m4 && m4_load

package e2e_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestM4SSEConnectionCapIsEnforced runs only against a dedicated load-test
// stack whose configured cap is supplied explicitly. It must not share an Edge
// instance with unrelated browser sessions.
func TestM4SSEConnectionCapIsEnforced(t *testing.T) {
	environment := loadM4Environment(t, false)
	connectionCap := requiredM4BoundedInt(t, "M4_E2E_SSE_CONNECTION_CAP", 1, 10)
	accessTokens := requiredM4AccessTokens(t, connectionCap)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	streams := make([]*m4EventStream, 0, connectionCap)
	for index := 0; index < connectionCap; index++ {
		stream := openM4EventStream(t, ctx, environment.edgeURLs[0], environment.publicOrigin, accessTokens[index], "")
		event, err := readM4SSEEvent(stream)
		require.NoError(t, err)
		require.Equal(t, "books-resync", event.eventType)
		streams = append(streams, stream)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
	require.NoError(t, err)
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Authorization", "Bearer "+accessTokens[0])
	request.Header.Set("Origin", environment.publicOrigin)
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	assert.Contains(t, []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}, response.StatusCode)
	if response.StatusCode == http.StatusTooManyRequests {
		assert.NotEmpty(t, response.Header.Get("Retry-After"))
	}

	// Keep references alive until after the over-cap request. Closing one stream
	// must release capacity promptly and allow a replacement connection.
	require.NoError(t, streams[0].body.Close())
	replacementDeadline := time.Now().Add(5 * time.Second)
	for {
		replacementRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, environment.edgeURLs[0]+"/books/events", nil)
		require.NoError(t, requestErr)
		replacementRequest.Header.Set("Accept", "text/event-stream")
		replacementRequest.Header.Set("Authorization", "Bearer "+accessTokens[0])
		replacementRequest.Header.Set("Origin", environment.publicOrigin)
		replacement, requestErr := http.DefaultClient.Do(replacementRequest)
		require.NoError(t, requestErr)
		if replacement.StatusCode == http.StatusOK {
			_ = replacement.Body.Close()
			break
		}
		_ = replacement.Body.Close()
		if time.Now().After(replacementDeadline) {
			t.Fatal("SSE capacity was not released after disconnect")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func requiredM4AccessTokens(t *testing.T, count int) []string {
	t.Helper()
	var tokens []string
	for _, token := range strings.Split(os.Getenv("M4_E2E_SSE_ACCESS_TOKENS"), ",") {
		if token = strings.TrimSpace(token); token != "" {
			tokens = append(tokens, token)
		}
	}
	if len(tokens) < count {
		t.Fatalf("M4_E2E_SSE_ACCESS_TOKENS must contain at least %d distinct active-session tokens", count)
	}
	return tokens[:count]
}

func requiredM4BoundedInt(t *testing.T, key string, minimum, maximum int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		t.Fatalf("%s must be an integer between %d and %d", key, minimum, maximum)
	}
	return value
}
