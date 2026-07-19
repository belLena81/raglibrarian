// parser_sandbox applies fail-closed Linux process and syscall restrictions
// before executing one allowlisted Poppler command.
package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func main() {
	path, arguments, sourcePath, err := validatedCommand(os.Args[1:])
	if err != nil {
		os.Exit(120)
	}
	if applyLimits() != nil {
		os.Exit(121)
	}
	if applyFilesystemPolicy(path, sourcePath) != nil {
		os.Exit(122)
	}
	if applySeccomp() != nil {
		os.Exit(123)
	}
	if err = syscall.Exec(path, append([]string{path}, arguments...), []string{"LANG=C.UTF-8"}); err != nil { // #nosec G204 G702 -- path and argv match the fixed Poppler allowlist above.
		os.Exit(124)
	}
}

func validatedCommand(arguments []string) (string, []string, string, error) {
	if len(arguments) < 2 {
		return "", nil, "", errors.New("invalid parser command")
	}
	path := arguments[0]
	commandArguments := arguments[1:]
	var sourcePath string
	switch path {
	case "/usr/bin/pdfinfo":
		if len(commandArguments) != 1 {
			return "", nil, "", errors.New("invalid pdfinfo command")
		}
		sourcePath = commandArguments[0]
	case "/usr/bin/pdftotext":
		if len(commandArguments) != 5 || commandArguments[0] != "-layout" || commandArguments[1] != "-enc" || commandArguments[2] != "UTF-8" || commandArguments[4] != "-" {
			return "", nil, "", errors.New("invalid pdftotext command")
		}
		sourcePath = commandArguments[3]
	default:
		return "", nil, "", errors.New("parser executable is not allowlisted")
	}
	cleaned := filepath.Clean(sourcePath)
	if !filepath.IsAbs(cleaned) || filepath.Dir(cleaned) == "/" || cleaned[:5] != "/tmp/" {
		return "", nil, "", errors.New("parser source path is invalid")
	}
	info, err := os.Lstat(cleaned) // #nosec G703 -- absolute /tmp path is cleaned, shape-checked, and must be a regular non-symlink file.
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, "", errors.New("parser source is not a regular file")
	}
	return path, commandArguments, cleaned, nil
}

// applyFilesystemPolicy installs a fail-closed Landlock allowlist. The parser
// can read exactly its source file and Poppler's runtime data, and can execute
// only the selected Poppler binary. In particular, /tmp siblings, /proc and
// /run/secrets remain inaccessible even after a parser compromise.
func applyFilesystemPolicy(executablePath, sourcePath string) error {
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
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REFER |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0) // #nosec G103 -- kernel ABI struct owned for the syscall duration.
	if errno != 0 {
		return errno
	}
	defer func() { _ = unix.Close(int(ruleset)) }() // #nosec G115 -- successful syscall return is a Linux file descriptor.

	readFile := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
	readTree := readFile | unix.LANDLOCK_ACCESS_FS_READ_DIR
	rules := []struct {
		path   string
		access uint64
	}{
		{executablePath, readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib/ld-musl-x86_64.so.1", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{"/lib/ld-musl-aarch64.so.1", readFile | unix.LANDLOCK_ACCESS_FS_EXECUTE},
		{sourcePath, readFile},
		{"/lib", readTree},
		{"/usr/lib", readTree},
		{"/usr/share/fonts", readTree},
		{"/usr/share/poppler", readTree},
		{"/usr/share/fontconfig", readTree},
		{"/etc/fonts", readTree},
		{"/var/cache/fontconfig", readTree},
		{"/dev/null", readFile | unix.LANDLOCK_ACCESS_FS_WRITE_FILE},
	}
	for _, rule := range rules {
		if err := addLandlockPathRule(ruleset, rule.path, rule.access); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
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
	limits := []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_CPU, 60},
		{unix.RLIMIT_AS, 805306368},
		{unix.RLIMIT_NOFILE, 64},
		{unix.RLIMIT_NPROC, 32},
		{unix.RLIMIT_CORE, 0},
		{unix.RLIMIT_FSIZE, 67108864},
	}
	for _, limit := range limits {
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: limit.value, Max: limit.value}); err != nil {
			return err
		}
	}
	return nil
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
