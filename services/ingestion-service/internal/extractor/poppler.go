// Package extractor runs a sandboxed external PDF text extractor behind a narrow port.
package extractor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

const ExtractionVersion = "poppler-layout-v1"

var (
	ErrSandboxUnavailable   = errors.New("parser sandbox unavailable")
	errIncompletePageStream = errors.New("incomplete page stream")
)

type Page struct {
	Number uint32
	Text   string
}

type DocumentInfo struct{ PageCount uint32 }

type Limits struct {
	MaximumPages          uint32
	MaximumPageBytes      int64
	MaximumExtractedBytes int64
}

type Runner interface {
	Run(context.Context, string, []string, int64) ([]byte, error)
}

type pageStreamer interface {
	StreamPages(context.Context, string, []string, Limits, uint32, func(Page) error) error
}

type categorizedError struct {
	category domain.FailureCategory
	cause    error
}

func (e *categorizedError) Error() string { return string(e.category) }
func (e *categorizedError) Unwrap() error { return e.cause }

// commandError retains bounded command diagnostics for local classification.
// Its Error method deliberately omits stderr so untrusted parser output cannot
// escape through application errors or logs.
type commandError struct {
	cause  error
	stderr []byte
}

func (e *commandError) Error() string { return "extractor command failed" }
func (e *commandError) Unwrap() error { return e.cause }

type consumerError struct{ cause error }

func (e *consumerError) Error() string { return "page consumer failed" }
func (e *consumerError) Unwrap() error { return e.cause }

func IsConsumerError(err error) bool {
	var target *consumerError
	return errors.As(err, &target)
}

func ConsumerCause(err error) (error, bool) {
	var target *consumerError
	if errors.As(err, &target) {
		return target.cause, true
	}
	return nil, false
}

func FailureCategory(err error) (domain.FailureCategory, bool) {
	var target *categorizedError
	if errors.As(err, &target) {
		return target.category, true
	}
	return "", false
}

func FailureDetail(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "extract_timeout", true
	}
	var detailed *detailedError
	if errors.As(err, &detailed) {
		if detail, ok := commandFailureDetail(detailed.cause); ok {
			switch detail {
			case "parser_sandbox_failed", "extract_timeout", "epub_parser_panic", "epub_parser_output_failed",
				"epub_parser_invalid_limits", "epub_parser_invalid_entry_limits", "epub_parser_invalid_configuration",
				"epub_parser_invalid_output", "epub_parser_trailing_output", "epub_parser_invalid_location",
				"epub_parser_xml_nesting", "epub_parser_xhtml_invalid", "epub_parser_xhtml_unsafe",
				"epub_parser_text_limit", "epub_parser_exit_125", "epub_parser_exit_126", "epub_parser_exit_127":
				return detail, true
			case "parser_command_failed":
				return specificCommandFailureDetail(detailed.detail), true
			}
			if detailed.detail != "epub_parser_failed" && strings.HasPrefix(detail, "parser_command_exit_") {
				if diagnostic, ok := commandDiagnosticDetail(detailed.cause); ok {
					return detailed.detail + "_" + diagnostic, true
				}
				return detailed.detail + "_" + detail, true
			}
		}
		if detailed.detail != "epub_parser_failed" {
			if detail, ok := commandDiagnosticDetail(detailed.cause); ok {
				return detailed.detail + "_" + detail, true
			}
		}
		if detailed.detail == "epub_parser_failed" {
			if detail, ok := epubFailureDetail(detailed.cause); ok {
				return detail, true
			}
		}
		return detailed.detail, true
	}
	var target *categorizedError
	if errors.As(err, &target) {
		return commandFailureDetail(target.cause)
	}
	return commandFailureDetail(err)
}

type Poppler struct {
	pdfInfoPath    string
	pdfTextPath    string
	limits         Limits
	runner         Runner
	rawTextDumpDir string
}

func NewPoppler(pdfInfoPath, pdfTextPath string, limits Limits, runner Runner) *Poppler {
	return NewPopplerWithOptions(pdfInfoPath, pdfTextPath, limits, runner, "")
}

