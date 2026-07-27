// parser_sandbox applies fail-closed Linux process and syscall restrictions
// before executing one allowlisted document parser command.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const landlockPreflightArgument = "--landlock-preflight"

const (
	defaultParserSandboxMemoryBytes = 1536 << 20
	maximumParserSandboxMemoryBytes = 8 << 30
)

const (
	defaultPDFInfoPath     = "/usr/bin/pdfinfo"
	defaultPDFToTextPath   = "/usr/bin/pdftotext"
	defaultPDFSeparatePath = "/usr/bin/pdfseparate"
	defaultPDFUnitePath    = "/usr/bin/pdfunite"
	defaultEPUBParserPath  = "/usr/local/bin/epub-parser"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == landlockPreflightArgument {
		if preflightFilesystemPolicy() != nil {
			os.Exit(122)
		}
		os.Exit(0)
	}
	path, arguments, sourcePath, workspaceDir, err := validatedCommand(os.Args[1:])
	if err != nil {
		traceParserSandboxValidationFailure(os.Args[1:], err)
		os.Exit(120)
	}
	argv := append([]string{path}, arguments...)
	environment := parserSandboxEnvironment(sourcePath)
	if applyFilesystemPolicy(path, sourcePath, workspaceDir) != nil {
		os.Exit(122)
	}
	if applySeccomp() != nil {
		os.Exit(123)
	}
	traceParserSandboxExec(path, arguments, sourcePath)
	if applyLimits() != nil {
		os.Exit(121)
	}
	if err = syscall.Exec(path, argv, environment); err != nil { // #nosec G204 G702 -- path and argv match the fixed Poppler allowlist above.
		os.Exit(124)
	}
}

func validatedCommand(arguments []string) (string, []string, string, string, error) {
	if len(arguments) < 2 {
		return "", nil, "", "", errors.New("invalid parser command")
	}
	path := arguments[0]
	commandArguments := arguments[1:]
	var sourcePath string
	var workspaceDir string
	switch path {
	case parserSandboxPDFInfoPath():
		if len(commandArguments) != 1 {
			return "", nil, "", "", errors.New("invalid pdfinfo command")
		}
		sourcePath = commandArguments[0]
	case parserSandboxPDFToTextPath():
		if len(commandArguments) != 5 || commandArguments[0] != "-layout" || commandArguments[1] != "-enc" || commandArguments[2] != "UTF-8" || commandArguments[4] != "-" {
			return "", nil, "", "", errors.New("invalid pdftotext command")
		}
		sourcePath = commandArguments[3]
	case parserSandboxPDFSeparatePath():
		if len(commandArguments) != 6 || commandArguments[0] != "-f" || commandArguments[2] != "-l" {
			return "", nil, "", "", errors.New("invalid pdfseparate command")
		}
		if _, err := strconv.Atoi(commandArguments[1]); err != nil {
			return "", nil, "", "", errors.New("invalid pdfseparate command")
		}
		if _, err := strconv.Atoi(commandArguments[3]); err != nil {
			return "", nil, "", "", errors.New("invalid pdfseparate command")
		}
		sourcePath = commandArguments[4]
		if _, err := validatedParserSandboxInputPath(sourcePath, filepath.Dir(sourcePath)); err != nil {
			return "", nil, "", "", err
		}
		workspaceDir = filepath.Dir(commandArguments[5])
		if _, err := validatedParserSandboxOutputPath(commandArguments[5], workspaceDir); err != nil {
			return "", nil, "", "", err
		}
	case parserSandboxPDFUnitePath():
		if len(commandArguments) < 3 {
			return "", nil, "", "", errors.New("invalid pdfunite command")
		}
		workspaceDir = filepath.Dir(commandArguments[len(commandArguments)-1])
		outputPath, err := validatedParserSandboxOutputPath(commandArguments[len(commandArguments)-1], workspaceDir)
		if err != nil {
			return "", nil, "", "", err
		}
		sourcePath = commandArguments[0]
		for _, inputPath := range commandArguments[:len(commandArguments)-1] {
			if _, err = validatedParserSandboxInputPath(inputPath, workspaceDir); err != nil {
				return "", nil, "", "", err
			}
		}
		if outputPath == "" {
			return "", nil, "", "", errors.New("invalid pdfunite command")
		}
	case parserSandboxEPUBParserPath():
		if len(commandArguments) != 1 {
			return "", nil, "", "", errors.New("invalid EPUB parser command")
		}
		sourcePath = commandArguments[0]
	default:
		return "", nil, "", "", errors.New("parser executable is not allowlisted")
	}
	cleaned := filepath.Clean(sourcePath)
	if !filepath.IsAbs(cleaned) || filepath.Dir(cleaned) == "/" || !strings.HasPrefix(cleaned, "/tmp/") {
		return "", nil, "", "", errors.New("parser source path is invalid")
	}
	info, err := os.Lstat(cleaned) // #nosec G703 -- absolute /tmp path is cleaned, shape-checked, and must be a regular non-symlink file.
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, "", "", errors.New("parser source is not a regular file")
	}
	return path, commandArguments, cleaned, workspaceDir, nil
}

