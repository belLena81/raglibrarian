// Package extractor runs a sandboxed external PDF text extractor behind a narrow port.
package extractor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
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

func FailureCategory(err error) (domain.FailureCategory, bool) {
	var target *categorizedError
	if errors.As(err, &target) {
		return target.category, true
	}
	return "", false
}

type Poppler struct {
	pdfInfoPath string
	pdfTextPath string
	limits      Limits
	runner      Runner
}

func NewPoppler(pdfInfoPath, pdfTextPath string, limits Limits, runner Runner) *Poppler {
	if runner == nil {
		runner = SandboxedExecRunner{delegate: ExecRunner{}}
	}
	return &Poppler{pdfInfoPath: pdfInfoPath, pdfTextPath: pdfTextPath, limits: limits, runner: runner}
}

func (p *Poppler) Extract(ctx context.Context, sourcePath string, consume func(Page) error) (DocumentInfo, error) {
	if consume == nil || p.limits.MaximumPages == 0 || p.limits.MaximumPageBytes < 1 || p.limits.MaximumExtractedBytes < 1 {
		return DocumentInfo{}, &categorizedError{category: domain.FailureInternalProcessing, cause: errors.New("invalid extractor configuration")}
	}
	preflight, err := p.runner.Run(ctx, p.pdfInfoPath, []string{sourcePath}, 64<<10)
	if err != nil {
		return DocumentInfo{}, classifyCommandError(ctx, err)
	}
	info, err := p.parseInfo(preflight)
	if err != nil {
		return DocumentInfo{}, err
	}
	args := []string{"-layout", "-enc", "UTF-8", sourcePath, "-"}
	if streamer, ok := p.runner.(pageStreamer); ok {
		if err = streamer.StreamPages(ctx, p.pdfTextPath, args, p.limits, info.PageCount, consume); err != nil {
			return DocumentInfo{}, classifyStreamError(ctx, err)
		}
		return info, nil
	}
	output, err := p.runner.Run(ctx, p.pdfTextPath, args, p.limits.MaximumExtractedBytes+int64(info.PageCount))
	if err != nil {
		return DocumentInfo{}, classifyCommandError(ctx, err)
	}
	if int64(len(output)) > p.limits.MaximumExtractedBytes+int64(info.PageCount) {
		return DocumentInfo{}, &categorizedError{category: domain.FailureResourceLimitExceeded}
	}
	parts := bytes.Split(output, []byte{'\f'})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	extractedPages := uint32(len(parts)) // #nosec G115 -- bounded by configured page count and output size.
	if extractedPages != info.PageCount {
		return DocumentInfo{}, &categorizedError{category: domain.FailureMalformedDocument}
	}
	var extracted int64
	for index, content := range parts {
		if int64(len(content)) > p.limits.MaximumPageBytes {
			return DocumentInfo{}, &categorizedError{category: domain.FailureResourceLimitExceeded}
		}
		extracted += int64(len(content))
		if extracted > p.limits.MaximumExtractedBytes {
			return DocumentInfo{}, &categorizedError{category: domain.FailureResourceLimitExceeded}
		}
		if err = consume(Page{Number: uint32(index + 1), Text: string(content)}); err != nil {
			return DocumentInfo{}, err
		}
	}
	return info, nil
}

func classifyStreamError(ctx context.Context, err error) error {
	var categorized *categorizedError
	if errors.As(err, &categorized) {
		return err
	}
	return classifyCommandError(ctx, err)
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
		return DocumentInfo{}, &categorizedError{category: domain.FailureResourceLimitExceeded}
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

func hasIncorrectPasswordDiagnostic(err error) bool {
	var commandErr *commandError
	return errors.As(err, &commandErr) && bytes.Contains(commandErr.stderr, []byte("Incorrect password"))
}

type ExecRunner struct{}

func VerifySandbox(ctx context.Context) error {
	return verifySandbox(ctx, ExecRunner{})
}

func verifySandbox(ctx context.Context, runner Runner) error {
	if _, err := runner.Run(ctx, "/parser-sandbox", []string{"--landlock-preflight"}, 1); err != nil {
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
	return "/parser-sandbox", append([]string{path}, args...)
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
				return newCommandError(waitErr, stderr.Bytes())
			}
			return streamErr
		}
		_ = command.Cancel()
		_ = command.Wait()
		return streamErr
	}
	if err = command.Wait(); err != nil {
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
				return consumeErr
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
		return nil, newCommandError(err, stderr.Bytes())
	}
	if stdout.exceeded {
		return stdout.Bytes(), &categorizedError{category: domain.FailureResourceLimitExceeded}
	}
	return stdout.Bytes(), nil
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
