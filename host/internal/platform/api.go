// Package platform is the only place in the host that knows an operating
// system exists.
//
// Everything above this package talks to interfaces (Input, Launcher, Shell,
// Media, Power, Metrics). Everything below it is a build-tagged implementation
// for one OS. Porting the host to a new OS means adding files here and changing
// nothing else (ARCHITECTURE.md §2).
//
// Operations an OS cannot perform return ErrUnsupported rather than silently
// doing nothing, so a client always learns that its request had no effect.
package platform

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrUnsupported means the operation is not implemented on this OS or this
// session (for example, media control on a headless Linux box).
var ErrUnsupported = errors.New("platform: operation is not supported on this system")

// ErrPermission means the OS refused the operation. The message is written for
// the person reading it in the client UI, and it always says what to do.
type PermissionError struct {
	Op     string
	Reason string
	Hint   string
}

func (e *PermissionError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("platform: %s was refused by the operating system: %s (%s)", e.Op, e.Reason, e.Hint)
	}
	return fmt.Sprintf("platform: %s was refused by the operating system: %s", e.Op, e.Reason)
}

// KeyMode selects between tapping, holding and releasing a key.
type KeyMode string

const (
	KeyPress KeyMode = "press" // down then up
	KeyDown  KeyMode = "down"  // hold until a matching KeyUp
	KeyUp    KeyMode = "up"
)

// ParseKeyMode validates a mode from the wire, defaulting to press.
func ParseKeyMode(s string) (KeyMode, error) {
	switch s {
	case "", string(KeyPress):
		return KeyPress, nil
	case string(KeyDown):
		return KeyDown, nil
	case string(KeyUp):
		return KeyUp, nil
	default:
		return "", fmt.Errorf("platform: key mode %q must be press, down or up", s)
	}
}

// MouseButton identifies a physical mouse button.
type MouseButton string

const (
	MouseLeft   MouseButton = "left"
	MouseRight  MouseButton = "right"
	MouseMiddle MouseButton = "middle"
)

// ParseMouseButton validates a button name from the wire.
func ParseMouseButton(s string) (MouseButton, error) {
	switch MouseButton(s) {
	case MouseLeft, MouseRight, MouseMiddle:
		return MouseButton(s), nil
	case "":
		return MouseLeft, nil
	default:
		return "", fmt.Errorf("platform: mouse button %q must be left, right or middle", s)
	}
}

// Input injects keyboard and mouse events into the interactive session.
//
// Implementations must be safe for concurrent use and must honour ctx: a
// cancelled context aborts a long text injection rather than finishing it.
type Input interface {
	// Key taps, holds, or releases one key.
	Key(ctx context.Context, key Key, mode KeyMode) error
	// Shortcut applies mode to every key in order, which is how modifiers are
	// expressed: ["CTRL","SHIFT","P"] with mode press means all three go down
	// and come back up, matching what a human does.
	Shortcut(ctx context.Context, keys []Key, mode KeyMode) error
	// Text types a string. interval is the delay between keystrokes; 0 means
	// as fast as the OS accepts. Cancellation must be checked between
	// characters.
	Text(ctx context.Context, text string, interval time.Duration) error
	// MouseClick presses and releases a button count times.
	MouseClick(ctx context.Context, btn MouseButton, count int) error
	// MouseMove moves the pointer by a relative offset in pixels.
	MouseMove(ctx context.Context, dx, dy int) error
	// MouseMoveAbsolute moves the pointer to a fraction of the primary screen,
	// 0..1 on each axis, so a phone-sized control works on any monitor layout.
	MouseMoveAbsolute(ctx context.Context, x, y float64) error
	// MouseScroll scrolls dy notches vertically (positive is up) and dx
	// horizontally (positive is right).
	MouseScroll(ctx context.Context, dx, dy int) error
}

