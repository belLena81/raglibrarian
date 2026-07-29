package extractor

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/domain"
)

type fakeRunner struct{ outputs [][]byte }

func (r *fakeRunner) Run(context.Context, string, []string, int64) ([]byte, error) {
	output := r.outputs[0]
	r.outputs = r.outputs[1:]
	return output, nil
}

type failingRunner struct{ err error }

func (r failingRunner) Run(context.Context, string, []string, int64) ([]byte, error) {
	return nil, r.err
}

type streamingRunner struct {
	info   []byte
	stream string
}

func (r streamingRunner) Run(context.Context, string, []string, int64) ([]byte, error) {
	return r.info, nil
}

func (r streamingRunner) StreamPages(_ context.Context, _ string, _ []string, limits Limits, expectedPages uint32, consume func(Page) error) error {
	return consumePageStream(strings.NewReader(r.stream), limits, expectedPages, consume)
}

func TestVerifySandboxReportsUnavailable(t *testing.T) {
	err := verifySandbox(context.Background(), failingRunner{err: exec.ErrNotFound})
	if !errors.Is(err, ErrSandboxUnavailable) {
		t.Fatalf("expected sandbox unavailable, got %v", err)
	}
}

func TestStreamPagesTreatsParentCancellationAsRetryable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	err := (ExecRunner{}).StreamPages(
		ctx,
		"sh",
		[]string{"-c", "printf 'first'; sleep 10"},
		Limits{MaximumPageBytes: 32, MaximumExtractedBytes: 64},
		3,
		func(Page) error { return nil },
	)
	classified := classifyStreamError(ctx, err)
	category, ok := FailureCategory(classified)
	if !ok || category != domain.FailureInternalProcessing {
		t.Fatalf("expected retryable internal processing failure, got %q", category)
	}
}

func TestClassifySandboxSetupFailures(t *testing.T) {
	tests := []struct {
		code     string
		expected domain.FailureCategory
	}{
		{code: "121", expected: domain.FailureResourceLimitExceeded},
		{code: "122", expected: domain.FailureDependencyUnavailable},
		{code: "123", expected: domain.FailureDependencyUnavailable},
		{code: "124", expected: domain.FailureDependencyUnavailable},
		{code: "2", expected: domain.FailureInternalProcessing},
		{code: "1", expected: domain.FailureMalformedDocument},
		{code: "99", expected: domain.FailureInternalProcessing},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			err := exec.Command("sh", "-c", "exit "+test.code).Run() // #nosec G204 -- fixed synthetic test input.
			classified := classifyCommandError(context.Background(), err)
			category, ok := FailureCategory(classified)
			if !ok || category != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, category)
			}
		})
	}
}

func TestFailureDetailMapsExtractorCommandStages(t *testing.T) {
	detail, ok := FailureDetail(detailedFailure("pdfinfo_failed", classifyCommandError(context.Background(), exec.ErrNotFound)))
	if !ok || detail != "parser_sandbox_failed" {
		t.Fatalf("FailureDetail(pdfinfo) = %q, %t", detail, ok)
	}

	commandErr := exec.Command("sh", "-c", "exit 124").Run() // #nosec G204 -- fixed synthetic test input.
	detail, ok = FailureDetail(detailedFailure("pdfinfo_failed", classifyCommandError(context.Background(), commandErr)))
	if !ok || detail != "pdfinfo_exec_failed" {
		t.Fatalf("FailureDetail(pdfinfo command) = %q, %t", detail, ok)
	}

	detail, ok = FailureDetail(detailedFailure("pdftotext_failed", &categorizedError{category: domain.FailureInternalProcessing, cause: errors.New("other")}))
	if !ok || detail != "pdftotext_failed" {
		t.Fatalf("FailureDetail(pdftotext) = %q, %t", detail, ok)
	}
}

func TestFailureDetailPreservesEPUBParserCommandTokens(t *testing.T) {
	err := detailedFailure("epub_parser_failed", epubFailure(domain.FailureInternalProcessing, &commandError{
		cause:  errors.New("exit status 1"),
		stderr: []byte("epub_parser_panic\n"),
	}))
	detail, ok := FailureDetail(err)
	if !ok || detail != "epub_parser_panic" {
		t.Fatalf("FailureDetail(epub parser) = %q, %t", detail, ok)
	}
}

