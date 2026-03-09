package logger_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/belLena81/raglibrarian/pkg/logger"
)

func TestNew_DevelopmentMode(t *testing.T) {
	os.Unsetenv("LOG_ENV")
	os.Unsetenv("LOG_LEVEL")

	log, err := logger.New("test-service")

	require.NoError(t, err)
	assert.NotNil(t, log)
}

func TestNew_ProductionMode(t *testing.T) {
	t.Setenv("LOG_ENV", "production")
	os.Unsetenv("LOG_LEVEL")

	log, err := logger.New("test-service")

	require.NoError(t, err)
	assert.NotNil(t, log)
}

func TestNew_ExplicitLogLevel_Info(t *testing.T) {
	t.Setenv("LOG_LEVEL", "info")

	log, err := logger.New("test-service")

	require.NoError(t, err)
	assert.NotNil(t, log)
}

func TestNew_ExplicitLogLevel_Warn(t *testing.T) {
	t.Setenv("LOG_LEVEL", "warn")

	log, err := logger.New("test-service")

	require.NoError(t, err)
	assert.NotNil(t, log)
}

func TestNew_InvalidLogLevel_ReturnsError(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbosely-extreme")

	_, err := logger.New("test-service")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "LOG_LEVEL")
}

func TestMust_ValidConfig_DoesNotPanic(t *testing.T) {
	os.Unsetenv("LOG_ENV")
	os.Unsetenv("LOG_LEVEL")

	assert.NotPanics(t, func() {
		log := logger.Must("test-service")
		assert.NotNil(t, log)
	})
}

func TestMust_InvalidConfig_Panics(t *testing.T) {
	t.Setenv("LOG_LEVEL", "not-a-level")

	assert.Panics(t, func() {
		logger.Must("test-service")
	})
}