// LaunchOptions carries the optional parts of starting a process.
type LaunchOptions struct {
	Args []string          `json:"args,omitempty"`
	Dir  string            `json:"cwd,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	// Detach starts the process without tying its lifetime to the host. It is
	// the default for launch_application: a terminal started from the deck must
	// outlive the button press.
	Detach bool `json:"detach,omitempty"`
}

// TerminalOptions describes opening a terminal emulator.
type TerminalOptions struct {
	Dir     string `json:"cwd,omitempty"`
	Command string `json:"command,omitempty"`
}

// Launcher starts applications and opens URLs and folders.
type Launcher interface {
	// Open starts a program. target is a platform-resolved string (an absolute
	// path, a PATH lookup name, or a URI on platforms that accept one).
	Open(ctx context.Context, target string, opts LaunchOptions) error
	// OpenURL opens a URL in the user's default browser.
	OpenURL(ctx context.Context, url string) error
	// OpenFolder opens a directory in the user's file manager.
	OpenFolder(ctx context.Context, path string) error
	// OpenTerminal opens a terminal emulator, optionally running a command.
	OpenTerminal(ctx context.Context, opts TerminalOptions) error
}

// ScriptOptions describes a script execution.
type ScriptOptions struct {
	// Path is the resolved script path. The caller (engine) has already
	// confined it; the platform layer must not re-resolve symlinks away.
	Path    string
	Args    []string
	Dir     string
	Env     map[string]string
	Timeout time.Duration
	// Interpreter overrides the shebang-based invocation, e.g. "bash" or
	// "powershell". Empty means "let the OS decide" (exec the file directly on
	// Unix, use the extension association on Windows).
	Interpreter string
}

// CommandOptions describes a shell command.
type CommandOptions struct {
	Command string
	// Shell selects the interpreter: "auto", "sh", "bash", "cmd", "powershell".
	// "auto" picks a sensible default for the OS.
	Shell   string
	Dir     string
	Env     map[string]string
	Timeout time.Duration
}

// ExecResult is the outcome of a script or command.
type ExecResult struct {
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"-"`
	// Truncated reports that stdout/stderr hit the capture cap and the strings
	// above are only a prefix. The client shows this so output is never
	// silently misleading.
	Truncated bool `json:"truncated,omitempty"`
	// TimedOut reports that the process was killed at the deadline.
	TimedOut bool `json:"timed_out,omitempty"`
	// Cancelled reports that ctx ended the process before it finished.
	Cancelled bool `json:"cancelled,omitempty"`
}

// MaxCaptureBytes caps captured output per stream. Anything larger is dropped
// and flagged, because a runaway script must not be able to exhaust host memory
// through the log path.
const MaxCaptureBytes = 64 * 1024

// Shell executes scripts and commands.
type Shell interface {
	RunScript(ctx context.Context, opts ScriptOptions) (ExecResult, error)
	RunCommand(ctx context.Context, opts CommandOptions) (ExecResult, error)
	// SupportedInterpreters lists what this host can actually run, so the CLI
	// and the docs can be honest about it instead of failing at press time.
	SupportedInterpreters() []string
}

// Media controls the OS media session. Implementations must not assume a single
// player: on Linux this is MPRIS, which is a bus with many players.
type Media interface {
	PlayPause(ctx context.Context) error
	Play(ctx context.Context) error
	Pause(ctx context.Context) error
	Stop(ctx context.Context) error
	Next(ctx context.Context) error
	Previous(ctx context.Context) error
	VolumeUp(ctx context.Context) error
	VolumeDown(ctx context.Context) error
	Mute(ctx context.Context) error
	SetVolume(ctx context.Context, percent int) error
	// NowPlaying returns the current track description, or ok=false when
	// nothing is playing. Used for a stateful button.
	NowPlaying(ctx context.Context) (NowPlaying, bool, error)
}

// NowPlaying is a snapshot of the active media session.
type NowPlaying struct {
	Title   string `json:"title,omitempty"`
	Artist  string `json:"artist,omitempty"`
	Album   string `json:"album,omitempty"`
	Playing bool   `json:"playing"`
}

// PowerCaps reports which power actions this host can actually perform. The
// host advertises these so the client can grey out what will not work instead
// of showing a button that always errors.
type PowerCaps struct {
	Lock     bool `json:"lock"`
	Sleep    bool `json:"sleep"`
	Shutdown bool `json:"shutdown"`
	Restart  bool `json:"restart"`
}

