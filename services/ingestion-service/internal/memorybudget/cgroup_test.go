package memorybudget

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestEffectiveCgroupMemoryLimitPrefersFirstBoundedValue(t *testing.T) {
	stubMemoryLimitReader(t, func(path string) (int64, bool, error) {
		switch filepath.Base(path) {
		case "memory.max":
			return 1 << 30, true, nil
		case "memory.limit_in_bytes":
			return 2 << 30, true, nil
		default:
			return 0, false, nil
		}
	})
	value, ok, err := effectiveCgroupMemoryLimitBytes([]string{"/tmp/memory.max", "/tmp/memory.limit_in_bytes"})
	if err != nil || !ok || value != 1<<30 {
		t.Fatalf("effective limit = (%d, %t, %v)", value, ok, err)
	}
}

func TestEffectiveCgroupMemoryLimitPropagatesInvalidContents(t *testing.T) {
	stubMemoryLimitReader(t, func(string) (int64, bool, error) {
		return 0, false, errors.New("invalid cgroup contents")
	})
	if _, _, err := effectiveCgroupMemoryLimitBytes([]string{"/tmp/memory.max"}); err == nil {
		t.Fatal("invalid cgroup contents accepted")
	}
}

func TestValidateRejectsInsufficientEffectiveLimit(t *testing.T) {
	stubMemoryLimitReader(t, func(string) (int64, bool, error) { return 1 << 30, true, nil })
	if err := Validate(1, 1536<<20, 256<<20); err == nil {
		t.Fatal("insufficient cgroup memory accepted")
	}
}

func TestValidateAcceptsSufficientOrUnlimitedLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		reader func(string) (int64, bool, error)
	}{
		{name: "sufficient", reader: func(string) (int64, bool, error) { return 2 << 30, true, nil }},
		{name: "unlimited", reader: func(string) (int64, bool, error) { return 0, false, nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			stubMemoryLimitReader(t, test.reader)
			if err := Validate(1, 1536<<20, 256<<20); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateRejectsInvalidPolicy(t *testing.T) {
	if err := Validate(0, 0, 0); err == nil {
		t.Fatal("invalid budget accepted")
	}
}

func stubMemoryLimitReader(t *testing.T, reader func(string) (int64, bool, error)) {
	t.Helper()
	originalFiles := cgroupMemoryLimitFiles
	originalReader := cgroupMemoryLimitReader
	t.Cleanup(func() {
		cgroupMemoryLimitFiles = originalFiles
		cgroupMemoryLimitReader = originalReader
	})
	cgroupMemoryLimitFiles = []string{"/tmp/memory.max", "/tmp/memory.limit_in_bytes"}
	cgroupMemoryLimitReader = reader
}
