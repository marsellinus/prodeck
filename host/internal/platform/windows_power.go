//go:build windows

package platform

import (
	"context"
	"fmt"
)

// procLockWorkStation is resolved from the same user32 handle the input layer
// uses. LockWorkStation is not wrapped by x/sys, so it is called directly.
var procLockWorkStation = user32.NewProc("LockWorkStation")

// windowsPower performs session and machine power actions.
type windowsPower struct {
	Unsupported
}

// Lock locks the workstation, exactly as Win+L does.
func (windowsPower) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// LockWorkStation has no useful return value: it succeeds unless the
	// session is already locked or the caller may not lock it.
	if r, _, err := procLockWorkStation.Call(); r == 0 {
		return &PermissionError{
			Op:     "locking the workstation",
			Reason: err.Error(),
			Hint:   "the agent must run in the user's interactive session; a service account cannot lock another user's desktop",
		}
	}
	return nil
}

// Sleep suspends the machine.
//
// SetSuspendState is reached through rundll32 because its arguments are not
// usable from Go: the third parameter is the hibernate flag, and passing 0
// keeps this a suspend rather than a hibernate-to-disk.
func (windowsPower) Sleep(ctx context.Context) error {
	return runPowerTool(ctx, "sleeping", "rundll32.exe", "powrprof.dll,SetSuspendState", "0,1,0")
}

// Shutdown powers the machine off.
func (windowsPower) Shutdown(ctx context.Context) error {
	return runPowerTool(ctx, "shutting down", "shutdown.exe", "/s", "/t", "0")
}

// Restart reboots the machine.
func (windowsPower) Restart(ctx context.Context) error {
	return runPowerTool(ctx, "restarting", "shutdown.exe", "/r", "/t", "0")
}

// Capabilities reports what this host can do. Windows exposes all four to an
// interactive session, so they are advertised as available.
func (windowsPower) Capabilities() PowerCaps {
	return PowerCaps{Lock: true, Sleep: true, Shutdown: true, Restart: true}
}

// runPowerTool runs one of the OS power utilities and reports a refusal in the
// operator's terms rather than as a bare exit code.
func runPowerTool(ctx context.Context, what, name string, args ...string) error {
	cmd := CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &PermissionError{
			Op:     what,
			Reason: fmt.Sprintf("%s failed: %v (%s)", name, err, firstLine(string(out))),
			Hint:   "this usually means the account the agent runs as may not perform power actions, or a group policy blocks them",
		}
	}
	return nil
}
