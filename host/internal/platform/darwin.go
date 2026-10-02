//go:build darwin

// Package-level implementation of the platform contracts for macOS.
//
// # What is real and what is a stub
//
// Real, implemented and exercised through documented command-line tools:
//
//   - Launcher: `open` / `open -a` for applications, URLs, folders and Terminal.
//   - Shell: /bin/sh and /bin/bash via -c, and script files run directly (their
//     shebang decides the interpreter), with the extension as a fallback.
//   - Power: Lock and Sleep, through `pmset displaysleepnow` / `pmset sleepnow`.
//   - Metrics: CPU, memory, disk, network throughput and uptime, read from
//     sysctl, vm_stat, df and netstat.
//
// Stubs, deliberately, because there is no correct implementation yet:
//
//   - Input is NOT implemented. Synthetic keyboard and mouse events need
//     CGEventPost from the Core Graphics framework, which is not reachable
//     through a Go binding here, and posting events without the Accessibility
//     permission silently does nothing. Every Input method therefore returns
//     ErrUnsupported with a hint, rather than pretending to type.
//   - Power.Shutdown and Power.Restart return ErrUnsupported: both need
//     privileges the agent does not take (docs/SECURITY.md §4).
//
// Do not mistake this file for a working macOS host: input control is missing,
// and PowerCapabilities advertises exactly what works.
package platform

import (
	"context"
	"fmt"
	"time"
)

// New builds the macOS platform adapters.
func New() *Platform {
	return &Platform{
		OSName:   "darwin",
		Input:    darwinInput{},
		Launcher: darwinLauncher{},
		Shell:    darwinShell{},
		Media:    darwinMedia{},
		Sound:    darwinSound{},
		Power:    darwinPower{},
		Metrics:  &darwinMetrics{},
	}
}

// darwinInput is a stub: no input injection is implemented.
//
// The methods are spelled out rather than left to the embedded Unsupported so
// that the failure carries the reason. A caller that sees only ErrUnsupported
// cannot tell "this OS never had it" from "this build forgot it", and the
// operator deserves the difference in the client UI.
type darwinInput struct {
	Unsupported
}

// errNoInput is the single failure every Input method returns.
var errNoInput = fmt.Errorf("%w: this macOS build has no input implementation; typing and mouse control need CGEventPost from Core Graphics (with the Accessibility permission) and are not wired up yet", ErrUnsupported)

func (darwinInput) Key(context.Context, Key, KeyMode) error        { return errNoInput }
func (darwinInput) Shortcut(context.Context, []Key, KeyMode) error { return errNoInput }
func (darwinInput) Text(context.Context, string, time.Duration) error {
	return errNoInput
}
func (darwinInput) MouseClick(context.Context, MouseButton, int) error { return errNoInput }
func (darwinInput) MouseMove(context.Context, int, int) error          { return errNoInput }
func (darwinInput) MouseMoveAbsolute(context.Context, float64, float64) error {
	return errNoInput
}
func (darwinInput) MouseScroll(context.Context, int, int) error { return errNoInput }

// darwinMedia controls the media session with the media key events the OS
// already routes, which needs no player-specific integration.
//
// It is a stub for the same reason as darwinInput: the media keys are delivered
// by posting NSEvent/CGEvent key events, so without the input layer there is
// nothing to send.
type darwinMedia struct {
	Unsupported
}

func (darwinMedia) PlayPause(context.Context) error      { return errNoInput }
func (darwinMedia) Play(context.Context) error           { return errNoInput }
func (darwinMedia) Pause(context.Context) error          { return errNoInput }
func (darwinMedia) Stop(context.Context) error           { return errNoInput }
func (darwinMedia) Next(context.Context) error           { return errNoInput }
func (darwinMedia) Previous(context.Context) error       { return errNoInput }
func (darwinMedia) VolumeUp(context.Context) error       { return errNoInput }
func (darwinMedia) VolumeDown(context.Context) error     { return errNoInput }
func (darwinMedia) Mute(context.Context) error           { return errNoInput }
func (darwinMedia) SetVolume(context.Context, int) error { return errNoInput }
func (darwinMedia) NowPlaying(context.Context) (NowPlaying, bool, error) {
	return NowPlaying{}, false, errNoInput
}