func NewPopplerWithOptions(pdfInfoPath, pdfTextPath string, limits Limits, runner Runner, rawTextDumpDir string) *Poppler {
	if runner == nil {
		runner = SandboxedExecRunner{delegate: ExecRunner{}}
	}
	return &Poppler{pdfInfoPath: pdfInfoPath, pdfTextPath: pdfTextPath, limits: limits, runner: runner, rawTextDumpDir: strings.TrimSpace(rawTextDumpDir)}
}

func (p *Poppler) Extract(ctx context.Context, sourcePath string, consume func(Page) error) (DocumentInfo, error) {
	if consume == nil || p.limits.MaximumPages == 0 || p.limits.MaximumPageBytes < 1 || p.limits.MaximumExtractedBytes < 1 {
		return DocumentInfo{}, &categorizedError{category: domain.FailureInternalProcessing, cause: errors.New("invalid extractor configuration")}
	}
	preflight, err := p.runner.Run(ctx, p.pdfInfoPath, []string{sourcePath}, 64<<10)
	if err != nil {
		return DocumentInfo{}, detailedFailure("pdfinfo_failed", classifyCommandError(ctx, err))
	}
	info, err := p.parseInfo(preflight)
	if err != nil {
		return DocumentInfo{}, err
	}
	args := []string{"-layout", "-enc", "UTF-8", sourcePath, "-"}
	if p.rawTextDumpDir != "" {
		output, runErr := p.runner.Run(ctx, p.pdfTextPath, args, p.limits.MaximumExtractedBytes+int64(info.PageCount))
		if runErr != nil {
			return DocumentInfo{}, detailedFailure("pdftotext_failed", classifyCommandError(ctx, runErr))
		}
		if dumpErr := p.dumpRawText(output); dumpErr != nil {
			return DocumentInfo{}, detailedFailure("pdftotext_dump_failed", &categorizedError{category: domain.FailureInternalProcessing, cause: dumpErr})
		}
		if err = p.consumeTextOutput(output, info.PageCount, consume); err != nil {
			return DocumentInfo{}, err
		}
		return info, nil
	}
	if streamer, ok := p.runner.(pageStreamer); ok {
		if err = streamer.StreamPages(ctx, p.pdfTextPath, args, p.limits, info.PageCount, consume); err != nil {
			if IsConsumerError(err) {
				return DocumentInfo{}, err
			}
			return DocumentInfo{}, detailedFailure("pdftotext_failed", classifyStreamError(ctx, err))
		}
		return info, nil
	}
	output, err := p.runner.Run(ctx, p.pdfTextPath, args, p.limits.MaximumExtractedBytes+int64(info.PageCount))
	if err != nil {
		return DocumentInfo{}, detailedFailure("pdftotext_failed", classifyCommandError(ctx, err))
	}
	if err = p.consumeTextOutput(output, info.PageCount, consume); err != nil {
		return DocumentInfo{}, err
	}
	return info, nil
}

func (p *Poppler) dumpRawText(output []byte) error {
	if p.rawTextDumpDir == "" {
		return nil
	}
	if err := ensureRawTextDumpDirectory(p.rawTextDumpDir); err != nil {
		return err
	}
	file, err := os.CreateTemp(p.rawTextDumpDir, "pdftotext-*.txt")
	if err != nil {
		return err
	}
	path := file.Name()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		_ = file.Close()
		return chmodErr
	}
	if _, err = file.Write(output); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	sum := sha256.Sum256(output)
	_, _ = fmt.Fprintf(os.Stderr, "ingestion raw pdftotext dump path=%s bytes=%d sha256=%s\n", path, len(output), hex.EncodeToString(sum[:]))
	return nil
}

func ensureRawTextDumpDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.MkdirAll(path, 0o700); mkdirErr != nil {
			return mkdirErr
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("raw text dump directory is a symlink")
	}
	if !info.IsDir() {
		return errors.New("raw text dump path is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("raw text dump directory is not private")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("raw text dump directory owner mismatch")
	}
	return nil
}