// Power performs session and machine power actions. None of these elevate
// privileges; an action that would need to is reported as Unsupported and the
// operator is told why (docs/SECURITY.md §4).
type Power interface {
	Lock(ctx context.Context) error
	Sleep(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Restart(ctx context.Context) error
	Capabilities() PowerCaps
}

// Metrics samples host health for the telemetry stream. Returning a metric
// absent from the map means "not available on this host"; the client renders
// that as "--" rather than zero, which would be a lie.
type Metrics interface {
	Sample(ctx context.Context) (map[string]float64, error)
	// Available lists the metric names this host can produce.
	Available() []string
}

// Platform bundles the adapters for the running OS.
type Platform struct {
	OSName   string
	Input    Input
	Launcher Launcher
	Shell    Shell
	Media    Media
	Power    Power
	Metrics  Metrics
}

// Unsupported is embedded by implementations that do not provide an interface.
// Every method returns ErrUnsupported, which keeps per-OS files honest: they
// only override what they really implement, and a caller always learns that an
// operation had no effect instead of assuming success.
//
// It is exported so a plugin, or a test that needs a deterministic host, can
// build a Platform by overriding only the interfaces it cares about.
type Unsupported struct{}

func (Unsupported) Key(context.Context, Key, KeyMode) error { return ErrUnsupported }
func (Unsupported) Shortcut(context.Context, []Key, KeyMode) error {
	return ErrUnsupported
}
func (Unsupported) Text(context.Context, string, time.Duration) error { return ErrUnsupported }
func (Unsupported) MouseClick(context.Context, MouseButton, int) error {
	return ErrUnsupported
}
func (Unsupported) MouseMove(context.Context, int, int) error { return ErrUnsupported }
func (Unsupported) MouseMoveAbsolute(context.Context, float64, float64) error {
	return ErrUnsupported
}
func (Unsupported) MouseScroll(context.Context, int, int) error { return ErrUnsupported }

func (Unsupported) Open(context.Context, string, LaunchOptions) error { return ErrUnsupported }
func (Unsupported) OpenURL(context.Context, string) error             { return ErrUnsupported }
func (Unsupported) OpenFolder(context.Context, string) error          { return ErrUnsupported }
func (Unsupported) OpenTerminal(context.Context, TerminalOptions) error {
	return ErrUnsupported
}

func (Unsupported) RunScript(context.Context, ScriptOptions) (ExecResult, error) {
	return ExecResult{}, ErrUnsupported
}
func (Unsupported) RunCommand(context.Context, CommandOptions) (ExecResult, error) {
	return ExecResult{}, ErrUnsupported
}
func (Unsupported) SupportedInterpreters() []string { return nil }

func (Unsupported) PlayPause(context.Context) error      { return ErrUnsupported }
func (Unsupported) Play(context.Context) error           { return ErrUnsupported }
func (Unsupported) Pause(context.Context) error          { return ErrUnsupported }
func (Unsupported) Stop(context.Context) error           { return ErrUnsupported }
func (Unsupported) Next(context.Context) error           { return ErrUnsupported }
func (Unsupported) Previous(context.Context) error       { return ErrUnsupported }
func (Unsupported) VolumeUp(context.Context) error       { return ErrUnsupported }
func (Unsupported) VolumeDown(context.Context) error     { return ErrUnsupported }
func (Unsupported) Mute(context.Context) error           { return ErrUnsupported }
func (Unsupported) SetVolume(context.Context, int) error { return ErrUnsupported }
func (Unsupported) NowPlaying(context.Context) (NowPlaying, bool, error) {
	return NowPlaying{}, false, ErrUnsupported
}

func (Unsupported) Lock(context.Context) error     { return ErrUnsupported }
func (Unsupported) Sleep(context.Context) error    { return ErrUnsupported }
func (Unsupported) Shutdown(context.Context) error { return ErrUnsupported }
func (Unsupported) Restart(context.Context) error  { return ErrUnsupported }
func (Unsupported) Capabilities() PowerCaps        { return PowerCaps{} }

func (Unsupported) Sample(context.Context) (map[string]float64, error) {
	return nil, ErrUnsupported
}
func (Unsupported) Available() []string { return nil }
