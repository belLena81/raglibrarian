// Package logger provides the fixed, human-readable service log sink.
package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	core := &activityCore{level: level, writer: writer, mu: &sync.Mutex{}}
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
	fields []zapcore.Field
	mu     *sync.Mutex
}

func (c *activityCore) Enabled(level zapcore.Level) bool { return level >= c.level }

func (c *activityCore) With(fields []zapcore.Field) zapcore.Core {
	clone := *c
	clone.fields = append(append([]zapcore.Field(nil), c.fields...), fields...)
	return &clone
}

func (c *activityCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c *activityCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	caller := "unknown:0"
	if entry.Caller.Defined {
		caller = filepath.Base(entry.Caller.File) + fmt.Sprintf(":%d", entry.Caller.Line)
	}
	message := logLine(entry.Message, 4096)
	if suffix := safeFieldSuffix(append(append([]zapcore.Field(nil), c.fields...), fields...)); suffix != "" {
		message += suffix
	}
	line := fmt.Sprintf("%s %-5s %s: %s\n", entry.Time.UTC().Format("2006-01-02T15:04:05.000Z"), strings.ToUpper(entry.Level.String()), caller, humanizeMessage(message))
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := io.WriteString(c.writer, line)
	return err
}

func humanizeMessage(message string) string {
	return strings.ReplaceAll(message, ".", " ")
}

var allowedFieldNames = map[string]struct{}{
	"request_id": {}, "method": {}, "route": {}, "route_template": {}, "status": {}, "outcome": {}, "duration": {}, "duration_ms": {},
	"response_bytes": {}, "operation": {}, "code": {}, "grpc_code": {}, "stage": {}, "reason": {}, "reason_code": {}, "error_code": {},
	"stack_fingerprint": {}, "actor_id": {}, "book_id": {}, "checksum_sha256": {}, "byte_size": {}, "tag_count": {}, "page_size": {}, "result_count": {}, "role": {}, "account_status": {},
}

