//go:build !windows

package cli

import (
	"errors"
	"os"
	"syscall"
)

// processAlive reports whether a pid exists and is signalable by this user.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means the process exists but belongs to another user.
	return errors.Is(err, os.ErrPermission)
}
