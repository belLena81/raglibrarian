package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLandlockAccessFSMasksRightsByABI(t *testing.T) {
	base := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	for _, test := range []struct {
		name string
		abi  uintptr
		want uint64
	}{
		{name: "ABI 1", abi: 1, want: base},
		{name: "ABI 2", abi: 2, want: base | unix.LANDLOCK_ACCESS_FS_REFER},
		{name: "ABI 3", abi: 3, want: base | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE},
		{name: "future ABI", abi: 4, want: base | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := landlockAccessFS(test.abi)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("landlockAccessFS(%d) = %#x, want %#x", test.abi, got, test.want)
			}
		})
	}
	if _, err := landlockAccessFS(0); err == nil {
		t.Fatal("ABI 0 was accepted")
	}
}

func TestNegotiatedLandlockAccessPropagatesABIQueryFailure(t *testing.T) {
	want := errors.New("Landlock ABI query failed")
	_, err := negotiatedLandlockAccess(func() (uintptr, error) {
		return 0, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("negotiatedLandlockAccess() error = %v, want %v", err, want)
	}
}

func TestValidatedCommandAllowsOnlyFixedPopplerShapeAndRegularTemporarySource(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.pdf")
	if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := validatedCommand([]string{"/usr/bin/pdfinfo", source}); err != nil {
		t.Fatalf("pdfinfo command rejected: %v", err)
	}
	if _, _, _, err := validatedCommand([]string{"/usr/bin/pdftotext", "-layout", "-enc", "UTF-8", source, "-"}); err != nil {
		t.Fatalf("pdftotext command rejected: %v", err)
	}
	for _, arguments := range [][]string{
		{"/bin/sh", source},
		{"/usr/bin/pdfinfo", "/etc/passwd"},
		{"/usr/bin/pdftotext", source, "-"},
	} {
		if _, _, _, err := validatedCommand(arguments); err == nil {
			t.Fatalf("unsafe command accepted: %q", arguments)
		}
	}
}

func TestFilesystemPolicyAllowsOnlySelectedSource(t *testing.T) {
	if os.Getenv("PARSER_SANDBOX_LANDLOCK_HELPER") == "1" {
		source := os.Getenv("PARSER_SANDBOX_SOURCE")
		sibling := os.Getenv("PARSER_SANDBOX_SIBLING")
		executable, err := os.Executable()
		if err != nil {
			os.Exit(10)
		}
		if err = applyFilesystemPolicy(executable, source); err != nil {
			os.Exit(11)
		}
		if _, err = os.ReadFile(source); err != nil {
			os.Exit(12)
		}
		if _, err = os.ReadFile(sibling); err == nil {
			os.Exit(13)
		}
		os.Exit(0)
	}

	directory := t.TempDir()
	source := filepath.Join(directory, "selected.pdf")
	sibling := filepath.Join(directory, "other.pdf")
	for _, path := range []string{source, sibling} {
		if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFilesystemPolicyAllowsOnlySelectedSource$") // #nosec G204 -- re-executes this fixed test binary only.
	command.Env = append(os.Environ(),
		"PARSER_SANDBOX_LANDLOCK_HELPER=1",
		"PARSER_SANDBOX_SOURCE="+source,
		"PARSER_SANDBOX_SIBLING="+sibling,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Landlock helper failed: %v: %s", err, output)
	}
}