func (p *Poppler) consumeTextOutput(output []byte, expectedPages uint32, consume func(Page) error) error {
	if int64(len(output)) > p.limits.MaximumExtractedBytes+int64(expectedPages) {
		return &categorizedError{category: domain.FailureResourceLimitExceeded}
	}
	parts := bytes.Split(output, []byte{'\f'})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	extractedPages := uint32(len(parts)) // #nosec G115 -- bounded by configured page count and output size.
	if extractedPages != expectedPages {
		return &categorizedError{category: domain.FailureMalformedDocument}
	}
	var extracted int64
	for index, content := range parts {
		if int64(len(content)) > p.limits.MaximumPageBytes {
			return &categorizedError{category: domain.FailureResourceLimitExceeded}
		}
		extracted += int64(len(content))
		if extracted > p.limits.MaximumExtractedBytes {
			return &categorizedError{category: domain.FailureResourceLimitExceeded}
		}
		if consumeErr := consume(Page{Number: uint32(index + 1), Text: string(content)}); consumeErr != nil {
			return consumeErr
		}
	}
	return nil
}

func classifyStreamError(ctx context.Context, err error) error {
	var categorized *categorizedError
	if errors.As(err, &categorized) {
		return err
	}
	return classifyCommandError(ctx, err)
}

type detailedError struct {
	detail string
	cause  error
}

func (e *detailedError) Error() string { return e.detail }
func (e *detailedError) Unwrap() error { return e.cause }

func detailedFailure(detail string, err error) error {
	if err == nil {
		return nil
	}
	return &detailedError{detail: detail, cause: err}
}

func (p *Poppler) parseInfo(output []byte) (DocumentInfo, error) {
	text := string(output)
	var pages uint64
	for _, line := range strings.Split(text, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.ToLower(strings.TrimSpace(value))
		if name == "encrypted" && strings.HasPrefix(value, "yes") {
			return DocumentInfo{}, &categorizedError{category: domain.FailureEncryptedDocument}
		}
		if name == "copy" && strings.HasPrefix(value, "no") {
			return DocumentInfo{}, &categorizedError{category: domain.FailureExtractionNotPermitted}
		}
		if name == "pages" {
			parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 32)
			if err != nil {
				return DocumentInfo{}, &categorizedError{category: domain.FailureMalformedDocument}
			}
			pages = parsed
		}
	}
	if pages == 0 {
		return DocumentInfo{}, &categorizedError{category: domain.FailureMalformedDocument}
	}
	if pages > uint64(p.limits.MaximumPages) {
		return DocumentInfo{}, detailedFailure("pdf_page_limit_exceeded", &categorizedError{category: domain.FailureResourceLimitExceeded})
	}
	return DocumentInfo{PageCount: uint32(pages)}, nil
}

func classifyCommandError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &categorizedError{category: domain.FailureProcessingTimeout, cause: ctx.Err()}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &categorizedError{category: domain.FailureInternalProcessing, cause: ctx.Err()}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return &categorizedError{category: domain.FailureDependencyUnavailable, cause: err}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 1:
			if hasIncorrectPasswordDiagnostic(err) {
				return &categorizedError{category: domain.FailureEncryptedDocument, cause: err}
			}
			return &categorizedError{category: domain.FailureMalformedDocument, cause: err}
		case 3:
			return &categorizedError{category: domain.FailureExtractionNotPermitted, cause: err}
		case 121:
			return &categorizedError{category: domain.FailureResourceLimitExceeded, cause: err}
		case 122, 123, 124:
			return &categorizedError{category: domain.FailureDependencyUnavailable, cause: err}
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && (status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGXCPU) {
			return &categorizedError{category: domain.FailureResourceLimitExceeded, cause: err}
		}
	}
	return &categorizedError{category: domain.FailureInternalProcessing, cause: err}
}

