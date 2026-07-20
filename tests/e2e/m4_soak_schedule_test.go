//go:build e2e && m4 && m4_soak

package e2e_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestM4SoakDurationConfigurationIsBounded(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", raw: "", want: 30 * time.Minute},
		{name: "minimum", raw: "1s", want: time.Second},
		{name: "release", raw: "30m", want: 30 * time.Minute},
		{name: "maximum", raw: "24h", want: 24 * time.Hour},
		{name: "below minimum", raw: "999ms", wantErr: true},
		{name: "above maximum", raw: "24h1s", wantErr: true},
		{name: "zero", raw: "0s", wantErr: true},
		{name: "negative", raw: "-1s", wantErr: true},
		{name: "malformed", raw: "thirty minutes", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseM4SoakDuration(test.raw)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestM4SoakScheduleFinishesTheIterationCrossingTheDeadline(t *testing.T) {
	clock := newM4FakeClock()
	iterations, elapsed := runM4SoakSchedule(2500*time.Millisecond, 1, clock.Now, func(int) {
		clock.Advance(time.Second)
	})

	assert.Equal(t, 3, iterations)
	assert.Equal(t, 3*time.Second, elapsed)
}

func TestM4SoakScheduleCompletesTheMinimumCorpusAfterDuration(t *testing.T) {
	clock := newM4FakeClock()
	var indexes []int
	iterations, elapsed := runM4SoakSchedule(time.Second, 5, clock.Now, func(index int) {
		indexes = append(indexes, index)
		clock.Advance(250 * time.Millisecond)
	})

	assert.Equal(t, 5, iterations)
	assert.Equal(t, 1250*time.Millisecond, elapsed)
	assert.Equal(t, []int{0, 1, 2, 3, 4}, indexes)
}

func TestM4SoakScheduleDoesNotStartAtTheExactDeadline(t *testing.T) {
	clock := newM4FakeClock()
	iterations, elapsed := runM4SoakSchedule(3*time.Second, 1, clock.Now, func(int) {
		clock.Advance(time.Second)
	})

	assert.Equal(t, 3, iterations)
	assert.Equal(t, 3*time.Second, elapsed)
}

func TestM4SoakScheduleHonorsTheLiveMinimumWithoutAnExtraIteration(t *testing.T) {
	clock := newM4FakeClock()
	iterations, elapsed := runM4SoakSchedule(time.Second, 10, clock.Now, func(int) {
		clock.Advance(100 * time.Millisecond)
	})

	assert.Equal(t, 10, iterations)
	assert.Equal(t, time.Second, elapsed)
}

func TestM4SoakUploadIntervalRespectsThePerEdgeHourlyBudget(t *testing.T) {
	assert.Equal(t, 3*time.Minute, m4SoakUploadInterval(1))
	assert.Equal(t, 90*time.Second, m4SoakUploadInterval(2))
}

func TestM4SoakDeadlineRequiresOneStatusTimeoutOfHeadroom(t *testing.T) {
	now := time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC)
	require.Error(t, validateM4SoakDeadline(now, now.Add(32*time.Minute), 30*time.Minute, 2*time.Minute))
	require.NoError(t, validateM4SoakDeadline(now, now.Add(32*time.Minute+time.Nanosecond), 30*time.Minute, 2*time.Minute))
}

func TestM4RefreshCookieHeaderAcceptsOnlyTheExpectedCookie(t *testing.T) {
	header, err := m4RefreshCookieHeader([]*http.Cookie{
		{Name: "unrelated", Value: "ignored"},
		{Name: "__Host-refresh_token", Value: "refresh-value"},
	})
	require.NoError(t, err)
	assert.Equal(t, "__Host-refresh_token=refresh-value", header)

	_, err = parseM4RefreshCookieHeader(header)
	require.NoError(t, err)
	for _, invalid := range []string{
		"",
		"session=value",
		"refresh_token=one; session=two",
		"refresh_token=value\r\nInjected: true",
		" refresh_token=value",
	} {
		_, err = parseM4RefreshCookieHeader(invalid)
		require.Error(t, err)
	}
}

func TestRefreshM4SoakSessionRotatesBothCredentials(t *testing.T) {
	client := &http.Client{Transport: m4RoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, http.MethodPost, request.Method)
		assert.Equal(t, "/auth/refresh", request.URL.Path)
		assert.Equal(t, "https://library.example", request.Header.Get("Origin"))
		assert.Equal(t, "refresh_token=old-refresh", request.Header.Get("Cookie"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"Set-Cookie":   []string{"refresh_token=new-refresh; Path=/; HttpOnly"},
			},
			Body: io.NopCloser(strings.NewReader(`{"token":"new-access"}`)),
		}, nil
	})}

	accessToken, refreshCookie, err := refreshM4SoakSession(
		context.Background(),
		client,
		"https://edge.example",
		"https://library.example",
		"refresh_token=old-refresh",
	)
	require.NoError(t, err)
	assert.Equal(t, "new-access", accessToken)
	assert.Equal(t, "refresh_token=new-refresh", refreshCookie)
}

func TestM4SoakSessionRefreshesBeforeTheFirstUpload(t *testing.T) {
	events := make([]string, 0, 2)
	client := &http.Client{Transport: m4RoundTripFunc(func(*http.Request) (*http.Response, error) {
		events = append(events, "refresh")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Set-Cookie": []string{"refresh_token=rotated-refresh; Path=/; HttpOnly"},
			},
			Body: io.NopCloser(strings.NewReader(`{"token":"rotated-access"}`)),
		}, nil
	})}
	startedAt := time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC)

	session, err := newM4SoakSession(
		context.Background(),
		client,
		"https://edge.example",
		"https://library.example",
		"refresh_token=initial-refresh",
		func() time.Time { return startedAt },
	)
	require.NoError(t, err)
	events = append(events, "upload:"+session.accessToken)

	assert.Equal(t, []string{"refresh", "upload:rotated-access"}, events)
	assert.Equal(t, "refresh_token=rotated-refresh", session.refreshCookie)
	assert.Equal(t, startedAt.Add(m4SoakRefreshInterval), session.nextRefreshAt)
}

func TestRefreshM4SoakSessionReturnsSanitizedErrors(t *testing.T) {
	const credentialCanary = "credential-canary"
	client := &http.Client{Transport: m4RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(credentialCanary)),
		}, nil
	})}

	_, _, err := refreshM4SoakSession(
		context.Background(),
		client,
		"https://edge.example",
		"https://library.example",
		"refresh_token="+credentialCanary,
	)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), credentialCanary)
	assert.NotContains(t, err.Error(), "edge.example")
}

type m4RoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip m4RoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type m4FakeClock struct {
	now time.Time
}

func newM4FakeClock() *m4FakeClock {
	return &m4FakeClock{now: time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC)}
}

func (clock *m4FakeClock) Now() time.Time {
	return clock.now
}

func (clock *m4FakeClock) Advance(duration time.Duration) {
	clock.now = clock.now.Add(duration)
}