func validatedParserSandboxInputPath(value, directory string) (string, error) {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) || filepath.Dir(cleaned) == "/" || !strings.HasPrefix(cleaned, "/tmp/") {
		return "", errors.New("parser source path is invalid")
	}
	if filepath.Dir(cleaned) != filepath.Clean(directory) {
		return "", errors.New("parser source path is invalid")
	}
	info, err := os.Lstat(cleaned) // #nosec G703 -- absolute /tmp path is cleaned, shape-checked, and must be a regular non-symlink file.
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("parser source is not a regular file")
	}
	return cleaned, nil
}

func validatedParserSandboxOutputPath(value, directory string) (string, error) {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) || filepath.Dir(cleaned) == "/" || !strings.HasPrefix(cleaned, "/tmp/") {
		return "", errors.New("parser source path is invalid")
	}
	if filepath.Dir(cleaned) != filepath.Clean(directory) {
		return "", errors.New("parser source path is invalid")
	}
	return cleaned, nil
}

func parserSandboxPDFInfoPath() string {
	return parserSandboxPathEnv("PARSER_SANDBOX_PDFINFO_PATH", defaultPDFInfoPath)
}

func parserSandboxPDFToTextPath() string {
	return parserSandboxPathEnv("PARSER_SANDBOX_PDFTOTEXT_PATH", defaultPDFToTextPath)
}

func parserSandboxEPUBParserPath() string {
	return parserSandboxPathEnv("PARSER_SANDBOX_EPUB_PARSER_PATH", defaultEPUBParserPath)
}

func parserSandboxPathEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func parserSandboxEnvironment(sourcePath string) []string {
	environment := []string{"LANG=C.UTF-8"}
	if value := strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")); value != "" {
		environment = append(environment, "INGESTION_COMMAND_FAILURE_TRACE="+value)
	}
	if sourcePath != "" {
		environment = append(environment, "EPUB_PARSER_SOURCE_PATH="+sourcePath)
	}
	return environment
}

func traceParserSandboxExec(path string, arguments []string, sourcePath string) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "parser_sandbox_exec_trace")
	_, _ = fmt.Fprintf(os.Stderr, "path=%s argc=%d source=%s env_source=%t\n", path, len(arguments), sourcePath, strings.TrimSpace(sourcePath) != "")
}

func traceParserSandboxValidationFailure(arguments []string, err error) {
	if strings.TrimSpace(os.Getenv("INGESTION_COMMAND_FAILURE_TRACE")) == "" {
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "parser_sandbox_validation_trace")
	_, _ = fmt.Fprintf(os.Stderr, "argc=%d error=%s\n", len(arguments), err.Error())
}

// applyFilesystemPolicy installs a fail-closed Landlock allowlist. The parser
// can read exactly its source file and Poppler's runtime data, and can execute
// only the selected Poppler binary. In particular, /tmp siblings, /proc and
// /run/secrets remain inaccessible even after a parser compromise.
func applyFilesystemPolicy(executablePath, sourcePath, workspaceDir string) error {
	ruleset, err := createLandlockRuleset()
	if err != nil {
		return err
	}
	defer func() { _ = closeLandlockFD(ruleset) }()

	readFile := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
	readTree := readFile | unix.LANDLOCK_ACCESS_FS_READ_DIR
	rules := filesystemPolicyRules(executablePath, sourcePath, workspaceDir, readFile, readTree)
	for _, rule := range rules {
		if err = addLandlockPathRule(ruleset, rule.path, rule.access); err != nil {
			return err
		}
	}
	return restrictWithLandlock(ruleset)
}

