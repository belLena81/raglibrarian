package main

import (
	"testing"
	"time"

	"github.com/belLena81/raglibrarian/pkg/process"
)

func TestParseRunAsUsesDefaultsAndValidatesOverrides(t *testing.T) {
	t.Setenv("RUN_AS_UID", "")
	t.Setenv("RUN_AS_GID", "")
	identity, err := parseRunAs()
	if err != nil || identity != (process.Identity{UID: 65532, GID: 65532}) {
		t.Fatalf("default identity=%+v err=%v", identity, err)
	}

	t.Setenv("RUN_AS_UID", "1234")
	t.Setenv("RUN_AS_GID", "5678")
	identity, err = parseRunAs()
	if err != nil || identity != (process.Identity{UID: 1234, GID: 5678}) {
		t.Fatalf("configured identity=%+v err=%v", identity, err)
	}

	for _, value := range []string{"0", "-1", "root", "2147483648"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("RUN_AS_UID", value)
			t.Setenv("RUN_AS_GID", "65532")
			if _, err := parseRunAs(); err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestEnvDurationUsesFallbackOnlyForMissingValue(t *testing.T) {
	t.Setenv("ANSWER_STUB_READ_TIMEOUT", "")
	if value, err := envDuration("ANSWER_STUB_READ_TIMEOUT", 5*time.Second); err != nil || value != 5*time.Second {
		t.Fatalf("envDuration() = %v, want fallback", value)
	}

	t.Setenv("ANSWER_STUB_READ_TIMEOUT", "invalid")
	if _, err := envDuration("ANSWER_STUB_READ_TIMEOUT", 5*time.Second); err == nil {
		t.Fatal("envDuration() accepted malformed explicit value")
	}

	t.Setenv("ANSWER_STUB_READ_TIMEOUT", "0s")
	if _, err := envDuration("ANSWER_STUB_READ_TIMEOUT", 5*time.Second); err == nil {
		t.Fatal("envDuration() accepted zero explicit value")
	}
}

func TestEnvDurationParsesPositiveDuration(t *testing.T) {
	t.Setenv("ANSWER_STUB_READ_TIMEOUT", "7s")
	if value, err := envDuration("ANSWER_STUB_READ_TIMEOUT", 5*time.Second); err != nil || value != 7*time.Second {
		t.Fatalf("envDuration() = %v, want 7s", value)
	}
}

func TestEnvInt64UsesFallbackOnlyForMissingValue(t *testing.T) {
	t.Setenv("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", "")
	if value, err := envInt64("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", 128); err != nil || value != 128 {
		t.Fatalf("envInt64() = %d, want fallback", value)
	}

	t.Setenv("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", "invalid")
	if _, err := envInt64("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", 128); err == nil {
		t.Fatal("envInt64() accepted malformed explicit value")
	}

	t.Setenv("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", "0")
	if _, err := envInt64("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", 128); err == nil {
		t.Fatal("envInt64() accepted zero explicit value")
	}
}

func TestEnvInt64ParsesPositiveValue(t *testing.T) {
	t.Setenv("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", "512")
	if value, err := envInt64("ANSWER_STUB_MAX_REQUEST_BODY_BYTES", 128); err != nil || value != 512 {
		t.Fatalf("envInt64() = %d, want 512", value)
	}
}
