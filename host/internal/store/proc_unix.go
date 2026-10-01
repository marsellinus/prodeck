//go:build !windows

package store

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
	return errors.Is(err, os.ErrPermission) // exists but owned by another user
}