func filesystemPolicyRules(executablePath, sourcePath, workspaceDir string, readFile, readTree uint64) []struct {
	path   string
	access uint64
} {
	rules := []struct {
		path   string
		access uint64
	}{
		{executablePath, readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib/ld-musl-x86_64.so.1", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib/ld-musl-aarch64.so.1", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib64/ld-linux-x86-64.so.2", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib/ld-linux-aarch64.so.1", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{sourcePath, readFile},
		{"/lib", readTree},
		{"/usr/lib", readTree},
		{"/usr/share/fonts", readTree},
		{"/usr/share/poppler", readTree},
		{"/usr/share/fontconfig", readTree},
		{"/etc/fonts", readTree},
		{"/var/cache/fontconfig", readTree},
		{sourcePath, readFile},
		{"/dev/null", readFile | unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
	}
	if workspaceDir != "" {
		rules = append(rules, struct {
			path   string
			access uint64
		}{
			path:   workspaceDir,
			access: readTree | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SYM,
		})
	}
	return rules
}

func parserSandboxPDFSeparatePath() string {
	return parserSandboxPathEnv("PARSER_SANDBOX_PDFSEPARATE_PATH", defaultPDFSeparatePath)
}

func parserSandboxPDFUnitePath() string {
	return parserSandboxPathEnv("PARSER_SANDBOX_PDFUNITE_PATH", defaultPDFUnitePath)
}

func preflightFilesystemPolicy() error {
	ruleset, err := createLandlockRuleset()
	if err != nil {
		return err
	}
	defer func() { _ = closeLandlockFD(ruleset) }()
	return restrictWithLandlock(ruleset)
}

func createLandlockRuleset() (uintptr, error) {
	handled, err := negotiatedLandlockAccess(landlockABIVersion)
	if err != nil {
		return 0, err
	}
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) // #nosec G103 -- kernel ABI struct owned for the syscall duration.
	if errno != 0 {
		return 0, errno
	}
	return ruleset, nil
}

func closeLandlockFD(fileDescriptor uintptr) error {
	_, _, errno := unix.Syscall(unix.SYS_CLOSE, fileDescriptor, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockABIVersion() (uintptr, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return version, nil
}

func negotiatedLandlockAccess(queryABIVersion func() (uintptr, error)) (uint64, error) {
	abiVersion, err := queryABIVersion()
	if err != nil {
		return 0, err
	}
	return landlockAccessFS(abiVersion)
}

func landlockAccessFS(abiVersion uintptr) (uint64, error) {
	if abiVersion < 1 {
		return 0, fmt.Errorf("unsupported Landlock ABI version %d", abiVersion)
	}
	handled := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
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
	if abiVersion >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abiVersion >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return handled, nil
}

func restrictWithLandlock(ruleset uintptr) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func addLandlockPathRule(ruleset uintptr, path string, access uint64) error {
	file, err := os.Open(path) // #nosec G304 -- every path is either the validated source or a fixed runtime allowlist entry.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(file.Fd())}                                                   // #nosec G115 -- open descriptors fit the process descriptor limit.
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&attr)), 0, 0, 0) // #nosec G103 -- kernel ABI struct owned for the syscall duration.
	if errno != 0 {
		return errno
	}
	return nil
}

func applyLimits() error {
	limits := parserSandboxLimits()
	for _, limit := range limits {
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: limit.value, Max: limit.value}); err != nil {
			return err
		}
	}
	return nil
}

func parserSandboxLimits() []struct {
	resource int
	value    uint64
} {
	memoryLimit := uint64(parserSandboxMemoryLimitBytes()) // #nosec G115 -- parserSandboxMemoryLimitBytes returns a positive bounded value.
	return []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CPU, 60},
		// Go-based parsers such as epub-parser need more virtual address space
		// than their live heap suggests. Keep this configurable and aligned
		// with the worker memory overcommit guard.
		{unix.RLIMIT_AS, memoryLimit},
		{unix.RLIMIT_NOFILE, 64},
		{unix.RLIMIT_CORE, 0},
		{unix.RLIMIT_FSIZE, 67108864},
	}
}

func parserSandboxMemoryLimitBytes() int64 {
	value := strings.TrimSpace(os.Getenv("INGESTION_PARSER_SANDBOX_MEMORY_BYTES"))
	if value == "" {
		return defaultParserSandboxMemoryBytes
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < defaultParserSandboxMemoryBytes || parsed > maximumParserSandboxMemoryBytes {
		return defaultParserSandboxMemoryBytes
	}
	return parsed
}

func applySeccomp() error {
	architecture, ok := auditArchitecture()
	if !ok {
		return errors.New("unsupported parser architecture")
	}
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: architecture},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	}
	denied := []uint32{
		unix.SYS_SOCKET, unix.SYS_SOCKETPAIR, unix.SYS_CONNECT, unix.SYS_BIND, unix.SYS_LISTEN,
		unix.SYS_ACCEPT, unix.SYS_ACCEPT4, unix.SYS_SENDTO, unix.SYS_RECVFROM, unix.SYS_SENDMSG,
		unix.SYS_RECVMSG, unix.SYS_SHUTDOWN, unix.SYS_PTRACE, unix.SYS_MOUNT, unix.SYS_UMOUNT2,
		unix.SYS_PIVOT_ROOT, unix.SYS_KEXEC_LOAD, unix.SYS_INIT_MODULE, unix.SYS_FINIT_MODULE,
		unix.SYS_DELETE_MODULE, unix.SYS_BPF, unix.SYS_USERFAULTFD, unix.SYS_PERF_EVENT_OPEN,
		unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY,
	}
	for _, systemCall := range denied {
		filter = append(filter,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: systemCall},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		)
	}
	filter = append(filter, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]} // #nosec G115 -- fixed filter is well below uint16.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0) // #nosec G103 -- audited kernel SockFprog pointer with fixed in-process filter lifetime.
}

func auditArchitecture() (uint32, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, true
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, true
	default:
		return 0, false
	}
}
