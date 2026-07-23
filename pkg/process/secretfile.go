package process

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

const azureSecretMount = "/mnt/secrets"

// OpenSecretFile opens a bounded regular secret file. Owner-only files are
// accepted everywhere; Azure Container Apps' immutable 0444 files are accepted
// only as direct children of its canonical secret mount.
func OpenSecretFile(path string, maximum int64) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0) // #nosec G304 -- operator-supplied secret path.
	if err != nil {
		return nil, errors.New("invalid secret file")
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("invalid secret file")
	}
	opened, err := file.Stat()
	if err != nil || !validSecretFile(path, opened, maximum) {
		_ = file.Close()
		return nil, errors.New("invalid secret file")
	}
	return file, nil
}

func validSecretFile(path string, info os.FileInfo, maximum int64) bool {
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > maximum {
		return false
	}
	permissions := info.Mode().Perm()
	if permissions&0o077 == 0 {
		return true
	}
	return permissions == 0o444 && filepath.Dir(filepath.Clean(path)) == azureSecretMount
}