func commandFailureDetail(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var commandErr *commandError
	if errors.As(err, &commandErr) {
		if detail, ok := commandStderrFailureDetail(commandErr.stderr); ok {
			return detail, true
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "extract_timeout", true
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "parser_sandbox_failed", true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 2:
			return "epub_parser_invalid_args", true
		case 122, 123:
			return "parser_sandbox_failed", true
		case 124:
			return "parser_command_failed", true
		case 125, 126, 127:
			return commandExitDetail(exitErr.ExitCode()), true
		}
		if exitErr.ExitCode() > 0 {
			return fmt.Sprintf("parser_command_exit_%d", exitErr.ExitCode()), true
		}
	}
	return "", false
}

func commandStderrFailureDetail(stderr []byte) (string, bool) {
	switch {
	case bytes.Contains(stderr, []byte("epub_parser_panic")):
		return "epub_parser_panic", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_args")):
		return "epub_parser_invalid_args", true
	case bytes.Contains(stderr, []byte("epub_parser_output_failed")):
		return "epub_parser_output_failed", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_limits")):
		return "epub_parser_invalid_limits", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_entry_limits")):
		return "epub_parser_invalid_entry_limits", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_configuration")):
		return "epub_parser_invalid_configuration", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_output")):
		return "epub_parser_invalid_output", true
	case bytes.Contains(stderr, []byte("epub_parser_trailing_output")):
		return "epub_parser_trailing_output", true
	case bytes.Contains(stderr, []byte("epub_parser_invalid_location")):
		return "epub_parser_invalid_location", true
	case bytes.Contains(stderr, []byte("epub_parser_xml_nesting")):
		return "epub_parser_xml_nesting", true
	case bytes.Contains(stderr, []byte("epub_parser_xhtml_invalid")):
		return "epub_parser_xhtml_invalid", true
	case bytes.Contains(stderr, []byte("epub_parser_xhtml_unsafe")):
		return "epub_parser_xhtml_unsafe", true
	case bytes.Contains(stderr, []byte("epub_parser_text_limit")):
		return "epub_parser_text_limit", true
	default:
		return "", false
	}
}

func commandDiagnosticDetail(err error) (string, bool) {
	var commandErr *commandError
	if !errors.As(err, &commandErr) {
		return "", false
	}
	parts := []string{"parser_command_failed"}
	var exitErr *exec.ExitError
	switch {
	case errors.As(commandErr.cause, &exitErr):
		if exitErr.ExitCode() > 0 {
			parts = append(parts, fmt.Sprintf("exit_%d", exitErr.ExitCode()))
		}
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			parts = append(parts, fmt.Sprintf("signal_%d", status.Signal()))
		}
	case errors.Is(commandErr.cause, exec.ErrNotFound):
		parts = append(parts, "exec_not_found")
	default:
		parts = append(parts, "unknown")
	}
	if len(commandErr.stderr) > 0 {
		sum := sha256.Sum256(commandErr.stderr)
		parts = append(parts, fmt.Sprintf("stderr_bytes_%d", len(commandErr.stderr)))
		parts = append(parts, "stderr_sha256_"+hex.EncodeToString(sum[:])[:16])
	}
	return strings.Join(parts, "_"), true
}

func commandExitDetail(code int) string {
	return fmt.Sprintf("epub_parser_exit_%d", code)
}

func specificCommandFailureDetail(detail string) string {
	switch detail {
	case "pdfinfo_failed":
		return "pdfinfo_exec_failed"
	case "pdftotext_failed":
		return "pdftotext_exec_failed"
	case "epub_parser_failed":
		return "epub_parser_exec_failed"
	default:
		return detail
	}
}

func epubFailureDetail(err error) (string, bool) {
	var categorized *categorizedError
	if !errors.As(err, &categorized) {
		return "", false
	}
	switch categorized.category {
	case domain.FailureMalformedDocument:
		return "epub_parser_malformed", true
	case domain.FailureResourceLimitExceeded:
		return "epub_parser_resource_limit", true
	case domain.FailureInternalProcessing:
		if detail, ok := commandFailureDetail(categorized.cause); ok {
			switch detail {
			case "epub_parser_panic", "epub_parser_output_failed",
				"epub_parser_invalid_limits", "epub_parser_invalid_entry_limits", "epub_parser_invalid_configuration",
				"epub_parser_invalid_output", "epub_parser_trailing_output", "epub_parser_invalid_location",
				"epub_parser_xml_nesting", "epub_parser_xhtml_invalid", "epub_parser_xhtml_unsafe",
				"epub_parser_text_limit", "epub_parser_exit_125", "epub_parser_exit_126", "epub_parser_exit_127":
				return detail, true
			}
		}
		return internalEPUBFailureDetail(categorized.cause), true
	default:
		return "", false
	}
}

