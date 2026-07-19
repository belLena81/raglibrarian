package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
	"github.com/belLena81/raglibrarian/services/ingestion-service/diagnostic"
	"github.com/belLena81/raglibrarian/services/ingestion-service/internal/application"
)

func TestRuntimeValidatesEventBeforeLoggingIdentifiers(t *testing.T) {
	const marker = "malicious-log-marker"
	var output bytes.Buffer
	runtimeValue := &Runtime{
		Config:      config.Config{MaximumSourceBytes: 50 << 20},
		Diagnostics: diagnostic.New(slog.New(slog.NewJSONHandler(&output, nil))),
	}
	event := application.UploadedEvent{EventID: marker + "\nforged", BookID: marker}

	err := runtimeValue.Process(context.Background(), event)
	if !errors.Is(err, application.ErrInvalidEvent) {
		t.Fatalf("Process() error = %v, want %v", err, application.ErrInvalidEvent)
	}
	if strings.Contains(output.String(), marker) {
		t.Fatalf("invalid identifier was logged: %q", output.String())
	}
}
