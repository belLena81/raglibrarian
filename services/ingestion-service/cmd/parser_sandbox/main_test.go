package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/belLena81/raglibrarian/services/ingestion-service/config"
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
		if limit.resource == unix.RLIMIT_AS && limit.value != uint64(config.DefaultParserSandboxMemoryBytes) {
			t.Fatalf("RLIMIT_AS = %d, want %d", limit.value, config.DefaultParserSandboxMemoryBytes)
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
	if _, _, _, _, err := validatedCommand([]string{defaultPDFToTextPath, "-bbox-layout", "-enc", "UTF-8", source, "-"}); err != nil {
		t.Fatalf("pdftotext bbox command rejected: %v", err)
	}
	epubSource := filepath.Join(directory, "source.epub")
	if err := os.WriteFile(epubSource, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	epubArguments := []string{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "8388608", "2097152", epubSource}
	if _, _, _, _, err := validatedCommand(epubArguments); err != nil {
		t.Fatalf("EPUB parser command rejected: %v", err)
	}
	for _, arguments := range [][]string{
		{"/bin/sh", source},
		{defaultPDFInfoPath, "/a/b"},
		{defaultPDFInfoPath, "/etc/passwd"},
		{defaultPDFToTextPath, source, "-"},
		{defaultPDFToTextPath, "-bbox", "-enc", "UTF-8", source, "-"},
		{defaultEPUBParserPath, epubSource},
		{defaultEPUBParserPath, "v2", "2000", "500", "1048576", "8388608", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "0", "500", "1048576", "8388608", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "2", "2", "1048576", "8388608", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "8193", "500", "1048576", "8388608", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "5001", "1048576", "8388608", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "268435457", "536870912", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "2147483649", "2097152", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "2147483648", "1073741825", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "524288", "262144", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "8388608", "16777216", epubSource},
		{defaultEPUBParserPath, "v1", "2000", "500", "1048576", "8388608", "2097152", epubSource, "extra"},
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
	if _, _, _, _, err := validatedCommand([]string{"/work/bin/epub-parser", "v1", "2000", "500", "1048576", "8388608", "2097152", epubSource}); err != nil {
		t.Fatalf("configured EPUB parser command rejected: %v", err)
	}
}

func TestSeccompDeniesNetworkIOUringAndProcessEscapeSyscalls(t *testing.T) {
	if os.Getenv("PARSER_SANDBOX_SECCOMP_HELPER") == "1" {
		if err := applySeccomp(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "applySeccomp failed: %v\n", err)
			os.Exit(10)
		}
		if _, _, errno := unix.Syscall(unix.SYS_GETPID, 0, 0, 0); errno != 0 {
			_, _ = fmt.Fprintf(os.Stderr, "allowed getpid failed after seccomp load: %v\n", errno)
			os.Exit(11)
		}
		os.Exit(0)
	}

	architecture, ok := auditArchitecture()
	if !ok {
		t.Skip("seccomp is not supported on this architecture")
	}
	filter := seccompFilter(architecture)
	denied := []uint32{
		unix.SYS_SOCKET,
		unix.SYS_IO_URING_SETUP,
		unix.SYS_IO_URING_REGISTER,
		unix.SYS_IO_URING_ENTER,
		unix.SYS_CLONE3,
		unix.SYS_SETSID,
		unix.SYS_SETPGID,
		unix.SYS_UNSHARE,
		unix.SYS_SETNS,
	}
	if architecture == unix.AUDIT_ARCH_X86_64 {
		denied = append(denied, 57, 58)
	}
	for _, systemCall := range denied {
		assertSeccompFilterDeniesSyscall(t, filter, systemCall)
	}
	assertSeccompFilterRestrictsProcessClone(t, filter)

	command := exec.Command(os.Args[0], "-test.run=^TestSeccompDeniesNetworkIOUringAndProcessEscapeSyscalls$") // #nosec G204 -- re-executes this fixed test binary only.
	command.Env = append(os.Environ(),
		"PARSER_SANDBOX_SECCOMP_HELPER=1",
		"GOMAXPROCS=1",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seccomp helper failed: %v: %s", err, output)
	}
}

func assertSeccompFilterDeniesSyscall(t *testing.T, filter []unix.SockFilter, systemCall uint32) {
	t.Helper()
	wantReject := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	for index := 0; index < len(filter)-1; index++ {
		jump := filter[index]
		reject := filter[index+1]
		if jump.Code == unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K &&
			jump.K == systemCall &&
			jump.Jf == 1 &&
			reject.Code == unix.BPF_RET|unix.BPF_K &&
			reject.K == wantReject {
			return
		}
	}
	t.Fatalf("seccomp filter does not deny syscall %d with EPERM", systemCall)
}

func assertSeccompFilterRestrictsProcessClone(t *testing.T, filter []unix.SockFilter) {
	t.Helper()
	wantReject := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	for index := 0; index < len(filter)-3; index++ {
		cloneCheck := filter[index]
		flagsLoad := filter[index+1]
		threadCheck := filter[index+2]
		reject := filter[index+3]
		if cloneCheck.Code == unix.BPF_JMP|unix.BPF_JEQ|unix.BPF_K &&
			cloneCheck.K == unix.SYS_CLONE &&
			cloneCheck.Jf == 3 &&
			flagsLoad.Code == unix.BPF_LD|unix.BPF_W|unix.BPF_ABS &&
			flagsLoad.K == 16 &&
			threadCheck.Code == unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K &&
			threadCheck.K == unix.CLONE_THREAD &&
			threadCheck.Jt == 1 &&
			reject.Code == unix.BPF_RET|unix.BPF_K &&
			reject.K == wantReject {
			return
		}
	}
	t.Fatal("seccomp filter does not reject process-style clone with EPERM")
}

func TestSeccompFilterRejectsX32BeforeSyscallComparisons(t *testing.T) {
	filter := seccompFilter(unix.AUDIT_ARCH_X86_64)
	if len(filter) < 7 {
		t.Fatalf("seccomp filter length = %d", len(filter))
	}
	loadNumber := filter[3]
	if loadNumber.Code != unix.BPF_LD|unix.BPF_W|unix.BPF_ABS || loadNumber.K != 0 {
		t.Fatalf("seccomp filter does not load syscall number first: %#v", loadNumber)
	}
	x32Check := filter[4]
	if x32Check.Code != unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K ||
		x32Check.K != x32SyscallBit ||
		x32Check.Jf != 1 {
		t.Fatalf("seccomp filter x32 guard is invalid: %#v", x32Check)
	}
	x32Reject := filter[5]
	wantReject := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	if x32Reject.Code != unix.BPF_RET|unix.BPF_K || x32Reject.K != wantReject {
		t.Fatalf("seccomp filter x32 disposition is invalid: %#v", x32Reject)
	}

	arm64Filter := seccompFilter(unix.AUDIT_ARCH_AARCH64)
	for _, instruction := range arm64Filter {
		if instruction.Code == unix.BPF_JMP|unix.BPF_JSET|unix.BPF_K && instruction.K == x32SyscallBit {
			t.Fatal("arm64 seccomp filter unexpectedly contains an x32 guard")
		}
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