func TestFailureDetailUsesBoundedFallbackForUnknownEPUBParserInternals(t *testing.T) {
	err := detailedFailure("epub_parser_failed", epubFailure(domain.FailureInternalProcessing, errors.New("unexpected parser state: /tmp/books/secret.epub")))
	detail, ok := FailureDetail(err)
	if !ok || detail != "epub_parser_internal_unexpected_parser_state_tmp_book" {
		t.Fatalf("FailureDetail(epub parser fallback) = %q, %t", detail, ok)
	}
}

func TestFailureDetailMapsEPUBParserCommandErrorToExecFailure(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 1").Run() // #nosec G204 -- fixed synthetic test input.
	err := detailedFailure("epub_parser_failed", epubFailure(domain.FailureInternalProcessing, &commandError{cause: exitErr}))
	detail, ok := FailureDetail(err)
	if !ok || detail != "epub_parser_exec_exit_1" {
		t.Fatalf("FailureDetail(epub parser command error) = %q, %t", detail, ok)
	}
}

func TestFailureDetailMapsEPUBParserExitTwoToInvalidArgs(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 2").Run() // #nosec G204 -- fixed synthetic test input.
	err := detailedFailure("epub_parser_failed", epubFailure(domain.FailureInternalProcessing, &commandError{cause: exitErr}))
	detail, ok := FailureDetail(err)
	if !ok || detail != "epub_parser_invalid_args" {
		t.Fatalf("FailureDetail(epub parser exit 2) = %q, %t", detail, ok)
	}
}

func TestFailureDetailMapsEPUBParserThreadCreationFailureToRuntimeResourceExhaustion(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 2").Run() // #nosec G204 -- fixed synthetic test input.
	err := detailedFailure("epub_parser_failed", epubFailure(domain.FailureInternalProcessing, &commandError{
		cause:  exitErr,
		stderr: []byte("runtime/cgo: pthread_create failed: Resource temporarily unavailable\n"),
	}))
	detail, ok := FailureDetail(err)
	if !ok || detail != "epub_parser_runtime_resource_exhausted" {
		t.Fatalf("FailureDetail(epub parser thread creation failure) = %q, %t", detail, ok)
	}
}

func TestClassifyCommandErrorRecognizesIncorrectPasswordDiagnostic(t *testing.T) {
	_, err := (ExecRunner{}).Run(
		context.Background(),
		"sh",
		[]string{"-c", "printf 'Command Line Error: Incorrect password\\n' >&2; exit 1"},
		1024,
	)
	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), "Incorrect password") {
		t.Fatalf("command error exposed stderr: %v", err)
	}
	classified := classifyCommandError(context.Background(), err)
	category, ok := FailureCategory(classified)
	if !ok || category != domain.FailureEncryptedDocument {
		t.Fatalf("expected encrypted document, got %q", category)
	}
}

func TestClassifyCommandErrorKeepsOtherExitOneFailuresMalformed(t *testing.T) {
	_, err := (ExecRunner{}).Run(
		context.Background(),
		"sh",
		[]string{"-c", "printf 'Syntax Error: damaged document\\n' >&2; exit 1"},
		1024,
	)
	if err == nil {
		t.Fatal("expected command failure")
	}
	classified := classifyCommandError(context.Background(), err)
	category, ok := FailureCategory(classified)
	if !ok || category != domain.FailureMalformedDocument {
		t.Fatalf("expected malformed document, got %q", category)
	}
}

func TestClassifyCommandErrorTreatsRuntimeThreadCreationFailureAsInternalProcessing(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 2").Run() // #nosec G204 -- fixed synthetic test input.
	classified := classifyCommandError(context.Background(), &commandError{
		cause:  exitErr,
		stderr: []byte("runtime/cgo: pthread_create failed: Resource temporarily unavailable\n"),
	})
	category, ok := FailureCategory(classified)
	if !ok || category != domain.FailureInternalProcessing {
		t.Fatalf("expected internal processing error, got %q", category)
	}
}

func TestStreamPagesDistinguishesMalformedOutputFromUnexpectedParserExit(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected domain.FailureCategory
	}{
		{
			name:     "successful parser with incomplete page stream",
			command:  "printf 'first\\f'; exit 0",
			expected: domain.FailureMalformedDocument,
		},
		{
			name:     "failed parser with incomplete page stream",
			command:  "printf 'first\\f'; exit 99",
			expected: domain.FailureInternalProcessing,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := (ExecRunner{}).StreamPages(
				context.Background(),
				"sh",
				[]string{"-c", test.command},
				Limits{MaximumPageBytes: 32, MaximumExtractedBytes: 64},
				3,
				func(Page) error { return nil },
			)
			classified := classifyStreamError(context.Background(), err)
			category, ok := FailureCategory(classified)
			if !ok || category != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, category)
			}
		})
	}
}

