//go:build e2e

// Package e2e contains black-box tests that run against a live service stack.
//
// The complete Milestone 2 lifecycle additionally requires:
//   - E2E_BOOTSTRAP_CODE: the one-time code configured for the test stack
//   - E2E_MAIL_FIXTURE_URL: a local Mailpit latest-message endpoint or a
//     test-only endpoint returning {"token":"..."} for the latest message
//
// Neither value is printed by these tests. The mail endpoint must only be
// exposed inside the local test environment; it is not a production API.
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validPassword = "correct-horse-123"

var verificationTokenPattern = regexp.MustCompile(`#[A-Za-z0-9_-]{43}`)

type authSession struct {
	Token         string `json:"token"`
	RefreshCookie string `json:"-"`
}

type principal struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

type pendingUser struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	RegisteredAt string `json:"registered_at"`
}

type pendingPage struct {
	Users         []pendingUser `json:"users"`
	NextPageToken string        `json:"next_page_token"`
}

type errorResponse struct {
	Code      string `json:"code"`
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

type book struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Author           string `json:"author"`
	ProcessingStatus string `json:"processing_status"`
}

type bookPage struct {
	Books []book `json:"books"`
}

func baseURL() string {
	if value := os.Getenv("E2E_BASE_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "http://localhost:8080"
}

func uniqueEmail(prefix string) string {
	return fmt.Sprintf("%s+%d@e2e.example.com", prefix, time.Now().UnixNano())
}

func request(t *testing.T, method, path string, body any, token string) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, baseURL()+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func uploadBook(t *testing.T, token string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := textproto.MIMEHeader{}
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	require.NoError(t, err)
	_, err = metadataPart.Write([]byte("{\"title\":\"E2E Catalog Book\",\"author\":\"E2E Author\",\"year\":2026,\"tags\":[\"e2e\"]}"))
	require.NoError(t, err)
	fileHeader := textproto.MIMEHeader{}
	fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="book.pdf"`)
	fileHeader.Set("Content-Type", "application/pdf")
	filePart, err := writer.CreatePart(fileHeader)
	require.NoError(t, err)
	_, err = filePart.Write([]byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF\n"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL()+"/books", &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func requireStatus(t *testing.T, want int, resp *http.Response) {
	t.Helper()
	require.Equal(t, want, resp.StatusCode, "unexpected HTTP status")
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	require.NoError(t, decoder.Decode(dst), "response was not valid bounded JSON")
	require.NoError(t, ensureJSONEOF(decoder), "response contained trailing JSON")
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	require.NoError(t, resp.Body.Close())
}

func writeM4SessionToken(t *testing.T, environmentKey, token string) {
	t.Helper()
	path := strings.TrimSpace(os.Getenv(environmentKey))
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) || strings.TrimSpace(token) == "" {
		t.Fatalf("%s must be an absolute output path for a non-empty session", environmentKey)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- explicit CI-owned secret output.
	if err != nil {
		t.Fatalf("could not create the requested M4 session handoff for %s", environmentKey)
	}
	if _, err = io.WriteString(file, token); err != nil {
		_ = file.Close()
		t.Fatalf("could not write the requested M4 session handoff for %s", environmentKey)
	}
	require.NoError(t, file.Close())
}

func assertPrivateNoStore(t *testing.T, resp *http.Response) {
	t.Helper()
	cacheControl := strings.ToLower(resp.Header.Get("Cache-Control"))
	assert.Contains(t, cacheControl, "no-store")
	assert.Contains(t, cacheControl, "private")
}

func assertNoSessionArtifacts(t *testing.T, resp *http.Response) {
	t.Helper()
	if count := len(resp.Header.Values("Set-Cookie")); count != 0 {
		t.Errorf("response must not create a session; got %d Set-Cookie headers", count)
	}
}

func requireSanitizedError(t *testing.T, resp *http.Response) errorResponse {
	t.Helper()
	assertPrivateNoStore(t, resp)
	var raw map[string]json.RawMessage
	decodeJSON(t, resp, &raw)
	allowed := map[string]bool{"code": true, "error": true, "request_id": true}
	if len(raw) != len(allowed) {
		t.Errorf("sanitized error must contain exactly %d fields; got %d", len(allowed), len(raw))
	}
	for key := range raw {
		if !allowed[key] {
			t.Errorf("sanitized error contained unexpected field %q", key)
		}
	}
	var body errorResponse
	require.NoError(t, json.Unmarshal(raw["code"], &body.Code))
	require.NoError(t, json.Unmarshal(raw["error"], &body.Error))
	require.NoError(t, json.Unmarshal(raw["request_id"], &body.RequestID))
	require.NotEmpty(t, body.Code)
	require.NotEmpty(t, body.Error)
	require.NotEmpty(t, body.RequestID)
	lowerError := strings.ToLower(body.Error)
	for _, forbidden := range []string{"sql", "grpc", "stack", "password"} {
		if strings.Contains(lowerError, forbidden) {
			t.Error("error message contained a forbidden implementation or credential detail")
		}
	}
	return body
}

func requireExactBooleanResponse(t *testing.T, resp *http.Response, key string) {
	t.Helper()
	var body map[string]json.RawMessage
	decodeJSON(t, resp, &body)
	if len(body) != 1 {
		t.Errorf("response must contain exactly one field; got %d", len(body))
	}
	for actualKey := range body {
		if actualKey != key {
			t.Errorf("response contained unexpected field %q", actualKey)
		}
	}
	var value bool
	require.NoError(t, json.Unmarshal(body[key], &value))
	assert.True(t, value)
}

func register(t *testing.T, name, email, role string) {
	t.Helper()
	resp := request(t, http.MethodPost, "/auth/register", map[string]string{
		"name": name, "email": email, "password": validPassword, "role": role,
	}, "")
	requireStatus(t, http.StatusAccepted, resp)
	assertPrivateNoStore(t, resp)
	assertNoSessionArtifacts(t, resp)
	requireExactBooleanResponse(t, resp, "accepted")
}

func mailVerificationToken(t *testing.T, email string) string {
	t.Helper()
	fixtureURL := os.Getenv("E2E_MAIL_FIXTURE_URL")
	if fixtureURL == "" {
		t.Skip("E2E_MAIL_FIXTURE_URL is required for email-verification lifecycle tests")
	}

	parsed, err := url.Parse(fixtureURL)
	require.NoError(t, err, "mail fixture URL is invalid")

	var token string
	require.Eventually(t, func() bool {
		payload, available := verificationPayload(context.Background(), parsed, email)
		if !available {
			return false
		}
		var body struct {
			Token string `json:"token"`
		}
		if json.Unmarshal(payload, &body) == nil && body.Token != "" {
			token = body.Token
			return true
		}
		match := verificationTokenPattern.Find(payload)
		if len(match) != 44 {
			return false
		}
		token = string(match[1:])
		return true
	}, 10*time.Second, 200*time.Millisecond, "verification message was not available")
	return token
}

func verificationPayload(ctx context.Context, fixtureURL *url.URL, email string) ([]byte, bool) {
	if strings.HasSuffix(fixtureURL.Path, "/view/latest.txt") {
		searchURL := *fixtureURL
		searchURL.Path = "/api/v1/search"
		query := url.Values{}
		query.Set("query", `to:"`+email+`"`)
		searchURL.RawQuery = query.Encode()
		payload, ok := getBounded(ctx, searchURL.String())
		if !ok {
			return nil, false
		}
		var search struct {
			Messages []struct {
				ID string `json:"ID"`
			} `json:"messages"`
		}
		if json.Unmarshal(payload, &search) != nil || len(search.Messages) == 0 || search.Messages[0].ID == "" {
			return nil, false
		}
		messageURL := *fixtureURL
		messageURL.Path = "/view/" + url.PathEscape(search.Messages[0].ID) + ".txt"
		messageURL.RawQuery = ""
		return getBounded(ctx, messageURL.String())
	}
	customURL := *fixtureURL
	query := customURL.Query()
	query.Set("recipient", email)
	customURL.RawQuery = query.Encode()
	return getBounded(ctx, customURL.String())
}

func getBounded(ctx context.Context, endpoint string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return payload, err == nil
}

func verifyEmail(t *testing.T, email string) {
	t.Helper()
	token := mailVerificationToken(t, email)
	resp := request(t, http.MethodPost, "/auth/verify-email", map[string]string{"token": token}, "")
	requireStatus(t, http.StatusNoContent, resp)
	assertPrivateNoStore(t, resp)
	assertNoSessionArtifacts(t, resp)
	closeBody(t, resp)
}

func login(t *testing.T, email string) authSession {
	t.Helper()
	resp := request(t, http.MethodPost, "/auth/login", map[string]string{
		"email": email, "password": validPassword,
	}, "")
	requireStatus(t, http.StatusOK, resp)
	assertPrivateNoStore(t, resp)
	refreshCookie, err := m4RefreshCookieHeader(resp.Cookies())
	require.NoError(t, err)
	var session authSession
	decodeJSON(t, resp, &session)
	require.NotEmpty(t, session.Token)
	session.RefreshCookie = refreshCookie
	return session
}

func m4RefreshCookieHeader(cookies []*http.Cookie) (string, error) {
	for _, cookie := range cookies {
		if cookie.Name != "refresh_token" && cookie.Name != "__Host-refresh_token" {
			continue
		}
		if cookie.Value == "" || len(cookie.Value) > 16<<10 || strings.ContainsAny(cookie.Value, "\r\n;") {
			return "", fmt.Errorf("refresh session cookie was invalid")
		}
		return cookie.Name + "=" + cookie.Value, nil
	}
	return "", fmt.Errorf("refresh session cookie was missing")
}

func getMe(t *testing.T, token string) principal {
	t.Helper()
	resp := request(t, http.MethodGet, "/auth/me", nil, token)
	requireStatus(t, http.StatusOK, resp)
	assertPrivateNoStore(t, resp)
	var body principal
	decodeJSON(t, resp, &body)
	return body
}

func TestHealthAndReadiness(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			resp := request(t, http.MethodGet, path, nil, "")
			defer closeBody(t, resp)
			assert.Equal(t, http.StatusOK, resp.StatusCode)
			assert.NotEmpty(t, resp.Header.Get("X-Request-ID"))
		})
	}
}

func TestSetupStatusContract(t *testing.T) {
	resp := request(t, http.MethodGet, "/setup/status", nil, "")
	requireStatus(t, http.StatusOK, resp)
	assertPrivateNoStore(t, resp)
	var body struct {
		Required bool `json:"required"`
	}
	decodeJSON(t, resp, &body)
}

func TestRegisterIsGenericAndCreatesNoSession(t *testing.T) {
	email := uniqueEmail("generic")
	payload := map[string]string{
		"name": "Generic Reader", "email": email, "password": validPassword, "role": "reader",
	}

	for attempt := 0; attempt < 2; attempt++ {
		resp := request(t, http.MethodPost, "/auth/register", payload, "")
		requireStatus(t, http.StatusAccepted, resp)
		assertPrivateNoStore(t, resp)
		assertNoSessionArtifacts(t, resp)
		requireExactBooleanResponse(t, resp, "accepted")
	}
}

func TestStrictJSONAndSanitizedErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
		body map[string]string
	}{
		{
			name: "register",
			path: "/auth/register",
			body: map[string]string{"name": "Reader", "email": uniqueEmail("unknown-register"), "password": validPassword, "role": "reader", "unexpected": "value"},
		},
		{
			name: "verify email",
			path: "/auth/verify-email",
			body: map[string]string{"token": "invalid-test-value", "unexpected": "value"},
		},
		{
			name: "resend verification",
			path: "/auth/verification/resend",
			body: map[string]string{"email": uniqueEmail("unknown-resend"), "unexpected": "value"},
		},
		{
			name: "login",
			path: "/auth/login",
			body: map[string]string{"email": uniqueEmail("unknown-login"), "password": validPassword, "unexpected": "value"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := request(t, http.MethodPost, test.path, test.body, "")
			requireStatus(t, http.StatusBadRequest, resp)
			assertNoSessionArtifacts(t, resp)
			requireSanitizedError(t, resp)
		})
	}
}

func TestInvalidVerificationTokensShareSanitizedResponse(t *testing.T) {
	var first errorResponse
	for attempt := 0; attempt < 2; attempt++ {
		resp := request(t, http.MethodPost, "/auth/verify-email", map[string]string{
			"token": fmt.Sprintf("invalid-test-value-%d", attempt),
		}, "")
		requireStatus(t, http.StatusBadRequest, resp)
		assertNoSessionArtifacts(t, resp)
		body := requireSanitizedError(t, resp)
		if attempt == 0 {
			first = body
			continue
		}
		if first.Code != body.Code || first.Error != body.Error {
			t.Error("invalid verification tokens did not share one sanitized error contract")
		}
	}
}

func TestVerificationResendIsGeneric(t *testing.T) {
	resp := request(t, http.MethodPost, "/auth/verification/resend", map[string]string{
		"email": uniqueEmail("absent-resend"),
	}, "")
	requireStatus(t, http.StatusAccepted, resp)
	assertPrivateNoStore(t, resp)
	assertNoSessionArtifacts(t, resp)
	requireExactBooleanResponse(t, resp, "accepted")
}

func TestMilestone2IdentityLifecycle(t *testing.T) {
	bootstrapCode := os.Getenv("E2E_BOOTSTRAP_CODE")
	if bootstrapCode == "" {
		t.Skip("E2E_BOOTSTRAP_CODE is required for the complete Milestone 2 lifecycle")
	}
	if os.Getenv("E2E_MAIL_FIXTURE_URL") == "" {
		t.Skip("E2E_MAIL_FIXTURE_URL is required for the complete Milestone 2 lifecycle")
	}
	status := request(t, http.MethodGet, "/setup/status", nil, "")
	requireStatus(t, http.StatusOK, status)
	var setup struct {
		Required bool `json:"required"`
	}
	decodeJSON(t, status, &setup)
	if !setup.Required {
		t.Skip("stack already contains an administrator; use a fresh M2 database to test the complete lifecycle")
	}

	adminEmail := uniqueEmail("admin")
	readerEmail := uniqueEmail("reader")
	approvedEmail := uniqueEmail("librarian-approved")
	rejectedEmail := uniqueEmail("librarian-rejected")
	eventEmail := uniqueEmail("librarian-event")
	var approvedSession authSession
	var activeReaderSession authSession

	t.Run("bootstrap singleton admin", func(t *testing.T) {
		invalid := request(t, http.MethodPost, "/setup/admin", map[string]string{
			"name": "E2E Admin", "email": adminEmail, "password": validPassword, "bootstrap_code": bootstrapCode + "-invalid",
		}, "")
		assert.Equal(t, http.StatusUnauthorized, invalid.StatusCode)
		assertNoSessionArtifacts(t, invalid)
		requireSanitizedError(t, invalid)

		created := request(t, http.MethodPost, "/setup/admin", map[string]string{
			"name": "E2E Admin", "email": adminEmail, "password": validPassword, "bootstrap_code": bootstrapCode,
		}, "")
		requireStatus(t, http.StatusCreated, created)
		assertPrivateNoStore(t, created)
		assertNoSessionArtifacts(t, created)
		requireExactBooleanResponse(t, created, "created")

		repeated := request(t, http.MethodPost, "/setup/admin", map[string]string{
			"name": "Second Admin", "email": uniqueEmail("second-admin"), "password": validPassword, "bootstrap_code": bootstrapCode,
		}, "")
		assert.NotEqual(t, http.StatusCreated, repeated.StatusCode)
		assertNoSessionArtifacts(t, repeated)
		requireSanitizedError(t, repeated)
	})

	admin := login(t, adminEmail)
	adminPrincipal := getMe(t, admin.Token)
	assert.Equal(t, "admin", adminPrincipal.Role)
	assert.Equal(t, "active", adminPrincipal.Status)
	assert.Equal(t, adminEmail, adminPrincipal.Email)
	assert.NotEmpty(t, adminPrincipal.UserID)

	t.Run("reader verifies and receives live principal", func(t *testing.T) {
		register(t, "E2E Reader", readerEmail, "reader")

		pendingLogin := request(t, http.MethodPost, "/auth/login", map[string]string{
			"email": readerEmail, "password": validPassword,
		}, "")
		assert.Equal(t, http.StatusUnauthorized, pendingLogin.StatusCode)
		requireSanitizedError(t, pendingLogin)

		verifyEmail(t, readerEmail)
		reader := login(t, readerEmail)
		actual := getMe(t, reader.Token)
		assert.Equal(t, principal{Name: "E2E Reader", Email: readerEmail, Role: "reader", Status: "active", UserID: actual.UserID}, actual)

		forbidden := request(t, http.MethodGet, "/admin/users/pending", nil, reader.Token)
		assert.Equal(t, http.StatusForbidden, forbidden.StatusCode)
		requireSanitizedError(t, forbidden)

		logout := request(t, http.MethodPost, "/auth/logout", nil, reader.Token)
		assert.Contains(t, []int{http.StatusOK, http.StatusNoContent}, logout.StatusCode)
		assertPrivateNoStore(t, logout)
		closeBody(t, logout)

		revoked := request(t, http.MethodGet, "/auth/me", nil, reader.Token)
		assert.Equal(t, http.StatusUnauthorized, revoked.StatusCode)
		requireSanitizedError(t, revoked)
	})

	register(t, "Approved Librarian", approvedEmail, "librarian")
	verifyEmail(t, approvedEmail)
	register(t, "Rejected Librarian", rejectedEmail, "librarian")
	verifyEmail(t, rejectedEmail)

	t.Run("pending login is denied with generic error", func(t *testing.T) {
		resp := request(t, http.MethodPost, "/auth/login", map[string]string{
			"email": approvedEmail, "password": validPassword,
		}, "")
		requireStatus(t, http.StatusUnauthorized, resp)
		requireSanitizedError(t, resp)
	})

	var approvedUserID string
	var rejectedUserID string
	t.Run("pending list uses bounded cursor pagination", func(t *testing.T) {
		oversized := request(t, http.MethodGet, "/admin/users/pending?page_size=101", nil, admin.Token)
		requireStatus(t, http.StatusBadRequest, oversized)
		requireSanitizedError(t, oversized)

		invalidCursor := request(t, http.MethodGet, "/admin/users/pending?page_token=invalid-test-value", nil, admin.Token)
		requireStatus(t, http.StatusBadRequest, invalidCursor)
		requireSanitizedError(t, invalidCursor)

		first := request(t, http.MethodGet, "/admin/users/pending?page_size=1", nil, admin.Token)
		requireStatus(t, http.StatusOK, first)
		assertPrivateNoStore(t, first)
		var page pendingPage
		decodeJSON(t, first, &page)
		require.Len(t, page.Users, 1)
		require.NotEmpty(t, page.NextPageToken)
		assert.NotEmpty(t, page.Users[0].UserID)
		assert.NotEmpty(t, page.Users[0].Name)
		assert.NotEmpty(t, page.Users[0].RegisteredAt)

		seen := map[string]string{page.Users[0].Email: page.Users[0].UserID}
		cursor := page.NextPageToken
		for cursor != "" && len(seen) < 20 {
			path := "/admin/users/pending?page_size=1&page_token=" + url.QueryEscape(cursor)
			next := request(t, http.MethodGet, path, nil, admin.Token)
			requireStatus(t, http.StatusOK, next)
			var nextPage pendingPage
			decodeJSON(t, next, &nextPage)
			for _, user := range nextPage.Users {
				seen[user.Email] = user.UserID
			}
			cursor = nextPage.NextPageToken
		}
		approvedUserID = seen[approvedEmail]
		rejectedUserID = seen[rejectedEmail]
		require.NotEmpty(t, approvedUserID)
		require.NotEmpty(t, rejectedUserID)
	})

	t.Run("admin decisions are body based", func(t *testing.T) {
		unknown := request(t, http.MethodPost, "/admin/users/approve", map[string]string{
			"user_id": approvedUserID, "unexpected": "value",
		}, admin.Token)
		requireStatus(t, http.StatusBadRequest, unknown)
		requireSanitizedError(t, unknown)

		approved := request(t, http.MethodPost, "/admin/users/approve", map[string]string{"user_id": approvedUserID}, admin.Token)
		requireStatus(t, http.StatusNoContent, approved)
		assertPrivateNoStore(t, approved)
		closeBody(t, approved)

		rejected := request(t, http.MethodPost, "/admin/users/reject", map[string]string{"user_id": rejectedUserID}, admin.Token)
		requireStatus(t, http.StatusNoContent, rejected)
		assertPrivateNoStore(t, rejected)
		closeBody(t, rejected)

		approvedSession = login(t, approvedEmail)
		actual := getMe(t, approvedSession.Token)
		assert.Equal(t, "librarian", actual.Role)
		assert.Equal(t, "active", actual.Status)

		t.Run("catalog workflow stays behind Edge and Catalog role policy", func(t *testing.T) {
			uploaded := uploadBook(t, approvedSession.Token)
			requireStatus(t, http.StatusCreated, uploaded)
			assertPrivateNoStore(t, uploaded)
			var created book
			decodeJSON(t, uploaded, &created)
			require.NotEmpty(t, created.ID)
			assert.Equal(t, "E2E Catalog Book", created.Title)
			assert.Equal(t, "pending", created.ProcessingStatus)

			readerEmail := uniqueEmail("catalog-reader")
			register(t, "Catalog Reader", readerEmail, "reader")
			verifyEmail(t, readerEmail)
			readerSession := login(t, readerEmail)
			activeReaderSession = readerSession

			forbidden := uploadBook(t, readerSession.Token)
			requireStatus(t, http.StatusForbidden, forbidden)
			requireSanitizedError(t, forbidden)

			invalidBookID := request(t, http.MethodGet, "/books/not-a-book-id", nil, readerSession.Token)
			requireStatus(t, http.StatusBadRequest, invalidBookID)
			invalidBookIDError := requireSanitizedError(t, invalidBookID)
			assert.Equal(t, "invalid_book_id", invalidBookIDError.Code)
			assert.Equal(t, invalidBookID.Header.Get("X-Request-ID"), invalidBookIDError.RequestID)

			listed := request(t, http.MethodGet, "/books?page_size=1", nil, readerSession.Token)
			requireStatus(t, http.StatusOK, listed)
			assertPrivateNoStore(t, listed)
			var page bookPage
			decodeJSON(t, listed, &page)
			require.NotEmpty(t, page.Books)

			got := request(t, http.MethodGet, "/books/"+url.PathEscape(created.ID), nil, readerSession.Token)
			requireStatus(t, http.StatusOK, got)
			assertPrivateNoStore(t, got)
			var fetched book
			decodeJSON(t, got, &fetched)
			assert.Equal(t, created.ID, fetched.ID)
			assert.Equal(t, created.Title, fetched.Title)
		})

		rejectedLogin := request(t, http.MethodPost, "/auth/login", map[string]string{
			"email": rejectedEmail, "password": validPassword,
		}, "")
		requireStatus(t, http.StatusUnauthorized, rejectedLogin)
		requireSanitizedError(t, rejectedLogin)
	})

	t.Run("SSE emits fixed PII-free hint", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL()+"/admin/events", nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+admin.Token)
		req.Header.Set("Accept", "text/event-stream")

		responseCh := make(chan *http.Response, 1)
		errorCh := make(chan error, 1)
		go func() {
			resp, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				errorCh <- doErr
				return
			}
			responseCh <- resp
		}()
		var stream *http.Response
		select {
		case stream = <-responseCh:
			requireStatus(t, http.StatusOK, stream)
		case err := <-errorCh:
			require.NoError(t, err)
		case <-time.After(time.Second):
			// Some HTTP servers do not flush SSE response headers until the
			// first event. Continue and collect the response after the change.
		}

		register(t, "Event Librarian", eventEmail, "librarian")
		verifyEmail(t, eventEmail)

		if stream == nil {
			select {
			case stream = <-responseCh:
			case err := <-errorCh:
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("SSE connection was not established before its deadline")
			}
		}
		defer closeBody(t, stream)
		requireStatus(t, http.StatusOK, stream)
		assertPrivateNoStore(t, stream)
		assert.Equal(t, "text/event-stream", strings.Split(stream.Header.Get("Content-Type"), ";")[0])

		reader := bufio.NewReader(io.LimitReader(stream.Body, 8<<10))
		var eventName string
		var eventData string
		for eventName == "" || eventData == "" {
			line, readErr := reader.ReadString('\n')
			require.NoError(t, readErr, "SSE stream ended before the expected hint")
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event:") {
				eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			}
			if strings.HasPrefix(line, "data:") {
				eventData = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if eventName != "pending-librarians-changed" {
			t.Error("SSE stream emitted an unexpected event name")
		}
		var data map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(eventData), &data), "SSE data was not valid JSON")
		if len(data) != 1 {
			t.Errorf("SSE data must contain exactly one field; got %d", len(data))
		}
		for key := range data {
			if key != "version" {
				t.Errorf("SSE data contained unexpected field %q", key)
			}
		}
		var version int
		require.NoError(t, json.Unmarshal(data["version"], &version))
		assert.Equal(t, 1, version)
	})

	m5AdminSession := login(t, adminEmail)
	writeM4SessionToken(t, "E2E_M4_ACCESS_TOKEN_OUT", approvedSession.Token)
	writeM4SessionToken(t, "E2E_M4_REFRESH_COOKIE_OUT", approvedSession.RefreshCookie)
	writeM4SessionToken(t, "E2E_M4_REVOCABLE_TOKEN_OUT", admin.Token)
	writeM4SessionToken(t, "E2E_M5_READER_TOKEN_OUT", activeReaderSession.Token)
	writeM4SessionToken(t, "E2E_M5_LIBRARIAN_TOKEN_OUT", approvedSession.Token)
	writeM4SessionToken(t, "E2E_M5_ADMIN_TOKEN_OUT", m5AdminSession.Token)
}

func TestUnknownRouteReturns404(t *testing.T) {
	resp := request(t, http.MethodGet, "/nonexistent", nil, "")
	defer closeBody(t, resp)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
