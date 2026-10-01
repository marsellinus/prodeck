//go:build windows

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// Windows process creation flags.
const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
	createNoWindow        = 0x08000000
)

// detach starts a child that survives its parent.
//
// DETACHED_PROCESS is what makes the background host keep running after the
// launching shell exits; CREATE_NEW_PROCESS_GROUP makes it its own console group
// so a Ctrl+C in the parent's console is not delivered to it.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags = detachedProcess | createNewProcessGroup | createNoWindow
	cmd.Stdin = nil
}

// terminate asks a process to stop.
//
// Windows has no SIGTERM. The host installs a console control handler and the
// documented way to reach it from another process is to send a Ctrl+Break event
// to the target's process group; a process started with CREATE_NEW_PROCESS_GROUP
// (see detach) is reachable this way. If that fails, the process is killed,
// which loses the clean shutdown but is better than leaving it running.
func terminate(p *os.Process) error {
	if err := sendCtrlBreak(p.Pid); err == nil {
		return nil
	}
	return p.Kill()
}

// sendCtrlBreak generates a CTRL_BREAK_EVENT in the target's process group.
func sendCtrlBreak(pid int) error {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	generateConsoleCtrlEvent := kernel32.NewProc("GenerateConsoleCtrlEvent")
	const ctrlBreakEvent = 1

	// The event is delivered to a process group, and we pass the pid because the
	// child was created as its own group leader, so its pid is its group id.
	r, _, err := generateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(uint32(pid)))
	if r == 0 {
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", err)
	}
	return nil
}