func TestStreamPagesPreservesPopplerExitDetail(t *testing.T) {
	err := (ExecRunner{}).StreamPages(
		context.Background(),
		"sh",
		[]string{"-c", "printf 'first'; exit 42"},
		Limits{MaximumPageBytes: 32, MaximumExtractedBytes: 64},
		1,
		func(Page) error { return nil },
	)
	if err == nil {
		t.Fatal("expected command failure")
	}
	detail, ok := FailureDetail(detailedFailure("pdftotext_failed", classifyStreamError(context.Background(), err)))
	if !ok || detail != "pdftotext_failed_parser_command_failed_exit_42" {
		t.Fatalf("FailureDetail(pdftotext stream exit) = %q, %t", detail, ok)
	}
}

func TestPopplerDoesNotRelabelPageConsumerFailureAsPDFToTextFailure(t *testing.T) {
	consumerFailure := errors.New("chunk sequence failed")
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, streamingRunner{
		info:   []byte("Pages: 1\nEncrypted: no\n"),
		stream: "first page\f",
	})
	_, err := extractor.Extract(context.Background(), "source.pdf", func(Page) error {
		return consumerFailure
	})
	if !errors.Is(err, consumerFailure) {
		t.Fatalf("Extract() error = %v, want consumer failure", err)
	}
	if detail, ok := FailureDetail(err); ok && strings.Contains(detail, "pdftotext") {
		t.Fatalf("consumer failure was relabeled as parser failure: %q", detail)
	}
}

func TestPopplerDebugDumpWritesRawPDFTextOutputPrivately(t *testing.T) {
	dumpDir := t.TempDir()
	if err := os.Chmod(dumpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{outputs: [][]byte{
		[]byte("Pages: 1\nEncrypted: no\n"),
		[]byte("first page\f"),
	}}
	extractor := NewPopplerWithOptions("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, runner, dumpDir)
	_, err := extractor.Extract(context.Background(), "source.pdf", func(Page) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("dump entries = %d, want 1", len(entries))
	}
	path := dumpDir + "/" + entries[0].Name()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first page\f" {
		t.Fatalf("dump contents = %q", string(contents))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("dump mode = %o, want 600", info.Mode().Perm())
	}
}

func TestPopplerDebugDumpRejectsSymlinkDirectory(t *testing.T) {
	target := t.TempDir()
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := t.TempDir() + "/dump-link"
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	runner := &fakeRunner{outputs: [][]byte{
		[]byte("Pages: 1\nEncrypted: no\n"),
		[]byte("first page\f"),
	}}
	extractor := NewPopplerWithOptions("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, runner, link)
	_, err := extractor.Extract(context.Background(), "source.pdf", func(Page) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "pdftotext_dump_failed") {
		t.Fatalf("Extract() error = %v, want dump failure", err)
	}
}

func TestPopplerDebugDumpRejectsSharedDirectory(t *testing.T) {
	dumpDir := t.TempDir()
	if err := os.Chmod(dumpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(dumpDir, 0o700)
	})
	runner := &fakeRunner{outputs: [][]byte{
		[]byte("Pages: 1\nEncrypted: no\n"),
		[]byte("first page\f"),
	}}
	extractor := NewPopplerWithOptions("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, runner, dumpDir)
	_, err := extractor.Extract(context.Background(), "source.pdf", func(Page) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "pdftotext_dump_failed") {
		t.Fatalf("Extract() error = %v, want dump failure", err)
	}
}

func TestFailureDetailIncludesSanitizedCommandDiagnostics(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "printf 'runtime loader failure' >&2; exit 9").Run() // #nosec G204 -- fixed synthetic test input.
	err := detailedFailure("pdftotext_failed", classifyStreamError(context.Background(), &commandError{
		cause:  exitErr,
		stderr: []byte("runtime loader failure"),
	}))
	detail, ok := FailureDetail(err)
	if !ok {
		t.Fatal("FailureDetail() ok = false")
	}
	if !strings.HasPrefix(detail, "pdftotext_failed_parser_command_failed_exit_9") {
		t.Fatalf("FailureDetail() = %q", detail)
	}
	if !strings.Contains(detail, "stderr_bytes_22") || !strings.Contains(detail, "stderr_sha256_") {
		t.Fatalf("FailureDetail() missing stderr digest metadata: %q", detail)
	}
	if strings.Contains(detail, "loader") || strings.Contains(detail, "runtime") {
		t.Fatalf("FailureDetail() exposed stderr text: %q", detail)
	}
}

func TestCommandFailureTraceContainsOnlySanitizedStderrMetadata(t *testing.T) {
	exitErr := exec.Command("sh", "-c", "exit 9").Run() // #nosec G204 -- fixed synthetic test input.
	stderr := []byte("epub_parser_panic\nPRIVATE_BOOK_CANARY\x1b[31m\r\n")
	var output bytes.Buffer
	traceCommandFailureTo(
		&output,
		"/usr/local/bin/epub-parser",
		[]string{"v1", "1", "1", "1", "1", "1", "/tmp/private-book.epub"},
		stderr,
		exitErr,
		[]byte("bounded parent stack\n"),
	)
	trace := output.String()
	for _, want := range []string{
		"command=epub-parser",
		"argc=7",
		"reason=epub_parser_panic",
		"exit_code=9",
		"signal=none",
		"stderr_bytes=44",
		"stderr_sha256=",
		"bounded parent stack",
	} {
		if !strings.Contains(trace, want) {
			t.Fatalf("command trace missing %q: %q", want, trace)
		}
	}
	for _, forbidden := range []string{"PRIVATE_BOOK_CANARY", "\x1b", "/tmp/private-book.epub", "command stderr"} {
		if strings.Contains(trace, forbidden) {
			t.Fatalf("command trace exposed %q: %q", forbidden, trace)
		}
	}
}

func TestParseInfoRecognizesPaddedEncryptedField(t *testing.T) {
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{}, &fakeRunner{})
	_, err := extractor.parseInfo([]byte("Pages:           1\nEncrypted:       yes (print:yes copy:yes)\n"))
	category, ok := FailureCategory(err)
	if !ok || category != domain.FailureEncryptedDocument {
		t.Fatalf("failure = %v, category = %q", err, category)
	}
}

