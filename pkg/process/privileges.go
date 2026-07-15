// Package process contains small process-lifecycle primitives.
package process

import (
	"fmt"
	"os"
	"syscall"
)

// Identity is the operating-system user and group a service must run as.
type Identity struct{ UID, GID int }

// DropPrivileges permanently drops root privileges when the process starts as root.
func DropPrivileges(identity Identity) error {
	if identity.UID < 1 || identity.GID < 1 {
		return fmt.Errorf("process: UID and GID must be positive")
	}
	if os.Geteuid() != 0 {
		return nil
	}
	if err := syscall.Setgroups([]int{}); err != nil {
		return fmt.Errorf("clear supplementary groups: %w", err)
	}
	if err := syscall.Setgid(identity.GID); err != nil {
		return fmt.Errorf("set GID: %w", err)
	}
	if err := syscall.Setuid(identity.UID); err != nil {
		return fmt.Errorf("set UID: %w", err)
	}
	return nil
}
