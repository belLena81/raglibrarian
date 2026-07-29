package catalog

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testMaxPreviewBytes = 1 << 20
	testMaxPreviewPages = 3
	testMaxEPUBEntries  = 2048
)

func TestDefaultPreviewBookRemovesTemporaryWorkspace(t *testing.T) {
	originalPreviewTempDir := previewTempDir
	originalPreviewExecCommand := previewExecCommand
	t.Cleanup(func() {
		previewTempDir = originalPreviewTempDir
		previewExecCommand = originalPreviewExecCommand
	})

	workspace := filepath.Join(t.TempDir(), "catalog-preview-workspace")
	previewTempDir = func(_, _ string) (string, error) {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			return "", err
		}
		return workspace, nil
	}
	previewExecCommand = func(_ context.Context, command string, args ...string) error {
		switch command {
		case "/parser-sandbox":
			switch args[0] {
			case "/usr/bin/pdfseparate":
				outputPattern := args[len(args)-1]
				for page := 1; page <= testMaxPreviewPages; page++ {
					pagePath := fmt.Sprintf(outputPattern, page)
					if err := os.WriteFile(pagePath, []byte(fmt.Sprintf("page-%d", page)), 0o600); err != nil {
						return err
					}
				}
				return nil
			case "/usr/bin/pdfunite":
				fragmentPath := args[len(args)-1]
				return os.WriteFile(fragmentPath, []byte("%PDF-1.4\npreview\n"), 0o600)
			default:
				return fmt.Errorf("unexpected sandbox command %q", args[0])
			}
		default:
			return fmt.Errorf("unexpected command %q", command)
		}
	}

	objects := NewMemoryObjectStore()
	source := []byte("%PDF-1.4\nbody")
	if _, err := objects.Put(context.Background(), "book.pdf", strings.NewReader(string(source))); err != nil {
		t.Fatal(err)
	}
	preview, err := defaultPreviewBook(context.Background(), Book{
		MediaType:       "application/pdf",
		ObjectReference: "book.pdf",
		ByteSize:        int64(len(source)),
		Checksum:        sha256.Sum256(source),
	}, objects, int64(len(source)), testMaxPreviewBytes, testMaxPreviewPages, testMaxEPUBEntries)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(preview, "data:application/pdf;base64,") {
		t.Fatalf("preview = %q", preview)
	}
	if _, err := os.Stat(workspace); !os.IsNotExist(err) {
		t.Fatalf("workspace %q still exists after preview generation: %v", workspace, err)
	}
}

func TestDefaultPreviewBookRejectsStoredSourceIntegrityMismatch(t *testing.T) {
	objects := NewMemoryObjectStore()
	source := []byte("%PDF-1.4\nbody")
	if _, err := objects.Put(context.Background(), "book.pdf", strings.NewReader(string(source))); err != nil {
		t.Fatal(err)
	}

	_, err := defaultPreviewBook(context.Background(), Book{
		MediaType:       "application/pdf",
		ObjectReference: "book.pdf",
		ByteSize:        int64(len(source)),
		Checksum:        sha256.Sum256([]byte("different")),
	}, objects, int64(len(source)), testMaxPreviewBytes, testMaxPreviewPages, testMaxEPUBEntries)

	if err == nil {
		t.Fatal("preview source checksum mismatch was accepted")
	}
}

func TestExtractEPUBPreviewPagesRejectsOversizedArchive(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "book.epub")
	if err := writePreviewEPUB(sourcePath, testMaxEPUBEntries+1); err != nil {
		t.Fatal(err)
	}

	_, err := extractEPUBPreviewPages(sourcePath, testMaxPreviewBytes, testMaxPreviewPages, testMaxEPUBEntries)

	if err == nil {
		t.Fatal("oversized EPUB archive was accepted")
	}
}

func TestPreviewPDFFragmentRejectsOversizedOutput(t *testing.T) {
	originalPreviewExecCommand := previewExecCommand
	t.Cleanup(func() {
		previewExecCommand = originalPreviewExecCommand
	})
	outputDir := t.TempDir()
	previewExecCommand = func(_ context.Context, _ string, args ...string) error {
		switch args[0] {
		case "/usr/bin/pdfseparate":
			pagePath := strings.Replace(args[len(args)-1], "%03d", "001", 1)
			return os.WriteFile(pagePath, []byte("page"), 0o600)
		case "/usr/bin/pdfunite":
			return os.WriteFile(args[len(args)-1], []byte("12345"), 0o600)
		default:
			return fmt.Errorf("unexpected command %q", args[0])
		}
	}

	_, err := previewPDFFragment(context.Background(), "source.pdf", outputDir, 4, 1)

	if err == nil {
		t.Fatal("oversized PDF preview was accepted")
	}
}

