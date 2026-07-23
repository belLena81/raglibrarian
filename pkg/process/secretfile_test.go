package process

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSecretFilePermissions(t *testing.T) {
	directory := t.TempDir()
	ownerOnly := filepath.Join(directory, "owner-only")
	if err := os.WriteFile(ownerOnly, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenSecretFile(ownerOnly, 16); err != nil {
		t.Fatalf("OpenSecretFile() error = %v", err)
	} else {
		_ = file.Close()
	}

	outsideAzure := filepath.Join(directory, "azure-mode")
	if err := os.WriteFile(outsideAzure, []byte("value"), 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSecretFile(outsideAzure, 16); err == nil {
		t.Fatal("OpenSecretFile() accepted 0444 file outside Azure mount")
	}
}

func TestValidSecretFileAcceptsAzureMount0444Only(t *testing.T) {
	info, err := os.Stat("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	if validSecretFile("/mnt/secrets/runtime", info, 16) {
		t.Fatal("test fixture unexpectedly has a regular-file mode")
	}
	secret := fakeFileInfo{FileInfo: info, mode: 0o444, size: 5}
	if !validSecretFile("/mnt/secrets/runtime", secret, 16) {
		t.Fatal("validSecretFile() rejected Azure 0444 secret")
	}
	if validSecretFile("/mnt/secrets/nested/runtime", secret, 16) {
		t.Fatal("validSecretFile() accepted nested Azure secret")
	}
}

type fakeFileInfo struct {
	os.FileInfo
	mode os.FileMode
	size int64
}

func (f fakeFileInfo) Mode() os.FileMode { return f.mode }
func (f fakeFileInfo) Size() int64       { return f.size }

func TestOpenSecretFileRejectsSymlinkAndOversizedFile(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("too-large"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSecretFile(link, 32); err == nil {
		t.Fatal("OpenSecretFile() followed a symlink")
	}
	if _, err := OpenSecretFile(target, 1); err == nil {
		t.Fatal("OpenSecretFile() accepted an oversized file")
	}
}
