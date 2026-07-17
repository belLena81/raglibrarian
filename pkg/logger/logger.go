// Package logger provides the fixed, human-readable service log sink.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New returns a logger that writes the same safe single-line format in every
// environment. LOG_LEVEL controls filtering; LOG_ENV intentionally does not
// alter the output contract.
func New(_ string) (*zap.Logger, error) {
	return NewWithWriter(os.Stdout)
}

// NewWithWriter is provided for deterministic formatter tests and local
// embedding. Application code should use New.
func NewWithWriter(writer io.Writer) (*zap.Logger, error) {
	level, err := parseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil {
		return nil, fmt.Errorf("logger: %w", err)
	}
	if writer == nil {
		return nil, fmt.Errorf("logger: writer is required")
	}
	core := &activityCore{level: level, writer: writer}
	return zap.New(core, zap.AddCaller()), nil
}

// Must calls New and panics on invalid process logging configuration.
func Must(service string) *zap.Logger {
	log, err := New(service)
	if err != nil {
		panic(err)
	}
	return log
}

type activityCore struct {
	level  zapcore.Level
	writer io.Writer
	mu     sync.Mutex
}

func (c *activityCore) Enabled(level zapcore.Level) bool { return level >= c.level }

func (c *activityCore) With([]zapcore.Field) zapcore.Core { return c }

func (c *activityCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *activityCore) Write(entry zapcore.Entry, _ []zapcore.Field) error {
	caller := "unknown:0"
	if entry.Caller.Defined {
		caller = filepath.Base(entry.Caller.File) + fmt.Sprintf(":%d", entry.Caller.Line)
	}
	line := fmt.Sprintf("[%s]%s %s : %s\n", entry.Level.String(), entry.Time.UTC().Format("2006-01-02T15:04:05.000Z"), caller, logLine(entry.Message, 4096))
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := io.WriteString(c.writer, line)
	return err
}

func (c *activityCore) Sync() error { return nil }

func parseLevel(raw string) (zapcore.Level, error) {
	if raw == "" {
		return zapcore.InfoLevel, nil
	}
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(raw))); err != nil {
		return level, fmt.Errorf("unrecognised LOG_LEVEL %q (want debug|info|warn|error)", raw)
	}
	return level, nil
}

func logLine(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "?")
	value = strings.Map(func(character rune) rune {
		if unsafeRune(character) {
			return '?'
		}
		return character
	}, value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func unsafeRune(character rune) bool {
	return unicode.IsControl(character) || unicode.Is(unicode.Cf, character) || character == '\u2028' || character == '\u2029'
}
