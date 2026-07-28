package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
)

func TestEffectiveCgroupMemoryLimitBytesPrefersFirstPresentBoundedValue(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "memory.max")
	second := filepath.Join(dir, "memory.limit_in_bytes")
	writeTestFile(t, second, "2147483648\n")
	writeTestFile(t, first, "1073741824\n")

	value, ok, err := effectiveCgroupMemoryLimitBytes([]string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || value != 1073741824 {
		t.Fatalf("effectiveCgroupMemoryLimitBytes() = %d, %t", value, ok)
	}
}

func TestEffectiveCgroupMemoryLimitBytesIgnoresUnlimitedOrMissingFiles(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "memory.max")
	writeTestFile(t, first, "max\n")

	value, ok, err := effectiveCgroupMemoryLimitBytes([]string{first, filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if ok || value != 0 {
		t.Fatalf("effectiveCgroupMemoryLimitBytes() = %d, %t", value, ok)
	}
}

func TestEffectiveCgroupMemoryLimitBytesRejectsInvalidContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.max")
	writeTestFile(t, path, "not-a-number\n")

	if _, _, err := effectiveCgroupMemoryLimitBytes([]string{path}); err == nil {
		t.Fatal("expected invalid cgroup limit contents to fail")
	}
}

func TestValidateRuntimeMemoryBudgetRejectsInsufficientEffectiveLimit(t *testing.T) {
	original := cgroupMemoryLimitFiles
	t.Cleanup(func() { cgroupMemoryLimitFiles = original })

	dir := t.TempDir()
	path := filepath.Join(dir, "memory.max")
	writeTestFile(t, path, "1073741824\n")
	cgroupMemoryLimitFiles = []string{path}

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
	original := cgroupMemoryLimitFiles
	t.Cleanup(func() { cgroupMemoryLimitFiles = original })

	dir := t.TempDir()
	path := filepath.Join(dir, "memory.max")
	writeTestFile(t, path, "2147483648\n")
	cgroupMemoryLimitFiles = []string{path}

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
