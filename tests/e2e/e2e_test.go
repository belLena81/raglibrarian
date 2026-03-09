//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var baseURL = func() string {
	if u := os.Getenv("E2E_BASE_URL"); u != "" {
		return u
	}
	return "http://localhost:8080"
}()

func TestHealthz(t *testing.T) {
	resp, err := http.Get(baseURL + "/healthz")
	require.NoError(t, err)
	assert.Equal(t, 200, resp.StatusCode)
}

func TestRegisterAndLogin(t *testing.T) {
	body, _ := json.Marshal(map[string]string{
		"email":    "e2e@example.com",
		"password": "testpass",
		"role":     "reader",
	})

	resp, err := http.Post(baseURL+"/auth/register",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 201, resp.StatusCode)

	var reg map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&reg))
	require.NotEmpty(t, reg["token"])

	// Login returns a fresh token
	resp, err = http.Post(baseURL+"/auth/login",
		"application/json", bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
}
