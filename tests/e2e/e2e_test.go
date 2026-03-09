//go:build e2e

// Package e2e contains end-to-end tests that run against a live service.
//
// Prerequisites (all managed by `make e2e`):
//   - Postgres running and migrated
//   - Query service running on E2E_BASE_URL (default: http://localhost:8080)
//
// Run manually:
//
//	make infra-up && make migrate-up && make run-query   # terminal 1
//	make e2e                                             # terminal 2
//
// Override the target:
//
//	E2E_BASE_URL=http://staging:8080 make e2e
package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Config ────────────────────────────────────────────────────────────────────

func baseURL() string {
	if u := os.Getenv("E2E_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}

// uniqueEmail generates a unique address per test run so repeated runs
// against the same DB do not fail with "email already registered".
func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s+%d@e2e.example.com", prefix, time.Now().UnixNano())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func postJSON(t *testing.T, path string, body any, token string) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		b, merr := json.Marshal(body)
		require.NoError(t, merr)
		req, err = http.NewRequest(http.MethodPost, baseURL()+path, bytes.NewReader(b))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(http.MethodPost, baseURL()+path, nil)
		require.NoError(t, err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// requireStatus asserts the expected status code and prints the body on failure.
func requireStatus(t *testing.T, want int, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != want {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("expected status %d, got %d — body: %s", want, resp.StatusCode, buf.String())
	}
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(dst))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestHealthz(t *testing.T) {
	resp, err := http.Get(baseURL() + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHealthz_ReturnsRequestIDHeader(t *testing.T) {
	resp, err := http.Get(baseURL() + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.NotEmpty(t, resp.Header.Get("X-Request-Id"))
}

func TestRegister_ValidReader_Returns201WithToken(t *testing.T) {
	resp := postJSON(t, "/auth/register", map[string]string{
		"email":    uniqueEmail("reader"),
		"password": "s3cr3t123",
		"role":     "reader",
	}, "")

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body map[string]string
	decodeJSON(t, resp, &body)
	assert.NotEmpty(t, body["token"])
	assert.Equal(t, "reader", body["role"])
}

func TestRegister_ValidAdmin_Returns201WithAdminRole(t *testing.T) {
	resp := postJSON(t, "/auth/register", map[string]string{
		"email":    uniqueEmail("admin"),
		"password": "s3cr3t123",
		"role":     "admin",
	}, "")

	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var body map[string]string
	decodeJSON(t, resp, &body)
	assert.Equal(t, "admin", body["role"])
}

func TestRegister_DuplicateEmail_Returns409(t *testing.T) {
	email := uniqueEmail("dup")
	payload := map[string]string{"email": email, "password": "pw", "role": "reader"}

	resp := postJSON(t, "/auth/register", payload, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Second registration with same email must be rejected.
	resp = postJSON(t, "/auth/register", payload, "")
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

func TestRegister_InvalidEmail_Returns422(t *testing.T) {
	resp := postJSON(t, "/auth/register", map[string]string{
		"email":    "not-an-email",
		"password": "pw",
		"role":     "reader",
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestRegister_InvalidRole_Returns422(t *testing.T) {
	resp := postJSON(t, "/auth/register", map[string]string{
		"email":    uniqueEmail("badrole"),
		"password": "pw",
		"role":     "superuser",
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestLogin_ValidCredentials_Returns200WithToken(t *testing.T) {
	email := uniqueEmail("login")
	password := "loginpass"

	// Register first.
	resp := postJSON(t, "/auth/register", map[string]string{
		"email": email, "password": password, "role": "reader",
	}, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Then login.
	resp = postJSON(t, "/auth/login", map[string]string{
		"email": email, "password": password,
	}, "")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]string
	decodeJSON(t, resp, &body)
	assert.NotEmpty(t, body["token"])
}

func TestLogin_WrongPassword_Returns401(t *testing.T) {
	email := uniqueEmail("wrongpw")

	resp := postJSON(t, "/auth/register", map[string]string{
		"email": email, "password": "correct", "role": "reader",
	}, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = postJSON(t, "/auth/login", map[string]string{
		"email": email, "password": "wrong",
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestLogin_UnknownEmail_Returns401(t *testing.T) {
	// Must return the same 401 as wrong password — no user enumeration.
	resp := postJSON(t, "/auth/login", map[string]string{
		"email": "nobody@nowhere.example.com", "password": "irrelevant",
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestMe_WithValidToken_Returns200WithIdentity(t *testing.T) {
	email := uniqueEmail("me")
	resp := postJSON(t, "/auth/register", map[string]string{
		"email": email, "password": "mepass", "role": "reader",
	}, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var reg map[string]string
	decodeJSON(t, resp, &reg)
	token := reg["token"]
	require.NotEmpty(t, token)

	meResp, err := http.NewRequest(http.MethodGet, baseURL()+"/auth/me", nil)
	require.NoError(t, err)
	meResp.Header.Set("Authorization", "Bearer "+token)
	result, err := http.DefaultClient.Do(meResp)
	require.NoError(t, err)
	requireStatus(t, http.StatusOK, result)

	var body map[string]string
	decodeJSON(t, result, &body)
	assert.Equal(t, email, body["email"])
	assert.Equal(t, "reader", body["role"])
	assert.NotEmpty(t, body["user_id"])
}

func TestMe_WithoutToken_Returns401(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, baseURL()+"/auth/me", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestLogout_WithValidToken_Returns200(t *testing.T) {
	email := uniqueEmail("logout")
	resp := postJSON(t, "/auth/register", map[string]string{
		"email": email, "password": "logoutpass", "role": "reader",
	}, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var reg map[string]string
	decodeJSON(t, resp, &reg)
	token := reg["token"]

	resp = postJSON(t, "/auth/logout", nil, token)
	requireStatus(t, http.StatusOK, resp)

	var body map[string]string
	decodeJSON(t, resp, &body)
	assert.Equal(t, "logged out", body["message"])
}

func TestLogout_WithoutToken_Returns401(t *testing.T) {
	resp := postJSON(t, "/auth/logout", nil, "")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestQuery_WithValidToken_Returns200(t *testing.T) {
	email := uniqueEmail("query")
	password := "querypass"

	resp := postJSON(t, "/auth/register", map[string]string{
		"email": email, "password": password, "role": "reader",
	}, "")
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var reg map[string]string
	decodeJSON(t, resp, &reg)
	token := reg["token"]
	require.NotEmpty(t, token)

	resp = postJSON(t, "/query/", map[string]string{
		"question": "What is a goroutine?",
		"user_id":  "e2e-user",
	}, token)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestQuery_WithoutToken_Returns401(t *testing.T) {
	resp := postJSON(t, "/query/", map[string]string{
		"question": "test?", "user_id": "u",
	}, "")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestQuery_WithInvalidToken_Returns401(t *testing.T) {
	resp := postJSON(t, "/query/", map[string]string{
		"question": "test?", "user_id": "u",
	}, "v4.local.totallyinvalid")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestUnknownRoute_Returns404(t *testing.T) {
	resp, err := http.Get(baseURL() + "/nonexistent")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
