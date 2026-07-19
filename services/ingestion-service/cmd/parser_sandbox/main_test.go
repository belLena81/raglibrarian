package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

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
