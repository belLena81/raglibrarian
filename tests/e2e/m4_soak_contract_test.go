//go:build e2e && m4 && m4_soak

package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

const (
	defaultM4SoakDuration = 30 * time.Minute
	minimumM4SoakDuration = time.Second
	maximumM4SoakDuration = 24 * time.Hour
	m4SoakRefreshInterval = 10 * time.Minute
	m4SoakRefreshTimeout  = 30 * time.Second
)

type m4SoakSession struct {
	accessToken   string
	refreshCookie string
	nextRefreshAt time.Time
}

// TestM4SoakRepeatedIngestion exercises repeated success and permanent-failure
// paths for both the configured minimum duration and iteration floor.
func TestM4SoakRepeatedIngestion(t *testing.T) {
	environment := loadM4Environment(t, false)
	refreshCookie := loadM4FileSecret(t, "M4_E2E_REFRESH_COOKIE_FILE")
	if refreshCookie == "" {
		t.Fatal("M4_E2E_REFRESH_COOKIE_FILE is required")
	}
	if _, err := parseM4RefreshCookieHeader(refreshCookie); err != nil {
		t.Fatal(err)
	}
	duration, err := parseM4SoakDuration(os.Getenv("M4_SOAK_DURATION"))
	if err != nil {
		t.Fatal(err)
	}
	minimumIterations := 10
	if raw := strings.TrimSpace(os.Getenv("M4_E2E_SOAK_ITERATIONS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 10 || parsed > 1000 {
			t.Fatal("M4_E2E_SOAK_ITERATIONS must be an integer between 10 and 1000")
		}
		minimumIterations = parsed
	}
	uploadInterval := m4SoakUploadInterval(len(environment.edgeURLs))
	minimumRunDuration := time.Duration(minimumIterations-1) * uploadInterval
	requiredRunDuration := max(duration, minimumRunDuration)
	if deadline, ok := t.Deadline(); ok {
		if err = validateM4SoakDeadline(time.Now(), deadline, requiredRunDuration, environment.timeout); err != nil {
			t.Fatal(err)
		}
	}
	refreshContext, cancelRefresh := context.WithTimeout(t.Context(), m4SoakRefreshTimeout)
	session, err := newM4SoakSession(
		refreshContext,
		http.DefaultClient,
		environment.edgeURLs[0],
		environment.publicOrigin,
		refreshCookie,
		time.Now,
	)
	cancelRefresh()
	if err != nil {
		t.Fatal(err)
	}
	environment.accessToken = session.accessToken
	fixtures := []struct {
		name    string
		stage   string
		failure string
	}{
		{name: "minimal.pdf", stage: "chunks_ready"},
		{name: "multipage.pdf", stage: "chunks_ready"},
		{name: "blank_middle_page.pdf", stage: "chunks_ready"},
		{name: "image_only.pdf", stage: "failed", failure: "no_extractable_text"},
		{name: "malformed.pdf", stage: "failed", failure: "malformed_document"},
	}
	nextUploadAt := time.Now()
	iterations, elapsed := runM4SoakSchedule(duration, minimumIterations, time.Now, func(index int) {
		if index > 0 {
			waitForM4SoakUploadSlot(t, nextUploadAt)
		}
		nextUploadAt = time.Now().Add(uploadInterval)
		if !time.Now().Before(session.nextRefreshAt) {
			ctx, cancel := context.WithTimeout(t.Context(), m4SoakRefreshTimeout)
			session.accessToken, session.refreshCookie, err = refreshM4SoakSession(
				ctx,
				http.DefaultClient,
				environment.edgeURLs[index%len(environment.edgeURLs)],
				environment.publicOrigin,
				session.refreshCookie,
			)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			environment.accessToken = session.accessToken
			session.nextRefreshAt = time.Now().Add(m4SoakRefreshInterval)
		}
		fixture := fixtures[index%len(fixtures)]
		book := uploadM4Fixture(t, environment.edgeURLs[index%len(environment.edgeURLs)], session.accessToken, environment.fixtureDir, fixture.name)
		book = waitForM4Status(t, environment, environment.edgeURLs[(index+1)%len(environment.edgeURLs)], book.ID, func(current m4Book) bool {
			return current.ProcessingStage == fixture.stage
		})
		assert.Equal(t, fixture.failure, book.ProcessingFailureCategory)
		assert.Positive(t, book.ProcessingVersion)
	})
	t.Logf("M4 soak completed: elapsed=%s iterations=%d fixtures=%d", elapsed, iterations, len(fixtures))
}

