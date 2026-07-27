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

func TestParserSandboxLimitsDoNotRestrictProcessCount(t *testing.T) {
	for _, limit := range parserSandboxLimits() {
		if limit.resource == unix.RLIMIT_NPROC {
			t.Fatal("parser sandbox still limits process count")
		}
	}
}

func TestParserSandboxMemoryLimitUsesEPUBSafeDefault(t *testing.T) {
	t.Setenv("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", "")
	for _, limit := range parserSandboxLimits() {
		if limit.resource == unix.RLIMIT_AS && limit.value != uint64(defaultParserSandboxMemoryBytes) {
			t.Fatalf("RLIMIT_AS = %d, want %d", limit.value, defaultParserSandboxMemoryBytes)
		}
	}
}

func TestParserSandboxMemoryLimitAcceptsBoundedOverride(t *testing.T) {
	t.Setenv("INGESTION_PARSER_SANDBOX_MEMORY_BYTES", "2147483648")
	for _, limit := range parserSandboxLimits() {
		if limit.resource == unix.RLIMIT_AS && limit.value != 2147483648 {
			t.Fatalf("RLIMIT_AS = %d, want 2147483648", limit.value)
		}
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
	if _, _, _, _, err := validatedCommand([]string{defaultPDFInfoPath, source}); err != nil {
		t.Fatalf("pdfinfo command rejected: %v", err)
	}
	if _, _, _, _, err := validatedCommand([]string{defaultPDFToTextPath, "-layout", "-enc", "UTF-8", source, "-"}); err != nil {
		t.Fatalf("pdftotext command rejected: %v", err)
	}
	epubSource := filepath.Join(directory, "source.epub")
	if err := os.WriteFile(epubSource, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := validatedCommand([]string{defaultEPUBParserPath, epubSource}); err != nil {
		t.Fatalf("EPUB parser command rejected: %v", err)
	}
	for _, arguments := range [][]string{
		{"/bin/sh", source},
		{defaultPDFInfoPath, "/a/b"},
		{defaultPDFInfoPath, "/etc/passwd"},
		{defaultPDFToTextPath, source, "-"},
		{defaultEPUBParserPath, epubSource, "extra"},
	} {
		if _, _, _, _, err := validatedCommand(arguments); err == nil {
			t.Fatalf("unsafe command accepted: %q", arguments)
		}
	}
}

func TestValidatedCommandAllowsPreviewPopplerCommands(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source.pdf")
	if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(directory, "output")
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPattern := filepath.Join(outputDir, "catalog-preview-page-%03d.pdf")
	if _, _, _, _, err := validatedCommand([]string{defaultPDFSeparatePath, "-f", "1", "-l", "3", source, outputPattern}); err != nil {
		t.Fatalf("pdfseparate command rejected: %v", err)
	}
	pageOne := filepath.Join(outputDir, "page-001.pdf")
	pageTwo := filepath.Join(outputDir, "page-002.pdf")
	for _, path := range []string{pageOne, pageTwo} {
		if err := os.WriteFile(path, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fragment := filepath.Join(outputDir, "catalog-preview-fragment.pdf")
	if _, _, _, _, err := validatedCommand([]string{defaultPDFUnitePath, pageOne, pageTwo, fragment}); err != nil {
		t.Fatalf("pdfunite command rejected: %v", err)
	}
}

func TestValidatedCommandAcceptsConfiguredParserPaths(t *testing.T) {
	t.Setenv("PARSER_SANDBOX_PDFINFO_PATH", "/opt/tools/pdfinfo")
	t.Setenv("PARSER_SANDBOX_PDFTOTEXT_PATH", "/opt/tools/pdftotext")
	t.Setenv("PARSER_SANDBOX_EPUB_PARSER_PATH", "/work/bin/epub-parser")

	directory := t.TempDir()
	source := filepath.Join(directory, "source.pdf")
	if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := validatedCommand([]string{"/opt/tools/pdfinfo", source}); err != nil {
		t.Fatalf("configured pdfinfo command rejected: %v", err)
	}
	if _, _, _, _, err := validatedCommand([]string{"/opt/tools/pdftotext", "-layout", "-enc", "UTF-8", source, "-"}); err != nil {
		t.Fatalf("configured pdftotext command rejected: %v", err)
	}
	epubSource := filepath.Join(directory, "source.epub")
	if err := os.WriteFile(epubSource, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := validatedCommand([]string{"/work/bin/epub-parser", epubSource}); err != nil {
		t.Fatalf("configured EPUB parser command rejected: %v", err)
	}
}

func TestFilesystemPolicyAllowsPreviewWorkspaceWrites(t *testing.T) {
	if os.Getenv("PARSER_SANDBOX_WORKSPACE_HELPER") == "1" {
		source := os.Getenv("PARSER_SANDBOX_SOURCE")
		output := os.Getenv("PARSER_SANDBOX_OUTPUT")
		executable, err := os.Executable()
		if err != nil {
			os.Exit(10)
		}
		if err = applyFilesystemPolicy(executable, source, filepath.Dir(output)); err != nil {
			os.Exit(11)
		}
		if err = os.WriteFile(output, []byte("preview"), 0o600); err != nil {
			os.Exit(12)
		}
		if _, err = os.ReadFile(source); err != nil {
			os.Exit(13)
		}
		os.Exit(0)
	}

	directory := t.TempDir()
	source := filepath.Join(directory, "selected.pdf")
	output := filepath.Join(directory, "page-001.pdf")
	if err := os.WriteFile(source, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestFilesystemPolicyAllowsPreviewWorkspaceWrites$") // #nosec G204 -- re-executes this fixed test binary only.
	command.Env = append(os.Environ(),
		"PARSER_SANDBOX_WORKSPACE_HELPER=1",
		"PARSER_SANDBOX_SOURCE="+source,
		"PARSER_SANDBOX_OUTPUT="+output,
	)
	if outputBytes, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Landlock preview helper failed: %v: %s", err, outputBytes)
	}
}

func TestParserSandboxEnvironmentIncludesEPUBSourcePath(t *testing.T) {
	environment := parserSandboxEnvironment("/tmp/source.epub")
	if !containsEnvironmentValue(environment, "LANG=C.UTF-8") {
		t.Fatalf("environment missing LANG entry: %#v", environment)
	}
	if !containsEnvironmentValue(environment, "EPUB_PARSER_SOURCE_PATH=/tmp/source.epub") {
		t.Fatalf("environment missing EPUB source entry: %#v", environment)
	}
}

func TestParserSandboxEnvironmentOmitsEmptyEPUBSourcePath(t *testing.T) {
	environment := parserSandboxEnvironment("")
	if containsEnvironmentValue(environment, "EPUB_PARSER_SOURCE_PATH=") {
		t.Fatalf("environment unexpectedly exposed empty EPUB source: %#v", environment)
	}
}

func TestFilesystemPolicyRulesIncludeDynamicLoaderPaths(t *testing.T) {
	rules := filesystemPolicyRules("/bin/parser", "/tmp/source.pdf", "", unix.LANDLOCK_ACCESS_FS_READ_FILE, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR)
	paths := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		paths[rule.path] = struct{}{}
	}
	for _, path := range []string{
		"/lib64/ld-linux-x86-64.so.2",
		"/lib/ld-linux-aarch64.so.1",
		"/lib/ld-musl-x86_64.so.1",
		"/lib/ld-musl-aarch64.so.1",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("filesystem policy missing loader path %q", path)
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
		if err = applyFilesystemPolicy(executable, source, ""); err != nil {
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

func containsEnvironmentValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