func TestPreviewPDFFragmentBoundsFinalDataURI(t *testing.T) {
	originalPreviewExecCommand := previewExecCommand
	t.Cleanup(func() {
		previewExecCommand = originalPreviewExecCommand
	})
	outputDir := t.TempDir()
	previewExecCommand = func(_ context.Context, _ string, args ...string) error {
		switch args[0] {
		case "/usr/bin/pdfseparate":
			pagePath := strings.Replace(args[len(args)-1], "%03d", "001", 1)
			return os.WriteFile(pagePath, []byte("page"), 0o600)
		case "/usr/bin/pdfunite":
			return os.WriteFile(args[len(args)-1], []byte("123456"), 0o600)
		default:
			return fmt.Errorf("unexpected command %q", args[0])
		}
	}
	const maximumBytes = len("data:application/pdf;base64,") + 8

	preview, err := previewPDFFragment(context.Background(), "source.pdf", outputDir, maximumBytes, 1)

	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != maximumBytes {
		t.Fatalf("preview length = %d, want %d", len(preview), maximumBytes)
	}
}

func TestPreviewEPUBFragmentRejectsEscapedOutputBeyondFinalBudget(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "book.epub")
	if err := writeValidPreviewEPUB(sourcePath, strings.Repeat("&", 512)); err != nil {
		t.Fatal(err)
	}

	preview, err := previewEPUBFragment(sourcePath, 1024, 1, 16)

	if err == nil {
		t.Fatalf("escape-amplified EPUB preview was accepted with %d bytes", len(preview))
	}
}

func TestReadEPUBEntryRejectsOversizedContent(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "entry.zip")
	file, err := os.Create(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("page.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("12345")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archive.Close() }()

	if _, err = readEPUBEntry(archive.File[0], 4); err == nil {
		t.Fatal("oversized EPUB entry was accepted")
	}
}

func writePreviewEPUB(path string, totalEntries int) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	writer := zip.NewWriter(file)
	for index := 0; index < totalEntries; index++ {
		name := fmt.Sprintf("entry-%05d.txt", index)
		switch index {
		case 0:
			name = "mimetype"
		case 1:
			name = "META-INF/container.xml"
		case 2:
			name = "EPUB/content.opf"
		}
		entry, err := writer.Create(name)
		if err != nil {
			return err
		}
		if _, err = entry.Write([]byte("synthetic")); err != nil {
			return err
		}
	}
	return writer.Close()
}

func writeValidPreviewEPUB(filePath, page string) error {
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	writer := zip.NewWriter(file)
	entries := map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="EPUB/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"EPUB/content.opf":       `<package><manifest><item id="page" href="page.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="page"/></spine></package>`,
		"EPUB/page.xhtml":        `<html><head></head><body>` + page + `</body></html>`,
	}
	for name, contents := range entries {
		entry, createErr := writer.Create(name)
		if createErr != nil {
			return createErr
		}
		if _, writeErr := entry.Write([]byte(contents)); writeErr != nil {
			return writeErr
		}
	}
	return writer.Close()
}

func TestInjectEPUBPreviewCSPWrapsTheDocument(t *testing.T) {
	page := []byte(`<?xml version="1.0"?><html><head><title>preview</title></head><body><img src="https://example.invalid/track.png"></body></html>`)
	wrapped := injectEPUBPreviewCSP(page)
	if !strings.Contains(wrapped, "Content-Security-Policy") {
		t.Fatalf("wrapped preview = %q", wrapped)
	}
}

func TestInjectEPUBPreviewCSPUsesTheRealHeadElement(t *testing.T) {
	page := []byte(`<?xml version="1.0"?><!-- <head> --><html><head><title>preview</title></head><body><img src="https://example.invalid/track.png"></body></html>`)
	wrapped := injectEPUBPreviewCSP(page)
	if !strings.Contains(wrapped, "<head>"+epubPreviewCSP) {
		t.Fatalf("wrapped preview = %q", wrapped)
	}
	if strings.Contains(wrapped, "<!-- <head> -->"+epubPreviewCSP) {
		t.Fatalf("CSP was injected into the comment instead of the real head: %q", wrapped)
	}
}