func safeFieldSuffix(fields []zapcore.Field) string {
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if _, allowed := allowedFieldNames[field.Key]; !allowed {
			continue
		}
		value, ok := safeFieldValue(field)
		if ok {
			values[field.Key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result strings.Builder
	for _, key := range keys {
		result.WriteByte(' ')
		result.WriteString(key)
		result.WriteByte('=')
		result.WriteString(values[key])
	}
	return result.String()
}

func safeFieldValue(field zapcore.Field) (string, bool) {
	var value string
	switch field.Type {
	case zapcore.StringType:
		value = field.String
	case zapcore.Int64Type, zapcore.Int32Type, zapcore.Int16Type, zapcore.Int8Type:
		value = strconv.FormatInt(field.Integer, 10)
	case zapcore.Uint64Type, zapcore.Uint32Type, zapcore.Uint16Type, zapcore.Uint8Type:
		value = strconv.FormatUint(uint64(field.Integer), 10)
	default:
		return "", false
	}
	if !validDiagnosticField(field.Key, field.Type, value) {
		return "", false
	}
	return value, true
}

var (
	requestIDPattern   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	routePattern       = regexp.MustCompile(`^/[A-Za-z0-9_{}./-]{1,160}$`)
	fingerprintPattern = regexp.MustCompile(`^[a-f0-9]{16,128}$`)
	opaqueIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	checksumPattern    = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validDiagnosticField(key string, fieldType zapcore.FieldType, value string) bool {
	if len(value) > 256 || value == "" || strings.ContainsAny(value, " =\t\r\n") {
		return false
	}
	switch key {
	case "request_id":
		return fieldType == zapcore.StringType && requestIDPattern.MatchString(value)
	case "method":
		return fieldType == zapcore.StringType && (value == "GET" || value == "POST" || value == "PUT" || value == "PATCH" || value == "DELETE" || value == "HEAD" || value == "OPTIONS")
	case "route", "route_template":
		return fieldType == zapcore.StringType && routePattern.MatchString(value)
	case "status":
		return integerField(fieldType) && parseBoundedInt(value, 100, 599)
	case "duration", "duration_ms", "response_bytes":
		return integerField(fieldType) && parseBoundedInt(value, 0, 1<<53-1)
	case "stack_fingerprint":
		return fieldType == zapcore.StringType && fingerprintPattern.MatchString(value)
	case "actor_id", "book_id":
		return fieldType == zapcore.StringType && opaqueIDPattern.MatchString(value)
	case "checksum_sha256":
		return fieldType == zapcore.StringType && checksumPattern.MatchString(value)
	case "byte_size", "tag_count", "page_size", "result_count":
		return integerField(fieldType) && parseBoundedInt(value, 0, 1<<53-1)
	case "role":
		return fieldType == zapcore.StringType && (value == "admin" || value == "librarian" || value == "reader")
	case "account_status":
		return fieldType == zapcore.StringType && (value == "active" || value == "pending" || value == "rejected")
	case "outcome", "operation", "stage", "reason", "reason_code", "error_code":
		return fieldType == zapcore.StringType && allowedDiagnosticValue(key, value)
	case "code", "grpc_code":
		return fieldType == zapcore.StringType && validGRPCCode(value)
	default:
		return false
	}
}

var allowedDiagnosticValues = map[string]map[string]struct{}{
	"outcome": {
		"success": {}, "client_error": {}, "server_error": {}, "response_aborted": {}, "not_implemented": {}, "invalid_token": {},
		"invalid_registration": {}, "invalid_credentials": {}, "dependency_unavailable": {},
	},
	"operation": {
		"register": {}, "verify_email": {}, "resend_verification": {}, "password_reset_request": {}, "password_reset_verify": {}, "password_reset_complete": {},
		"login": {}, "refresh": {}, "logout": {}, "validate_session": {}, "get_setup_status": {}, "create_admin": {}, "list_pending_librarians": {},
		"approve_librarian": {}, "reject_librarian": {}, "watch_pending_librarians": {},
		"upload_book": {}, "list_books": {}, "get_book": {},
	},
	"stage": {
		"session_cleanup": {}, "verification_cleanup": {}, "rejected_cleanup": {}, "email_claim": {}, "email_mark": {}, "email_retry": {}, "email_exhausted": {},
	},
	"reason": {},
	"reason_code": {
		"unknown_failure": {}, "config_required_missing": {}, "config_verify_key_invalid": {}, "config_trusted_proxy_cidrs_invalid": {}, "config_refresh_cookie_policy_invalid": {},
		"config_run_as_identity_invalid": {}, "token_verifier_initialization_failed": {}, "internal_tls_files_unreadable": {}, "internal_tls_material_invalid": {},
		"privilege_drop_failed": {}, "identity_client_initialization_failed": {}, "http_listen_failed": {}, "http_serve_failed": {}, "http_shutdown_failed": {},
		"invalid_metadata": {}, "unauthorized_actor": {}, "invalid_pdf": {}, "invalid_stream": {}, "upload_too_large": {}, "upload_capacity_exhausted": {}, "object_storage_unavailable": {}, "object_receipt_mismatch": {}, "persistence_unavailable": {}, "request_cancelled": {}, "not_found": {}, "invalid_pagination": {},
	},
	"error_code": {
		"request_id_generation_failed": {}, "internal_panic": {},
	},
}

func allowedDiagnosticValue(key, value string) bool {
	_, allowed := allowedDiagnosticValues[key][value]
	return allowed
}

func integerField(fieldType zapcore.FieldType) bool {
	return fieldType == zapcore.Int64Type || fieldType == zapcore.Int32Type || fieldType == zapcore.Int16Type || fieldType == zapcore.Int8Type ||
		fieldType == zapcore.Uint64Type || fieldType == zapcore.Uint32Type || fieldType == zapcore.Uint16Type || fieldType == zapcore.Uint8Type
}

func parseBoundedInt(value string, minimum, maximum int64) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed >= minimum && parsed <= maximum
}

func validGRPCCode(value string) bool {
	switch value {
	case "OK", "Canceled", "Unknown", "InvalidArgument", "DeadlineExceeded", "NotFound", "AlreadyExists", "PermissionDenied", "ResourceExhausted", "FailedPrecondition", "Aborted", "OutOfRange", "Unimplemented", "Internal", "Unavailable", "DataLoss", "Unauthenticated":
		return true
	default:
		return false
	}
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