func TestParseInfoReportsPageLimitDetail(t *testing.T) {
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{MaximumPages: 1000}, &fakeRunner{})
	_, err := extractor.parseInfo([]byte("Pages: 1001\nEncrypted: no\n"))
	category, ok := FailureCategory(err)
	if !ok || category != domain.FailureResourceLimitExceeded {
		t.Fatalf("failure = %v, category = %q", err, category)
	}
	detail, ok := FailureDetail(err)
	if !ok || detail != "pdf_page_limit_exceeded" {
		t.Fatalf("FailureDetail() = %q, %t", detail, ok)
	}
}

func TestPopplerStreamsPhysicalPages(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{
		[]byte("Pages: 2\nEncrypted: no\n"),
		[]byte("first page\fsecond page\f"),
	}}
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, runner)
	var pages []Page
	info, err := extractor.Extract(context.Background(), "source.pdf", func(page Page) error {
		pages = append(pages, page)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.PageCount != 2 || len(pages) != 2 || strings.TrimSpace(pages[1].Text) != "second page" {
		t.Fatalf("unexpected extraction: %#v %#v", info, pages)
	}
}

func TestPopplerRejectsEncryptedDocument(t *testing.T) {
	runner := &fakeRunner{outputs: [][]byte{[]byte("Pages: 1\nEncrypted: yes (print:yes copy:no)\n")}}
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{MaximumPages: 10, MaximumPageBytes: 1024, MaximumExtractedBytes: 2048}, runner)
	_, err := extractor.Extract(context.Background(), "source.pdf", func(Page) error { return nil })
	if category, ok := FailureCategory(err); !ok || category != "encrypted_document" {
		t.Fatalf("expected encrypted category, got %q %v", category, err)
	}
}

func TestConsumePageStreamPreservesBlankMiddlePageWithoutDocumentBuffering(t *testing.T) {
	var pages []Page
	err := consumePageStream(strings.NewReader("first\f\fthird\f"), Limits{MaximumPageBytes: 32, MaximumExtractedBytes: 64}, 3, func(page Page) error {
		pages = append(pages, page)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 || pages[1].Number != 2 || pages[1].Text != "" {
		t.Fatalf("unexpected pages: %#v", pages)
	}
}

func TestConsumePageStreamStopsAtPerPageLimit(t *testing.T) {
	err := consumePageStream(strings.NewReader("oversized\f"), Limits{MaximumPageBytes: 4, MaximumExtractedBytes: 64}, 1, func(Page) error { return nil })
	if category, ok := FailureCategory(err); !ok || category != "resource_limit_exceeded" {
		t.Fatalf("expected resource limit category, got %q %v", category, err)
	}
}
