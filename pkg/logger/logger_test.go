package logger_test

import (
	"bytes"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/belLena81/raglibrarian/pkg/logger"
)

func TestNew_DevelopmentMode(t *testing.T) {
	os.Unsetenv("LOG_ENV")
	os.Unsetenv("LOG_LEVEL")

	log, err := logger.New("test-service")

	require.NoError(t, err)
	assert.NotNil(t, log)
}

func TestNewWithWriterRetainsOnlySafeFieldsInSortedOrder(t *testing.T) {
	var output bytes.Buffer
	log, err := logger.NewWithWriter(&output)
	require.NoError(t, err)

	log.With(
		zap.String("request_id", "0123456789abcdef0123456789abcdef"),
		zap.String("ignored", "not-recorded"),
	).Info("request completed",
		zap.Int("status", 201),
		zap.String("route", "/books/{bookID}"),
		zap.String("error_code", "person@example.test"),
		zap.Error(assert.AnError),
	)

	line := output.String()
	assert.Contains(t, line, " request_id=0123456789abcdef0123456789abcdef route=/books/{bookID} status=201")
	assert.NotContains(t, line, "person@example.test")
	assert.NotContains(t, line, "not-recorded")
	assert.NotContains(t, line, "assert.AnError")
}

func TestNewWithWriterAllowsOnlyKnownPasswordResetCleanupStage(t *testing.T) {
	var output bytes.Buffer
	log, err := logger.NewWithWriter(&output)
	require.NoError(t, err)

	log.Warn("worker.operation.failed", zap.String("stage", "password_reset_cleanup"))
	assert.Contains(t, output.String(), "worker operation failed stage=password_reset_cleanup")

	output.Reset()
	log.Warn("worker.operation.failed", zap.String("stage", "future_cleanup"))
	assert.Contains(t, output.String(), "worker operation failed")
	assert.NotContains(t, output.String(), "stage=")
}

func TestNewWithWriterUsesFixedSingleLineFormat(t *testing.T) {
	var output bytes.Buffer
	log, err := logger.NewWithWriter(&output)
	require.NoError(t, err)
	log.Info("upload accepted\nwithout a second line")
	assert.Regexp(t, regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z INFO  logger_test\.go:\d+: upload accepted\?without a second line\n$`), output.String())
}

func TestNewWithWriterReplacesTerminalControls(t *testing.T) {
	var output bytes.Buffer
	log, err := logger.NewWithWriter(&output)
	require.NoError(t, err)
	log.Info("unsafe\x1b[31m\x00\u2028\u2029")
	assert.NotContains(t, output.String(), "\x1b")
	assert.NotContains(t, output.String(), "\x00")
	assert.NotContains(t, output.String(), "\u2028")
	assert.NotContains(t, output.String(), "\u2029")
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