func hasIncorrectPasswordDiagnostic(err error) bool {
	var commandErr *commandError
	return errors.As(err, &commandErr) && bytes.Contains(commandErr.stderr, []byte("Incorrect password"))
}

type ExecRunner struct{}

func VerifySandbox(ctx context.Context) error {
	return verifySandbox(ctx, ExecRunner{})
}

func verifySandbox(ctx context.Context, runner Runner) error {
	if _, err := runner.Run(ctx, parserSandboxPath(), []string{"--landlock-preflight"}, 1); err != nil {
		return ErrSandboxUnavailable
	}
	return nil
}

// SandboxedExecRunner runs the untrusted parser without network access and
// with hard per-process resource limits. Failure to create the sandbox fails
// closed; the worker never falls back to executing Poppler directly.
type SandboxedExecRunner struct{ delegate ExecRunner }

func (runner SandboxedExecRunner) Run(ctx context.Context, path string, args []string, maximumOutput int64) ([]byte, error) {
	sandboxPath, sandboxArgs := sandboxCommand(path, args)
	return runner.delegate.Run(ctx, sandboxPath, sandboxArgs, maximumOutput)
}

func (runner SandboxedExecRunner) StreamPages(ctx context.Context, path string, args []string, limits Limits, expectedPages uint32, consume func(Page) error) error {
	sandboxPath, sandboxArgs := sandboxCommand(path, args)
	return runner.delegate.StreamPages(ctx, sandboxPath, sandboxArgs, limits, expectedPages, consume)
}

func sandboxCommand(path string, args []string) (string, []string) {
	return parserSandboxPath(), append([]string{path}, args...)
}

func parserSandboxPath() string {
	if value := strings.TrimSpace(os.Getenv("PARSER_SANDBOX_PATH")); value != "" {
		return value
	}
	return "/parser-sandbox"
}

func (ExecRunner) StreamPages(ctx context.Context, path string, args []string, limits Limits, expectedPages uint32, consume func(Page) error) error {
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- fixed executable and trusted argv.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr boundedBuffer
	stderr.maximum = 8 << 10
	command.Stderr = &stderr
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err = command.Start(); err != nil {
		return err
	}
	streamErr := consumePageStream(stdout, limits, expectedPages, consume)
	if streamErr != nil {
		if errors.Is(streamErr, errIncompletePageStream) {
			waitErr := command.Wait()
			if waitErr != nil {
				traceCommandFailure(path, args, stderr.Bytes(), waitErr)
				return newCommandError(waitErr, stderr.Bytes())
			}
			return streamErr
		}
		_ = command.Cancel()
		_ = command.Wait()
		return streamErr
	}
	if err = command.Wait(); err != nil {
		traceCommandFailure(path, args, stderr.Bytes(), err)
		return newCommandError(err, stderr.Bytes())
	}
	return nil
}

