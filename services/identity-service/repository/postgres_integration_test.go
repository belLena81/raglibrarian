//go:build integration

package repository

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("IDENTITY_POSTGRES_DSN")
	if path := os.Getenv("IDENTITY_POSTGRES_DSN_FILE"); path != "" {
		contents, err := os.ReadFile(path) // #nosec G304 -- test-only configured runtime-role secret path.
		require.NoError(t, err)
		dsn = strings.TrimSpace(string(contents))
	}
	return dsn
}
