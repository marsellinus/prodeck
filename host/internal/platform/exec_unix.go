//go:build !windows

package platform

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the child in its own process group. Without this, a
// script that spawns children would leave them running after the deck cancels
// the parent, and killing the parent would also risk signalling the host's own
// group.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup signals the whole group. SIGKILL rather than SIGTERM because
// this path only runs after the context is already done: the process had its
// chance to exit cleanly and the user asked for it to stop now.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// The process may already be gone, or it may not have had its own
		// group; fall back to the direct signal.
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cmd.Process.Kill()
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return cmd.Process.Kill()
	}
	return nil
}