func consumePageStream(input io.Reader, limits Limits, expectedPages uint32, consume func(Page) error) error {
	reader := bufio.NewReaderSize(input, 64<<10)
	page := make([]byte, 0, min(int(limits.MaximumPageBytes), 256<<10))
	var total int64
	var pageNumber uint32
	for {
		fragment, readErr := reader.ReadSlice('\f')
		terminated := len(fragment) > 0 && fragment[len(fragment)-1] == '\f'
		if terminated {
			fragment = fragment[:len(fragment)-1]
		}
		total += int64(len(fragment))
		if total > limits.MaximumExtractedBytes || int64(len(page)+len(fragment)) > limits.MaximumPageBytes {
			return &categorizedError{category: domain.FailureResourceLimitExceeded}
		}
		page = append(page, fragment...)
		if terminated || (errors.Is(readErr, io.EOF) && len(page) > 0) {
			pageNumber++
			if pageNumber > expectedPages {
				return &categorizedError{category: domain.FailureMalformedDocument}
			}
			if consumeErr := consume(Page{Number: pageNumber, Text: string(page)}); consumeErr != nil {
				return &consumerError{cause: consumeErr}
			}
			page = page[:0]
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if pageNumber != expectedPages {
		return &categorizedError{category: domain.FailureMalformedDocument, cause: errIncompletePageStream}
	}
	return nil
}

func (ExecRunner) Run(ctx context.Context, path string, args []string, maximumOutput int64) ([]byte, error) {
	if path == "" || maximumOutput < 1 {
		return nil, errors.New("invalid command")
	}
	command := exec.CommandContext(ctx, path, args...) // #nosec G204 -- fixed executable and argv supplied by trusted configuration.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout boundedBuffer
	stdout.maximum = maximumOutput
	command.Stdout = &stdout
	var stderr boundedBuffer
	stderr.maximum = 8 << 10
	command.Stderr = &stderr
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	if err := command.Run(); err != nil {
		traceCommandFailure(path, args, stderr.Bytes(), err)
		return nil, newCommandError(err, stderr.Bytes())
	}
	if stdout.exceeded {
		return stdout.Bytes(), &categorizedError{category: domain.FailureResourceLimitExceeded}
	}
	return stdout.Bytes(), nil
}

func traceCommandFailure(path string, args []string, stderr []byte, err error) {
	if os.Getenv("INGESTION_COMMAND_FAILURE_TRACE") == "" {
		return
	}
	if !traceableCommandFailure(path, stderr, err) {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "ingestion command failure trace")
	_, _ = fmt.Fprintf(os.Stderr, "command=%s argc=%d stderr_detail=%s\n", filepathBase(path), len(args), traceCommandFailureDetail(stderr, err))
	if len(stderr) > 0 {
		_, _ = fmt.Fprintln(os.Stderr, "command stderr begin")
		_, _ = os.Stderr.Write(boundTrace(stderr, 4<<10))
		if len(stderr) > 4<<10 {
			_, _ = fmt.Fprintln(os.Stderr, "command stderr truncated")
		}
		_, _ = fmt.Fprintln(os.Stderr, "command stderr end")
	}
	_, _ = os.Stderr.Write(boundStackTrace(debug.Stack(), 4<<10))
}

func traceableCommandFailure(path string, stderr []byte, err error) bool {
	if path == "" || err == nil {
		return false
	}
	if bytes.Contains(stderr, []byte("epub_parser_invalid_args")) || bytes.Contains(stderr, []byte("epub_parser_panic")) {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
		return true
	}
	return false
}

func traceCommandFailureDetail(stderr []byte, err error) string {
	switch {
	case bytes.Contains(stderr, []byte("epub_parser_invalid_args")):
		return "epub_parser_invalid_args"
	case bytes.Contains(stderr, []byte("epub_parser_panic")):
		return "epub_parser_panic"
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Sprintf("exit_%d", exitErr.ExitCode())
	}
	return "unknown"
}

func boundStackTrace(stack []byte, maximum int) []byte {
	if len(stack) <= maximum {
		return stack
	}
	return append(stack[:maximum], '\n')
}

func boundTrace(value []byte, maximum int) []byte {
	if len(value) <= maximum {
		return value
	}
	return append(value[:maximum], '\n')
}

func filepathBase(path string) string {
	if path == "" {
		return ""
	}
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' {
			return path[index+1:]
		}
	}
	return path
}

func newCommandError(cause error, stderr []byte) error {
	return &commandError{cause: cause, stderr: append([]byte(nil), stderr...)}
}

type boundedBuffer struct {
	bytes.Buffer
	maximum  int64
	exceeded bool
}

func (w *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := w.maximum - int64(w.Len())
	if remaining <= 0 {
		w.exceeded = true
		return original, nil
	}
	if int64(len(value)) > remaining {
		value = value[:remaining]
		w.exceeded = true
	}
	_, _ = w.Buffer.Write(value)
	return original, nil
}
