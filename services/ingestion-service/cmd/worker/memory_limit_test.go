package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
)

func TestEffectiveCgroupMemoryLimitBytesPrefersFirstPresentBoundedValue(t *testing.T) {
	original := cgroupMemoryLimitReader
	t.Cleanup(func() { cgroupMemoryLimitReader = original })
	cgroupMemoryLimitReader = func(path string) (int64, bool, error) {
		switch filepath.Base(path) {
		case "memory.max":
			return 1073741824, true, nil
		case "memory.limit_in_bytes":
			return 2147483648, true, nil
		default:
			return 0, false, nil
		}
	}

	value, ok, err := effectiveCgroupMemoryLimitBytes([]string{"/tmp/memory.max", "/tmp/memory.limit_in_bytes"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || value != 1073741824 {
		t.Fatalf("effectiveCgroupMemoryLimitBytes() = %d, %t", value, ok)
	}
}

func TestEffectiveCgroupMemoryLimitBytesIgnoresUnlimitedOrMissingFiles(t *testing.T) {
	original := cgroupMemoryLimitReader
	t.Cleanup(func() { cgroupMemoryLimitReader = original })
	cgroupMemoryLimitReader = func(path string) (int64, bool, error) {
		if filepath.Base(path) == "memory.max" {
			return 0, false, nil
		}
		return 0, false, nil
	}

	value, ok, err := effectiveCgroupMemoryLimitBytes([]string{"/tmp/memory.max", "/tmp/missing"})
	if err != nil {
		t.Fatal(err)
	}
	if ok || value != 0 {
		t.Fatalf("effectiveCgroupMemoryLimitBytes() = %d, %t", value, ok)
	}
}

func TestEffectiveCgroupMemoryLimitBytesRejectsInvalidContents(t *testing.T) {
	original := cgroupMemoryLimitReader
	t.Cleanup(func() { cgroupMemoryLimitReader = original })
	cgroupMemoryLimitReader = func(string) (int64, bool, error) {
		return 0, false, errors.New("parse memory limit /tmp/memory.max: \"not-a-number\"")
	}

	if _, _, err := effectiveCgroupMemoryLimitBytes([]string{"/tmp/memory.max"}); err == nil {
		t.Fatal("expected invalid cgroup limit contents to fail")
	}
}

func TestEffectiveCgroupMemoryLimitBytesRejectsUnknownPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "other.limit")
	writeTestFile(t, path, "1024\n")

	if _, _, err := effectiveCgroupMemoryLimitBytes([]string{path}); err == nil {
		t.Fatal("expected unknown cgroup path to fail")
	}
}

func TestValidateRuntimeMemoryBudgetRejectsInsufficientEffectiveLimit(t *testing.T) {
	originalFiles := cgroupMemoryLimitFiles
	originalReader := cgroupMemoryLimitReader
	t.Cleanup(func() {
		cgroupMemoryLimitFiles = originalFiles
		cgroupMemoryLimitReader = originalReader
	})
	cgroupMemoryLimitFiles = []string{"/tmp/memory.max"}
	cgroupMemoryLimitReader = func(string) (int64, bool, error) { return 1073741824, true, nil }

	err := validateRuntimeMemoryBudget(config.Config{
		WorkConcurrency:            1,
		ParserSandboxMemoryBytes:   1536 << 20,
		ParserRuntimeHeadroomBytes: 256 << 20,
	})
	if err == nil {
		t.Fatal("expected insufficient cgroup memory limit to fail")
	}
}

func TestValidateRuntimeMemoryBudgetAcceptsSufficientEffectiveLimit(t *testing.T) {
	originalFiles := cgroupMemoryLimitFiles
	originalReader := cgroupMemoryLimitReader
	t.Cleanup(func() {
		cgroupMemoryLimitFiles = originalFiles
		cgroupMemoryLimitReader = originalReader
	})
	cgroupMemoryLimitFiles = []string{"/tmp/memory.max"}
	cgroupMemoryLimitReader = func(string) (int64, bool, error) { return 2147483648, true, nil }

	if err := validateRuntimeMemoryBudget(config.Config{
		WorkConcurrency:            1,
		ParserSandboxMemoryBytes:   1536 << 20,
		ParserRuntimeHeadroomBytes: 256 << 20,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
