//go:build linux

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

// systemdMarker is present on a machine whose services are managed by systemd.
// Its absence (containers, some distros, WSL) is why suspend/poweroff cannot be
// advertised: those verbs would fail at press time.
const systemdMarker = "/run/systemd/system"

// linuxPower performs session and machine power actions.
type linuxPower struct {
	Unsupported
}

// Capabilities reports what this host can actually do.
//
// Lock is always advertised because the fallbacks are session-level and
// harmless to try; the machine actions depend on systemd, which is the only
// supported way to request them without sudo (docs/SECURITY.md §4).
func (linuxPower) Capabilities() PowerCaps {
	_, systemd := os.Stat(systemdMarker)
	haveSystemd := systemd == nil
	return PowerCaps{
		Lock:     true,
		Sleep:    haveSystemd,
		Shutdown: haveSystemd,
		Restart:  haveSystemd,
	}
}

// Lock locks the session, trying the three mechanisms in order of how much of
// the desktop they know about.
func (linuxPower) Lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type attempt struct {
		name string
		args []string
	}
	// loginctl is first because it locks the actual login session (what the
	// screen locker is told to do); the others are X11-era fallbacks for
	// desktops without logind.
	for _, a := range []attempt{
		{"loginctl", []string{"lock-session"}},
		{"xdg-screensaver", []string{"lock"}},
		{"gnome-screensaver-command", []string{"-l"}},
	} {
		if _, err := exec.LookPath(a.name); err != nil {
			continue
		}
		cmd := CommandContext(ctx, a.name, a.args...)
		if _, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else if ctx.Err() != nil {
			// The context ended, so this is not the locker refusing.
			return ctx.Err()
		}
		// Otherwise try the next mechanism; only the exhausted list is reported.
	}
	return fmt.Errorf("%w: no screen locker was found; install logind (systemd), xdg-utils or gnome-screensaver", ErrUnsupported)
}

// Sleep suspends the machine.
func (linuxPower) Sleep(ctx context.Context) error {
	return systemctlAction(ctx, "suspend")
}

// Shutdown powers the machine off.
func (linuxPower) Shutdown(ctx context.Context) error {
	return systemctlAction(ctx, "poweroff")
}

// Restart reboots the machine.
func (linuxPower) Restart(ctx context.Context) error {
	return systemctlAction(ctx, "reboot")
}

// systemctlAction runs one systemd verb.
//
// sudo is never used: an action that needs it is reported as unsupported so the
// operator can decide, rather than the agent quietly asking for a password.
func systemctlAction(ctx context.Context, verb string) error {
	if _, err := os.Stat(systemdMarker); err != nil {
		return fmt.Errorf("%w: systemd is not managing this machine, so %s is not available", ErrUnsupported, verb)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("%w: systemctl is not installed, so %s is not available", ErrUnsupported, verb)
	}
	cmd := CommandContext(ctx, "systemctl", verb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &PermissionError{
			Op:     verb,
			Reason: fmt.Sprintf("systemctl %s failed: %v (%s)", verb, err, firstLine(string(out))),
			Hint:   "the account the agent runs as must be allowed to " + verb + " through polkit; see docs/SECURITY.md",
		}
	}
	return nil
}