func newM4SoakSession(
	ctx context.Context,
	client *http.Client,
	edgeURL string,
	publicOrigin string,
	refreshCookie string,
	now func() time.Time,
) (m4SoakSession, error) {
	accessToken, rotatedCookie, err := refreshM4SoakSession(ctx, client, edgeURL, publicOrigin, refreshCookie)
	if err != nil {
		return m4SoakSession{}, err
	}
	return m4SoakSession{
		accessToken:   accessToken,
		refreshCookie: rotatedCookie,
		nextRefreshAt: now().Add(m4SoakRefreshInterval),
	}, nil
}

func refreshM4SoakSession(
	ctx context.Context,
	client *http.Client,
	edgeURL string,
	publicOrigin string,
	refreshCookie string,
) (string, string, error) {
	if _, err := parseM4RefreshCookieHeader(refreshCookie); err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, edgeURL+"/auth/refresh", nil)
	if err != nil {
		return "", "", errors.New("M4 soak refresh request could not be created")
	}
	request.Header.Set("Origin", publicOrigin)
	request.Header.Set("Cookie", refreshCookie)
	response, err := client.Do(request)
	if err != nil {
		return "", "", errors.New("M4 soak session refresh failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", "", errors.New("M4 soak session refresh returned an unexpected status")
	}
	var payload struct {
		Token string `json:"token"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err = decoder.Decode(&payload); err != nil || ensureJSONEOF(decoder) != nil {
		return "", "", errors.New("M4 soak session refresh returned invalid JSON")
	}
	if payload.Token == "" || len(payload.Token) > 16<<10 || strings.ContainsAny(payload.Token, "\r\n") {
		return "", "", errors.New("M4 soak session refresh returned an invalid access token")
	}
	rotatedCookie, err := m4RefreshCookieHeader(response.Cookies())
	if err != nil {
		return "", "", errors.New("M4 soak session refresh did not rotate the refresh cookie")
	}
	return payload.Token, rotatedCookie, nil
}

func parseM4RefreshCookieHeader(value string) (*http.Cookie, error) {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 16<<10 || strings.ContainsAny(value, "\r\n;") {
		return nil, errors.New("M4 refresh session cookie file is invalid")
	}
	request := &http.Request{Header: http.Header{"Cookie": []string{value}}}
	cookies := request.Cookies()
	if len(cookies) != 1 {
		return nil, errors.New("M4 refresh session cookie file is invalid")
	}
	cookie := cookies[0]
	if cookie.Name != "refresh_token" && cookie.Name != "__Host-refresh_token" {
		return nil, errors.New("M4 refresh session cookie file is invalid")
	}
	if cookie.Value == "" || len(cookie.Value) > 16<<10 || strings.ContainsAny(cookie.Value, "\r\n;") {
		return nil, errors.New("M4 refresh session cookie file is invalid")
	}
	return cookie, nil
}

func m4SoakUploadInterval(edgeCount int) time.Duration {
	if edgeCount < 1 {
		panic("M4 soak requires at least one Edge endpoint")
	}
	return time.Hour / time.Duration(20*edgeCount)
}

func waitForM4SoakUploadSlot(t *testing.T, nextUploadAt time.Time) {
	t.Helper()
	wait := time.Until(nextUploadAt)
	if wait <= 0 {
		return
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("M4 soak cancelled while waiting for the next upload slot")
	}
}

func parseM4SoakDuration(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return defaultM4SoakDuration, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < minimumM4SoakDuration || duration > maximumM4SoakDuration {
		return 0, errors.New("M4_SOAK_DURATION must be a duration between 1s and 24h")
	}
	return duration, nil
}

func validateM4SoakDeadline(now, deadline time.Time, duration, statusTimeout time.Duration) error {
	if deadline.Sub(now) <= duration+statusTimeout {
		return errors.New("M4 soak test timeout must exceed the required soak window by at least one status timeout")
	}
	return nil
}

func runM4SoakSchedule(
	duration time.Duration,
	minimumIterations int,
	now func() time.Time,
	run func(index int),
) (int, time.Duration) {
	startedAt := now()
	iterations := 0
	elapsed := time.Duration(0)
	for iterations < minimumIterations || elapsed < duration {
		run(iterations)
		iterations++
		elapsed = now().Sub(startedAt)
	}
	return iterations, elapsed
}
