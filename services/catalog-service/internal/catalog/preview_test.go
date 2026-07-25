package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		case "/usr/bin/pdfseparate":
			outputPattern := args[len(args)-1]
			for page := 1; page <= previewPageLimit; page++ {
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
			return fmt.Errorf("unexpected command %q", command)
		}
	}

	objects := NewMemoryObjectStore()
	if _, err := objects.Put(context.Background(), "book.pdf", strings.NewReader("%PDF-1.4\nbody")); err != nil {
		t.Fatal(err)
	}
	preview, err := defaultPreviewBook(context.Background(), Book{MediaType: "application/pdf", ObjectReference: "book.pdf"}, objects)
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

func TestInjectEPUBPreviewCSPWrapsTheDocument(t *testing.T) {
	page := []byte(`<?xml version="1.0"?><html><head><title>preview</title></head><body><img src="https://example.invalid/track.png"></body></html>`)
	wrapped := injectEPUBPreviewCSP(page)
	if !strings.Contains(wrapped, "Content-Security-Policy") {
		t.Fatalf("wrapped preview = %q", wrapped)
	}
}
