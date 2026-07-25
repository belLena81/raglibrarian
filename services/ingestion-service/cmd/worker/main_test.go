package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestLogRunErrorIncludesUnderlyingError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	logRunError(logger, errors.New("rabbitmq dial failed"))

	var payload map[string]any
	if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if payload["msg"] != "ingestion worker stopped" {
		t.Fatalf("msg = %v, want ingestion worker stopped", payload["msg"])
	}
	if payload["reason"] != "runtime_failure" {
		t.Fatalf("reason = %v, want runtime_failure", payload["reason"])
	}
	if payload["error"] != "rabbitmq dial failed" {
		t.Fatalf("error = %v, want rabbitmq dial failed", payload["error"])
	}
	if strings.Contains(output.String(), "\n\n") {
		t.Fatalf("unexpected blank lines in log output: %q", output.String())
	}
}
