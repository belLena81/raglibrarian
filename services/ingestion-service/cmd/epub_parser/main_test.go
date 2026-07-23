package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/extractor"
)

func TestRunReturnsMalformedProtocolCodeWithoutOutput(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "malformed.epub")
	if err := os.WriteFile(sourcePath, []byte("not an EPUB"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer

	code := run([]string{sourcePath}, &output)

	if code != extractor.EPUBParserExitMalformed {
		t.Fatalf("run() code = %d, want %d", code, extractor.EPUBParserExitMalformed)
	}
	if output.Len() != 0 {
		t.Fatalf("run() emitted parser diagnostics: %q", output.String())
	}
}

func TestRunClassifiesOutputFailureAsInternalWithoutDiagnostics(t *testing.T) {
	sourcePath := writeParserTestEPUB(t)

	code := run([]string{sourcePath}, failingWriter{})

	if code != extractor.EPUBParserExitInternal {
		t.Fatalf("run() code = %d, want %d", code, extractor.EPUBParserExitInternal)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("private output failure")
}

func writeParserTestEPUB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "valid.epub")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entries := []struct {
		name     string
		contents string
		method   uint16
	}{
		{name: "mimetype", contents: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", contents: `<?xml version="1.0"?><container><rootfiles><rootfile full-path="book.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`, method: zip.Deflate},
		{name: "book.opf", contents: `<?xml version="1.0"?><package><manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`, method: zip.Deflate},
		{name: "chapter.xhtml", contents: `<?xml version="1.0"?><html><body><p>synthetic text</p></body></html>`, method: zip.Deflate},
	}
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method}
		target, createErr := writer.CreateHeader(header)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := target.Write([]byte(entry.contents)); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
