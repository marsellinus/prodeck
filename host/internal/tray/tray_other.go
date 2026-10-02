//go:build !(windows && cgo)

package tray

// Start does nothing where there is no notification area.
//
// The tray needs CGO and a Win32 message loop, so a CGO_ENABLED=0 build and
// every non-Windows platform get this instead of a compile error. Available
// reports the truth so the caller can say so rather than leave the user
// wondering where the icon went: the host still runs, closing the window still
// stops it, and that is the behaviour those builds have always had.
func Start(Options, Callbacks) {}

// Ready is already closed: there is no icon to wait for.
func Ready() <-chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Stop does nothing.
func Stop() {}

// Available reports whether this build can show an icon in the notification
// area.
func Available() bool { return false }
