//go:build !windows

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts a child that survives its parent and has no controlling
// terminal, so `mobiledeck start` can return while the host keeps running.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.Stdin = nil
}

// terminate asks a process to stop. SIGTERM rather than SIGKILL so the host can
// close its sessions, flush the audit log and cancel running actions.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
