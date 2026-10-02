//go:build !cgo

package gui

import "errors"

// ErrUnavailable is returned when this build cannot open a window.
//
// The web view is a C library, so the panel needs CGO. Rather than let a
// CGO_ENABLED=0 build fail to compile, the package keeps its API and reports the
// reason: the host must stay buildable as a static, cross-compiled binary with
// no window (docs/adr/0009-single-binary-no-docker-dependency.md), and a `gui`
// command that explains itself is better than one that does not exist.
var ErrUnavailable = errors.New(
	"gui: this build has CGO disabled, so the control panel is not available; " +
		"rebuild with CGO_ENABLED=1, or use the command line")

// Options configures the window. It mirrors the CGO build so callers compile
// unchanged either way.
//
// Tray and Address exist here even though this build has no window and no
// notification area: the caller fills in the same struct in both builds, and a
// field that only exists under CGO would make `go build` fail for exactly the
// static, cross-compiled binary the package promises to keep working
// (docs/adr/0009-single-binary-no-docker-dependency.md).
type Options struct {
	Addr    string
	Token   string
	Title   string
	Debug   bool
	Tray    bool
	Address string
}

// Run always fails in this build.
func Run(Options) error { return ErrUnavailable }

// Available always reports false in this build.
func Available() bool { return false }
