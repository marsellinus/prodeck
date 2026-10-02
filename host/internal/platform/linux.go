//go:build linux

// Package-level implementation of the platform contracts for Linux.
//
// The desktop side of Linux is X11/Wayland plus a pile of loosely standard
// tools; each adapter below uses the narrowest mechanism that is actually
// specified, and reports ErrUnsupported rather than guessing when none is
// available. Nothing here elevates privileges.
package platform

// New builds the Linux platform adapters.
func New() *Platform {
	return &Platform{
		OSName:   "linux",
		Input:    newLinuxInput(),
		Launcher: linuxLauncher{},
		Shell:    linuxShell{},
		Media:    &linuxMedia{},
		Sound:    linuxSound{},
		Power:    linuxPower{},
		Metrics:  &linuxMetrics{},
	}
}
