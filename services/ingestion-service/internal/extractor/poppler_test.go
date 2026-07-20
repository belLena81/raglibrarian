package extractor

import (
	"context"
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

func TestParseInfoRecognizesPaddedEncryptedField(t *testing.T) {
	extractor := NewPoppler("pdfinfo", "pdftotext", Limits{}, &fakeRunner{})
	_, err := extractor.parseInfo([]byte("Pages:           1\nEncrypted:       yes (print:yes copy:yes)\n"))
	category, ok := FailureCategory(err)
	if !ok || category != domain.FailureEncryptedDocument {
		t.Fatalf("failure = %v, category = %q", err, category)
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
