package memorybudget

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

var cgroupMemoryLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

var cgroupMemoryLimitReader = readKnownMemoryLimitFile

// Validate rejects a bounded container that cannot safely accommodate every
// configured parser process plus the parent worker's runtime headroom.
func Validate(workConcurrency int, parserMemoryBytes, runtimeHeadroomBytes int64) error {
	if workConcurrency < 1 || parserMemoryBytes < 1 || runtimeHeadroomBytes < 1 {
		return errors.New("invalid parser memory budget")
	}
	const maximumInt64 = int64(^uint64(0) >> 1)
	if parserMemoryBytes > (maximumInt64-runtimeHeadroomBytes)/int64(workConcurrency) {
		return errors.New("parser memory budget overflow")
	}
	required := int64(workConcurrency)*parserMemoryBytes + runtimeHeadroomBytes
	limit, ok, err := effectiveCgroupMemoryLimitBytes(cgroupMemoryLimitFiles)
	if err != nil {
		return err
	}
	if ok && limit < required {
		return fmt.Errorf("effective cgroup memory limit %d is below required parser budget %d", limit, required)
	}
	return nil
}

func effectiveCgroupMemoryLimitBytes(paths []string) (int64, bool, error) {
	for _, path := range paths {
		value, ok, err := cgroupMemoryLimitReader(path)
		if err != nil {
			return 0, false, err
		}
		if ok {
			return value, true, nil
		}
	}
	return 0, false, nil
}

func readKnownMemoryLimitFile(path string) (int64, bool, error) {
	var data []byte
	var err error
	switch path {
	case "/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes":
		data, err = os.ReadFile(path) // #nosec G304 -- only fixed kernel cgroup paths are accepted above.
	default:
		return 0, false, fmt.Errorf("unsupported cgroup memory limit path %s", path)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read memory limit %s: %w", path, err)
	}
	return parseMemoryLimitFile(path, data)
}

func parseMemoryLimitFile(path string, data []byte) (int64, bool, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "max" {
		return 0, false, nil
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || value <= 0 {
		return 0, false, fmt.Errorf("parse memory limit %s: %q", path, trimmed)
	}
	return value, true, nil
}
