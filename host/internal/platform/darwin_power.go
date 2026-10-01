//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os/exec"
)

// darwinPower performs session power actions.
//
// Only Lock and Sleep are implemented: both are session-level actions pmset
// performs without extra privileges. Shutdown and Restart are not, and are
// advertised as unavailable so the client greys the buttons out instead of
// showing ones that always fail.
type darwinPower struct {
	Unsupported
}

// Lock locks the screen by putting the display to sleep, which is what the
// login window then requires a password to leave.
func (darwinPower) Lock(ctx context.Context) error {
	return runPmset(ctx, "displaysleepnow")
}

// Sleep suspends the machine.
func (darwinPower) Sleep(ctx context.Context) error {
	return runPmset(ctx, "sleepnow")
}

// Shutdown is not implemented: it needs the agent to hold privileges it does
// not take.
func (darwinPower) Shutdown(context.Context) error {
	return fmt.Errorf("%w: shutting down needs root on macOS, and the agent does not take privileges", ErrUnsupported)
}

// Restart is not implemented for the same reason as Shutdown.
func (darwinPower) Restart(context.Context) error {
	return fmt.Errorf("%w: restarting needs root on macOS, and the agent does not take privileges", ErrUnsupported)
}

// Capabilities reports what this host can actually do.
func (darwinPower) Capabilities() PowerCaps {
	return PowerCaps{Lock: true, Sleep: true, Shutdown: false, Restart: false}
}

// runPmset runs one pmset verb and reports a refusal in the operator's terms.
func runPmset(ctx context.Context, verb string) error {
	if _, err := exec.LookPath("pmset"); err != nil {
		return fmt.Errorf("%w: pmset is not available, so %s cannot be requested", ErrUnsupported, verb)
	}
	cmd := CommandContext(ctx, "pmset", verb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &PermissionError{
			Op:     verb,
			Reason: fmt.Sprintf("pmset %s failed: %v (%s)", verb, err, firstLine(string(out))),
			Hint:   "the agent must run in the user's own login session; a daemon context cannot control the display or sleep",
		}
	}
	return nil
}
