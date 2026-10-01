//go:build darwin

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// darwinLauncher opens things with the system's own opener.
//
// `open` is the documented way to ask LaunchServices to start an application or
// hand a URL to its handler; it also gets the "already running, just focus it"
// behaviour for free, which is what a user pressing an app button expects.
type darwinLauncher struct {
	Unsupported
}

// Open starts an application.
//
// A path is opened by path, so a profile can point at a bundle anywhere; a bare
// name is opened with -a, which is how a profile says "Safari" rather than
// "/Applications/Safari.app". Note that `open` cannot set a working directory:
// the application gets its own, which is what a user launching it from Finder
// would see too.
func (darwinLauncher) Open(_ context.Context, target string, opts LaunchOptions) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("platform: target must not be empty")
	}
	if strings.Contains(target, "://") {
		return darwinOpen(target)
	}

	path := ExpandPath("", target)
	isPath := strings.ContainsRune(target, os.PathSeparator)
	if isPath {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("platform: %s does not exist", path)
		}
		args := []string{path}
		if len(opts.Args) > 0 {
			args = append(args, "--args")
			args = append(args, opts.Args...)
		}
		return darwinOpen(args...)
	}
	args := []string{"-a", target}
	if len(opts.Args) > 0 {
		args = append(args, "--args")
		args = append(args, opts.Args...)
	}
	return darwinOpen(args...)
}

// OpenURL opens a URL in the user's default browser.
func (darwinLauncher) OpenURL(_ context.Context, rawURL string) error {
	url, err := SanitizeURL(rawURL)
	if err != nil {
		return err
	}
	return darwinOpen(url)
}

// OpenFolder opens a directory in Finder.
func (darwinLauncher) OpenFolder(_ context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("platform: folder path must not be empty")
	}
	abs := ExpandPath("", path)
	st, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("platform: %s does not exist", abs)
	}
	if !st.IsDir() {
		return fmt.Errorf("platform: %s is not a directory", abs)
	}
	return darwinOpen(abs)
}

// OpenTerminal opens Terminal, optionally in a directory and running a command.
func (darwinLauncher) OpenTerminal(_ context.Context, opts TerminalOptions) error {
	dir := ExpandPath("", opts.Dir)
	if dir != "" {
		st, err := os.Stat(dir)
		if err != nil {
			return fmt.Errorf("platform: terminal working directory %s does not exist", dir)
		}
		if !st.IsDir() {
			return fmt.Errorf("platform: terminal working directory %s is not a directory", dir)
		}
	}
	command := strings.TrimSpace(opts.Command)
	if command == "" {
		// `open -a Terminal <dir>` starts a shell already in that directory.
		if dir == "" {
			return darwinOpen("-a", "Terminal")
		}
		return darwinOpen("-a", "Terminal", dir)
	}
	// `open` cannot pass a command to Terminal, so AppleScript is used to run
	// one in a new window. The directory is quoted into the script rather than
	// interpolated raw, so a path with a quote cannot break out.
	if _, err := exec.LookPath("osascript"); err != nil {
		return fmt.Errorf("%w: running a command in Terminal needs osascript, which is not installed", ErrUnsupported)
	}
	script := "tell application \"Terminal\" to do script " + appleScriptString(terminalCommand(dir, command))
	return darwinOpenScript(script)
}

// terminalCommand builds the shell line Terminal will run.
func terminalCommand(dir, command string) string {
	if dir == "" {
		return command
	}
	return "cd " + shellQuote(dir) + " && " + command
}

// appleScriptString renders s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// darwinOpen runs `open` and reports a refusal in the operator's terms.
func darwinOpen(args ...string) error {
	cmd := exec.Command("open", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: open %s failed: %w (%s)", strings.Join(args, " "), err, firstLine(string(out)))
	}
	return nil
}

// darwinOpenScript runs one AppleScript snippet.
func darwinOpenScript(script string) error {
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: osascript failed: %w (%s)", err, firstLine(string(out)))
	}
	return nil
}
