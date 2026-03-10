// Package logger provides a zap constructor for all raglibrarian services.
// LOG_ENV=production selects JSON output; anything else uses a coloured console encoder.
// LOG_LEVEL overrides the log level independently of the environment.
package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New returns a *zap.Logger named with the given service label.
// Reads LOG_ENV and LOG_LEVEL from the environment.
func New(service string) (*zap.Logger, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}

	var cfg zap.Config

	if strings.ToLower(os.Getenv("LOG_ENV")) == "production" {
		cfg = zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(level)
		cfg.Sampling = nil
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.Level = zap.NewAtomicLevelAt(level)
	}

	base, err := cfg.Build(zap.AddCallerSkip(0))
	base = base.With(
		zap.String("service", service),
		zap.String("env", os.Getenv("LOG_ENV")),
	)
	if err != nil {
		return nil, fmt.Errorf("logger: build failed: %w", err)
	}

	return base.Named(service), nil
}

// Must calls New and panics on error. Intended for use in main().
func Must(service string) *zap.Logger {
	log, err := New(service)
	if err != nil {
		panic(err)
	}
	return log
}

func parseLevel(raw string) (zapcore.Level, error) {
	if raw == "" {
		return zapcore.DebugLevel, nil
	}

	var l zapcore.Level
	if err := l.UnmarshalText([]byte(strings.ToLower(raw))); err != nil {
		return l, fmt.Errorf("unrecognised LOG_LEVEL %q (want debug|info|warn|error)", raw)
	}
	return l, nil
}
