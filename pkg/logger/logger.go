// Package logger provides a shared zap constructor for all raglibrarian services.
// It is the only place in the codebase that imports go.uber.org/zap directly for
// building the logger. All other packages receive a *zap.Logger via constructor
// injection — they never call zap.NewProduction() themselves.
//
// Environment behaviour:
//   - LOG_ENV=production  →  JSON encoder, Info level, no caller info in hot paths
//   - anything else        →  console encoder, Debug level, caller shown (dev mode)
//
// LOG_LEVEL overrides the level independently of the environment.
package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New builds and returns a *zap.Logger configured from environment variables.
//
// Recognised env vars:
//
//	LOG_ENV   "production" | anything else (default: development)
//	LOG_LEVEL "debug" | "info" | "warn" | "error" (default: info in prod, debug in dev)
//
// The returned logger is already named with the service label so every log line
// carries a "service" field without callers having to add it manually.
func New(service string) (*zap.Logger, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}

	var cfg zap.Config

	if strings.ToLower(os.Getenv("LOG_ENV")) == "production" {
		cfg = zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(level)
		// Disable sampling in production for a library API — every error matters.
		cfg.Sampling = nil
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
		cfg.Level = zap.NewAtomicLevelAt(level)
	}

	base, err := cfg.Build(
		// Skip one extra frame so caller info points at the call site,
		// not at this constructor.
		zap.AddCallerSkip(0),
	)
	if err != nil {
		return nil, fmt.Errorf("logger: build failed: %w", err)
	}

	return base.Named(service), nil
}

// Must is a convenience wrapper for use in main(). It panics if New returns an
// error — acceptable at process start, never acceptable at request time.
func Must(service string) *zap.Logger {
	log, err := New(service)
	if err != nil {
		panic(err)
	}
	return log
}

// parseLevel maps a string to a zapcore.Level.
// An empty string returns the caller's chosen default rather than an error.
func parseLevel(raw string) (zapcore.Level, error) {
	if raw == "" {
		// Resolve the default based on LOG_ENV, not here — callers set cfg.Level
		// themselves, so we return a sentinel that they replace.
		return zapcore.DebugLevel, nil
	}

	var l zapcore.Level
	if err := l.UnmarshalText([]byte(strings.ToLower(raw))); err != nil {
		return l, fmt.Errorf("unrecognised LOG_LEVEL %q (want debug|info|warn|error)", raw)
	}
	return l, nil
}
