//go:build windows

package platform

import (
	"os/exec"
	"strconv"
	"syscall"
)

// newProcessGroupFlag makes the child its own process group (CREATE_NEW_PROCESS_GROUP).
const newProcessGroupFlag = 0x00000200

// setProcessGroup puts the child in its own process group so that a script
// which spawns children can be torn down without touching the host's console
// group. CREATE_NO_WINDOW is deliberately not set: a launched application must
// be able to show its own window.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= newProcessGroupFlag
}

// killProcessGroup force-terminates the child and every process it spawned.
//
// taskkill is used rather than TerminateProcess because a script's children are
// not in a Windows job object by default: TerminateProcess would kill only the
// direct child and leave grandchildren (a shell's subprocesses) alive, which is
// exactly the orphan problem this exists to solve.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	if err := kill.Run(); err != nil {
		// taskkill is missing or refused; still guarantee the direct child dies.
		return cmd.Process.Kill()
	}
	return nil
}
