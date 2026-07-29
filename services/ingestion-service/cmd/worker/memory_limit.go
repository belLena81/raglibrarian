package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
)

var cgroupMemoryLimitFiles = []string{
	"/sys/fs/cgroup/memory.max",
	"/sys/fs/cgroup/memory/memory.limit_in_bytes",
}

var cgroupMemoryLimitReader = readKnownMemoryLimitFile

func validateRuntimeMemoryBudget(cfg config.Config) error {
	required := int64(cfg.WorkConcurrency)*cfg.ParserSandboxMemoryBytes + cfg.ParserRuntimeHeadroomBytes
	limit, ok, err := effectiveCgroupMemoryLimitBytes(cgroupMemoryLimitFiles)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if limit < required {
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
	switch path {
	case "/sys/fs/cgroup/memory.max":
		return readMemoryMaxFile()
	case "/sys/fs/cgroup/memory/memory.limit_in_bytes":
		return readMemoryLimitInBytesFile()
	default:
		return 0, false, fmt.Errorf("unsupported cgroup memory limit path %s", path)
	}
}

func readMemoryMaxFile() (int64, bool, error) {
	data, err := os.ReadFile("/sys/fs/cgroup/memory.max") // #nosec G304 -- fixed kernel cgroup path.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read memory limit %s: %w", "/sys/fs/cgroup/memory.max", err)
	}
	return parseMemoryLimitFile("/sys/fs/cgroup/memory.max", data)
}

func readMemoryLimitInBytesFile() (int64, bool, error) {
	data, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes") // #nosec G304 -- fixed kernel cgroup path.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read memory limit %s: %w", "/sys/fs/cgroup/memory/memory.limit_in_bytes", err)
	}
	return parseMemoryLimitFile("/sys/fs/cgroup/memory/memory.limit_in_bytes", data)
}

func parseMemoryLimitFile(path string, data []byte) (int64, bool, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "max" {
		return 0, false, nil
	}
	value, parseErr := strconv.ParseInt(trimmed, 10, 64)
	if parseErr != nil || value <= 0 {
		return 0, false, fmt.Errorf("parse memory limit %s: %q", path, trimmed)
	}
	return value, true, nil
}
